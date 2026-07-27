package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// pendingCommand 是一条已下发、正在等待 result 的指令。
type pendingCommand struct {
	// result 由读循环在收到匹配的 result 帧时写入一次。缓冲为 1，保证读循环
	// 即使在调用方已经超时放弃的情况下也不会阻塞。
	result chan ResultPayload
	// approvalExtended 在收到 ack{pending_approval} 时被关闭一次，通知等待方
	// 把超时放宽到审批时长。用 channel 而非 bool 字段，是为了让等待方能在
	// select 里同时等 result 和"超时被延长"这两件事。
	approvalExtended chan struct{}
	extendOnce       sync.Once
	approvalTimeout  time.Duration
}

// Conn 是一台已认证设备的 WebSocket 连接。
//
// 并发约定：WebSocket 不允许并发写同一条连接，因此所有写操作都必须经过
// writeMu。读则只有 readLoop 一个 goroutine 在做。
type Conn struct {
	ws         *websocket.Conn
	deviceID   string
	tenantCode string
	userID     string
	// capabilities 是插件声明支持的指令集。下发前据此拦截，避免把插件不认识
	// 的指令发出去后干等到超时——直接返回 UNSUPPORTED 让模型换个思路更快。
	capabilities map[CommandType]struct{}

	writeMu sync.Mutex

	pendingMu sync.Mutex
	pending   map[string]*pendingCommand

	// closed 在连接结束时关闭，让所有等待中的指令立刻失败而不是干等到超时。
	closed    chan struct{}
	closeOnce sync.Once

	log *slog.Logger
}

func newConn(ws *websocket.Conn, deviceID, tenantCode, userID string, capabilities []string, log *slog.Logger) *Conn {
	caps := make(map[CommandType]struct{}, len(capabilities))
	for _, c := range capabilities {
		caps[CommandType(c)] = struct{}{}
	}
	return &Conn{
		ws:           ws,
		deviceID:     deviceID,
		tenantCode:   tenantCode,
		userID:       userID,
		capabilities: caps,
		pending:      make(map[string]*pendingCommand),
		closed:       make(chan struct{}),
		log:          log,
	}
}

// DeviceID 返回该连接对应的设备 ID。
func (c *Conn) DeviceID() string { return c.deviceID }

// Supports 判断插件是否声明支持某条指令。
func (c *Conn) Supports(t CommandType) bool {
	// 插件没声明 capabilities（旧版本）时不做拦截，交给插件自己回
	// UNSUPPORTED——宁可多一次往返，也不要因为服务端的过度推断而拒绝一台
	// 其实能用的设备。
	if len(c.capabilities) == 0 {
		return true
	}
	_, ok := c.capabilities[t]
	return ok
}

// send 序列化并发送一条消息。WebSocket 禁止并发写，故全程持锁。
func (c *Conn) send(ctx context.Context, env Envelope) error {
	data, err := json.Marshal(env)
	if err != nil {
		return fmt.Errorf("marshal %s envelope: %w", env.Kind, err)
	}

	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	writeCtx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()

	if err := c.ws.Write(writeCtx, websocket.MessageText, data); err != nil {
		return fmt.Errorf("write %s frame to device %s: %w", env.Kind, c.deviceID, err)
	}
	return nil
}

// Execute 下发一条指令并等待其 result。
//
// 超时分两段：先按 baseTimeout 等，如果插件回了 ack{pending_approval}（说明
// 正在等真人点确认），就把上限放宽到插件给出的审批时长。不这么做的话，所有
// 高危指令都会在用户还没来得及看弹窗时就被判超时（插件 DESIGN.md §2.3）。
func (c *Conn) Execute(ctx context.Context, cmd Command, baseTimeout time.Duration) (ResultPayload, error) {
	pending := &pendingCommand{
		result:           make(chan ResultPayload, 1),
		approvalExtended: make(chan struct{}),
	}

	c.pendingMu.Lock()
	if _, exists := c.pending[cmd.CmdID]; exists {
		c.pendingMu.Unlock()
		return ResultPayload{}, fmt.Errorf("command %s already in flight", cmd.CmdID)
	}
	c.pending[cmd.CmdID] = pending
	c.pendingMu.Unlock()

	defer func() {
		c.pendingMu.Lock()
		delete(c.pending, cmd.CmdID)
		c.pendingMu.Unlock()
	}()

	if err := c.send(ctx, newEnvelope(KindCommand, cmd)); err != nil {
		return ResultPayload{}, err
	}

	deadline := time.NewTimer(baseTimeout)
	defer deadline.Stop()

	for {
		select {
		case result := <-pending.result:
			return result, nil

		case <-pending.approvalExtended:
			// 换成审批时长重新计时。approvalExtended 只会被关闭一次
			// （extendOnce），所以这个分支不会反复触发导致无限延长。
			if !deadline.Stop() {
				<-deadline.C
			}
			deadline.Reset(pending.approvalTimeout)

		case <-deadline.C:
			// 超时后主动发 cancel：否则插件可能还在等用户审批，用户点了确认
			// 之后指令仍会真实执行，而服务端早已认定失败——那正是"用户以为
			// 取消了、实际却执行了"的危险错位。
			c.cancelCommand(cmd.CmdID, "server-side timeout")
			return ResultPayload{}, fmt.Errorf("command %s timed out after %s", cmd.CmdID, baseTimeout)

		case <-c.closed:
			return ResultPayload{}, fmt.Errorf("device %s disconnected while command %s was in flight", c.deviceID, cmd.CmdID)

		case <-ctx.Done():
			c.cancelCommand(cmd.CmdID, "caller cancelled")
			return ResultPayload{}, fmt.Errorf("command %s cancelled: %w", cmd.CmdID, ctx.Err())
		}
	}
}

// cancelCommand 尽力通知插件放弃一条指令。失败只记日志：此时调用方已经在
// 走失败路径了，取消通知发不出去不该再改变结果。
func (c *Conn) cancelCommand(cmdID, reason string) {
	ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
	defer cancel()

	err := c.send(ctx, newEnvelope(KindCancel, CancelPayload{CmdID: cmdID, Reason: reason}))
	if err != nil {
		c.log.WarnContext(ctx, "发送 cancel 帧失败",
			slog.String("cmd_id", cmdID),
			slog.String("reason", reason),
			slog.String("error", err.Error()))
	}
}

// deliverResult 把 result 交给等待中的调用方。
func (c *Conn) deliverResult(result ResultPayload) {
	c.pendingMu.Lock()
	pending, ok := c.pending[result.CmdID]
	c.pendingMu.Unlock()

	if !ok {
		// 调用方已超时放弃、或插件重发了一条早已完成的指令的结果。属于正常
		// 情形（协议允许插件重连后补发），记 INFO 不记 ERROR。
		c.log.Info("收到无人等待的指令结果，已丢弃",
			slog.String("cmd_id", result.CmdID),
			slog.String("device_id", c.deviceID))
		return
	}

	// 缓冲为 1 且每个 cmd_id 只会被 deliver 一次（deliver 后调用方即返回并
	// 从 pending 中摘除），因此这里不会阻塞。
	select {
	case pending.result <- result:
	default:
	}
}

// extendForApproval 在收到 ack{pending_approval} 时放宽该指令的超时。
func (c *Conn) extendForApproval(cmdID string, timeout time.Duration) {
	c.pendingMu.Lock()
	pending, ok := c.pending[cmdID]
	c.pendingMu.Unlock()

	if !ok {
		return
	}

	pending.extendOnce.Do(func() {
		pending.approvalTimeout = timeout
		close(pending.approvalExtended)
	})
}

// close 关闭连接并唤醒所有等待中的指令。
func (c *Conn) close(code websocket.StatusCode, reason string) {
	c.closeOnce.Do(func() {
		// 先关 closed 再关 ws：closed 是所有等待方的唤醒信号，即使底层
		// ws.Close 失败或耗时，等待中的指令也必须立刻得到通知。
		close(c.closed)
		if c.ws == nil {
			return
		}
		// 忽略关闭错误：连接可能已经因对端消失而不可写，此时没有任何补救
		// 动作可做，重复记录只会制造噪音。
		_ = c.ws.Close(code, reason)
	})
}
