package browser

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/browser/device"
)

const (
	// authTimeout 是插件建立连接后必须完成认证的时限。在此之前服务端不接受
	// 任何其他类型的消息——未认证连接不该有能力做任何事（插件 DESIGN.md §3.1）。
	authTimeout = 10 * time.Second

	// heartbeatInterval 与插件侧 HEARTBEAT_INTERVAL_MS 保持一致（20s）。
	// 这个值同时承担 MV3 Service Worker 的保活职责，改动需两端同步。
	heartbeatInterval = 20 * time.Second

	// idleTimeout 是连续多久收不到任何帧就判定连接已死。取 3 个心跳周期，
	// 与插件侧 PONG_TIMEOUT_MS 的判定口径一致。
	idleTimeout = heartbeatInterval * 3

	writeTimeout = 10 * time.Second

	// maxFrameBytes 限制单帧大小。read_page 会带回整页正文，需要留足空间；
	// 但不能不设上限——否则一个恶意/失控的插件能用超大帧打爆服务端内存。
	maxFrameBytes = 8 << 20 // 8 MiB
)

// tenantKey 把设备定位到具体租户下。
//
// 用 (tenant_code, device_id) 而非裸 device_id 作为键，是多租户红线的直接
// 体现（PROJECT.md §4.3）：即使两个租户下出现了相同的 device_id，也绝不
// 可能把 A 租户的指令投递到 B 租户的浏览器上。
type tenantKey struct {
	tenantCode string
	deviceID   string
}

// Authenticator 校验 device_token 并解析出它绑定的设备身份。
type Authenticator interface {
	// Authenticate 校验 token。校验失败时返回具体的 AuthErrCode，供服务端
	// 决定是让插件重新配对还是仅仅重试。
	Authenticate(ctx context.Context, tenantCode, token string) (device.Device, AuthErrCode, error)
}

// Hub 维护所有已连接浏览器设备的注册表，并负责把指令下发到指定设备。
type Hub struct {
	auth Authenticator
	// devices 记录设备最近一次连接时间等元数据；连接建立/断开时更新。
	devices device.Store
	log     *slog.Logger

	mu    sync.RWMutex
	conns map[tenantKey]*Conn

	// allowlistFor 返回某租户的域名白名单，在 auth_ok 时下发给插件。为空时
	// 插件完全依赖本地配置的白名单（插件本就不采信服务端的判断，只取交集）。
	allowlistFor func(ctx context.Context, tenantCode, userID string) []string
}

// HubConfig 是构建 Hub 所需的依赖。
type HubConfig struct {
	Authenticator Authenticator
	Devices       device.Store
	Logger        *slog.Logger
	AllowlistFor  func(ctx context.Context, tenantCode, userID string) []string
}

// NewHub 构建一个 Hub。
func NewHub(cfg HubConfig) *Hub {
	log := cfg.Logger
	if log == nil {
		log = slog.Default()
	}
	return &Hub{
		auth:         cfg.Authenticator,
		devices:      cfg.Devices,
		log:          log,
		conns:        make(map[tenantKey]*Conn),
		allowlistFor: cfg.AllowlistFor,
	}
}

// Lookup 返回某个租户下指定设备的连接。deviceID 为空时返回该租户+用户下
// 任意一台在线设备——单设备是绝大多数场景，让调用方不必先查一次设备列表。
func (h *Hub) Lookup(tenantCode, userID, deviceID string) (*Conn, bool) {
	h.mu.RLock()
	defer h.mu.RUnlock()

	if deviceID != "" {
		conn, ok := h.conns[tenantKey{tenantCode: tenantCode, deviceID: deviceID}]
		// 即使按 device_id 精确查找，也必须复核 user_id：device_id 是可枚举的
		// 标识，不校验归属就等于同租户下任意用户都能驱动别人的浏览器。
		if ok && conn.userID == userID {
			return conn, true
		}
		return nil, false
	}

	for key, conn := range h.conns {
		if key.tenantCode == tenantCode && conn.userID == userID {
			return conn, true
		}
	}
	return nil, false
}

// OnlineDevices 返回某租户+用户当前在线的设备 ID 列表。
func (h *Hub) OnlineDevices(tenantCode, userID string) []string {
	h.mu.RLock()
	defer h.mu.RUnlock()

	var out []string
	for key, conn := range h.conns {
		if key.tenantCode == tenantCode && conn.userID == userID {
			out = append(out, key.deviceID)
		}
	}
	return out
}

// Disconnect 主动断开一台设备，用于设备被吊销时立刻切断已建立的连接
// ——只把数据库里的 revoked_at 写上是不够的，正在保持的连接不会因此消失。
//
// 这里**先同步摘除注册表、再异步关闭连接**，顺序很关键：WebSocket 的
// Close 会等待对端回一个 close 帧（coder/websocket 最长等 5 秒），若依赖
// readLoop 结束后才 unregister，被吊销的设备在这几秒内仍能被 Lookup 到并
// 接收指令。吊销的语义是"立刻失去权限"，不能受对端配合程度的影响。
func (h *Hub) Disconnect(tenantCode, deviceID, reason string) {
	key := tenantKey{tenantCode: tenantCode, deviceID: deviceID}

	h.mu.Lock()
	conn, ok := h.conns[key]
	if ok {
		delete(h.conns, key)
	}
	h.mu.Unlock()

	if !ok {
		return
	}

	// close 内部会先关 closed channel（唤醒所有等待中的指令），随后的
	// ws.Close 可能阻塞数秒等待对端应答——放到单独 goroutine，避免让调用方
	// （吊销设备的 HTTP 请求）为此干等。
	go conn.close(websocket.StatusPolicyViolation, reason)
}

// register 把连接登记进注册表。同一设备重复连接时，旧连接会被断开
// ——插件重连（SW 被回收后重启）时旧连接可能尚未被 TCP 层判定失效，留着它
// 会让指令下发到一条实际已死的连接上。
func (h *Hub) register(conn *Conn) {
	key := tenantKey{tenantCode: conn.tenantCode, deviceID: conn.deviceID}

	h.mu.Lock()
	old, exists := h.conns[key]
	h.conns[key] = conn
	h.mu.Unlock()

	if exists {
		h.log.Info("同一设备建立了新连接，断开旧连接",
			slog.String("tenant_code", conn.tenantCode),
			slog.String("device_id", conn.deviceID))
		old.close(websocket.StatusNormalClosure, "superseded by a newer connection")
	}
}

// unregister 摘除连接。只有当注册表里存的仍是这条连接时才删除——否则会误删
// 掉刚刚替换上来的新连接（旧连接被 register 断开后也会走到这里）。
func (h *Hub) unregister(conn *Conn) {
	key := tenantKey{tenantCode: conn.tenantCode, deviceID: conn.deviceID}

	h.mu.Lock()
	if current, ok := h.conns[key]; ok && current == conn {
		delete(h.conns, key)
	}
	h.mu.Unlock()
}

// Serve 接管一条刚建立的 WebSocket 连接：完成认证握手，然后进入读循环，
// 直到连接断开才返回。
func (h *Hub) Serve(ctx context.Context, ws *websocket.Conn, tenantCode string) {
	ws.SetReadLimit(maxFrameBytes)

	conn, err := h.handshake(ctx, ws, tenantCode)
	if err != nil {
		// 握手失败的原因已在 handshake 内部按情况记录（认证失败是 WARN，
		// 基础设施异常才是 ERROR），这里不重复记录。
		return
	}

	h.register(conn)
	defer h.unregister(conn)
	defer conn.close(websocket.StatusNormalClosure, "server closing connection")

	if err := h.devices.TouchLastSeen(ctx, tenantCode, conn.deviceID); err != nil {
		// 只是"最近在线时间"这个展示字段没更新，连接本身完全可用——降级继续。
		h.log.WarnContext(ctx, "更新设备最近在线时间失败",
			slog.String("tenant_code", tenantCode),
			slog.String("device_id", conn.deviceID),
			slog.String("error", err.Error()))
	}

	h.log.InfoContext(ctx, "浏览器设备已连接",
		slog.String("tenant_code", tenantCode),
		slog.String("user_id", conn.userID),
		slog.String("device_id", conn.deviceID))

	h.readLoop(ctx, conn)

	h.log.InfoContext(ctx, "浏览器设备已断开",
		slog.String("tenant_code", tenantCode),
		slog.String("user_id", conn.userID),
		slog.String("device_id", conn.deviceID))
}

// handshake 处理认证首帧。
func (h *Hub) handshake(ctx context.Context, ws *websocket.Conn, tenantCode string) (*Conn, error) {
	authCtx, cancel := context.WithTimeout(ctx, authTimeout)
	defer cancel()

	_, data, err := ws.Read(authCtx)
	if err != nil {
		h.log.WarnContext(ctx, "认证首帧读取失败或超时",
			slog.String("tenant_code", tenantCode),
			slog.String("error", err.Error()))
		_ = ws.Close(websocket.StatusPolicyViolation, "auth timeout")
		return nil, fmt.Errorf("read auth frame: %w", err)
	}

	env, err := decodeEnvelope(data)
	if err != nil {
		h.log.WarnContext(ctx, "认证首帧格式非法",
			slog.String("tenant_code", tenantCode),
			slog.String("error", err.Error()))
		_ = ws.Close(websocket.StatusPolicyViolation, "malformed auth frame")
		return nil, err
	}

	if env.Kind != KindAuth {
		// 认证完成前不接受任何其他消息——这是"未认证连接零权限"的关键一环。
		h.log.WarnContext(ctx, "认证完成前收到了非 auth 帧",
			slog.String("tenant_code", tenantCode),
			slog.String("kind", string(env.Kind)))
		_ = ws.Close(websocket.StatusPolicyViolation, "expected auth frame")
		return nil, fmt.Errorf("expected auth frame, got %s", env.Kind)
	}

	if env.V != ProtocolVersion {
		h.rejectAuth(ctx, ws, AuthErrVersionUnsupported,
			fmt.Sprintf("Protocol version %d is not supported; this server speaks version %d. Please update the extension.", env.V, ProtocolVersion))
		return nil, fmt.Errorf("unsupported protocol version %d", env.V)
	}

	payload, err := decodePayload[AuthPayload](env)
	if err != nil {
		h.rejectAuth(ctx, ws, AuthErrInvalidToken, "Malformed auth payload.")
		return nil, err
	}

	dev, authErrCode, err := h.auth.Authenticate(authCtx, tenantCode, payload.DeviceToken)
	if err != nil {
		// 区分两类失败：凭证问题（认证被拒，正常业务分支，WARN）与基础设施
		// 问题（查库失败，需要人工关注，ERROR）——对齐 PROJECT.md §6.5 关于
		// ERROR 只用于需人工介入的异常的要求。
		if authErrCode != "" {
			h.log.WarnContext(ctx, "设备认证被拒绝",
				slog.String("tenant_code", tenantCode),
				slog.String("code", string(authErrCode)),
				slog.String("plugin_version", payload.PluginVersion),
				slog.String("error", err.Error()))
			h.rejectAuth(ctx, ws, authErrCode, authErrMessage(authErrCode))
			return nil, err
		}

		h.log.ErrorContext(ctx, "设备认证过程发生内部错误",
			slog.String("tenant_code", tenantCode),
			slog.String("plugin_version", payload.PluginVersion),
			slog.String("error", err.Error()))
		_ = ws.Close(websocket.StatusInternalError, "authentication failed")
		return nil, err
	}

	conn := newConn(ws, dev.ID, tenantCode, dev.UserID, payload.Capabilities, h.log)

	var allowlist []string
	if h.allowlistFor != nil {
		allowlist = h.allowlistFor(authCtx, tenantCode, dev.UserID)
	}

	ok := AuthOKPayload{
		DeviceID:    dev.ID,
		TenantCode:  tenantCode,
		UserID:      dev.UserID,
		Allowlist:   allowlist,
		HeartbeatMS: int(heartbeatInterval / time.Millisecond),
	}
	if err := conn.send(authCtx, newEnvelope(KindAuthOK, ok)); err != nil {
		h.log.WarnContext(ctx, "发送 auth_ok 失败",
			slog.String("tenant_code", tenantCode),
			slog.String("device_id", dev.ID),
			slog.String("error", err.Error()))
		return nil, err
	}

	return conn, nil
}

// rejectAuth 回一帧 auth_err 并关闭连接。
//
// 先发消息再关连接，是为了让插件能区分"凭证失效（需重新配对，不该重试）"
// 与"网络抖动（该退避重连）"——直接关闭的话插件只看到一次断线，会无休止地
// 用一个已失效的 token 重连。
func (h *Hub) rejectAuth(ctx context.Context, ws *websocket.Conn, code AuthErrCode, message string) {
	env := newEnvelope(KindAuthErr, AuthErrPayload{Code: code, Message: message})

	writeCtx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()

	if data, err := marshalEnvelope(env); err == nil {
		_ = ws.Write(writeCtx, websocket.MessageText, data)
	}
	_ = ws.Close(websocket.StatusPolicyViolation, string(code))
}

func authErrMessage(code AuthErrCode) string {
	switch code {
	case AuthErrTokenRevoked:
		return "This device has been unlinked. Please pair it again from the web console."
	case AuthErrTokenExpired:
		return "This device credential has expired. Please pair it again from the web console."
	case AuthErrVersionUnsupported:
		return "This extension version is no longer supported. Please update it."
	default:
		return "Invalid device credential. Please pair this device again."
	}
}

// readLoop 持续读取插件发来的帧，直到连接断开。
func (h *Hub) readLoop(ctx context.Context, conn *Conn) {
	for {
		select {
		case <-conn.closed:
			return
		case <-ctx.Done():
			conn.close(websocket.StatusGoingAway, "server shutting down")
			return
		default:
		}

		// 读超时即"连续 idleTimeout 没收到任何帧"。插件每 20s 必发一次 ping，
		// 所以正常连接绝不会触发；触发了就说明对端已经消失（NAT 超时、进程被杀
		// 等 TCP 层不会立刻感知的情况）。
		readCtx, cancel := context.WithTimeout(ctx, idleTimeout)
		_, data, err := conn.ws.Read(readCtx)
		cancel()

		if err != nil {
			// 正常断开（用户关浏览器、SW 被回收）与异常断开在这里无法可靠区分，
			// 统一记 INFO：连接断开是这个架构下的常态而非异常（插件 DESIGN.md §5），
			// 记 ERROR 只会淹没真正需要关注的问题。
			h.log.InfoContext(ctx, "设备连接读取结束",
				slog.String("tenant_code", conn.tenantCode),
				slog.String("device_id", conn.deviceID),
				slog.String("reason", err.Error()))
			conn.close(websocket.StatusNormalClosure, "read loop ended")
			return
		}

		h.handleFrame(ctx, conn, data)
	}
}

// handleFrame 分发一个已读取的帧。
//
// 任何一帧的解析失败都只丢弃该帧、不断开连接：一个畸形帧就能踢掉设备的话，
// 攻击者（或一个有 bug 的插件版本）可以让设备陷入无限重连。
func (h *Hub) handleFrame(ctx context.Context, conn *Conn, data []byte) {
	env, err := decodeEnvelope(data)
	if err != nil {
		h.log.WarnContext(ctx, "收到无法解析的帧，已丢弃",
			slog.String("tenant_code", conn.tenantCode),
			slog.String("device_id", conn.deviceID),
			slog.Int("size", len(data)),
			slog.String("error", err.Error()))
		return
	}

	switch env.Kind {
	case KindPing:
		if err := conn.send(ctx, newEnvelope(KindPong, struct{}{})); err != nil {
			h.log.WarnContext(ctx, "回复 pong 失败",
				slog.String("tenant_code", conn.tenantCode),
				slog.String("device_id", conn.deviceID),
				slog.String("error", err.Error()))
		}

	case KindPong:
		// 服务端不主动发 ping（插件侧负责心跳），收到 pong 说明对端还活着，
		// 读循环的空闲计时已经因这一帧而重置，无需额外处理。

	case KindAck:
		payload, err := decodePayload[AckPayload](env)
		if err != nil {
			h.log.WarnContext(ctx, "ack 帧解析失败", slog.String("error", err.Error()))
			return
		}
		if payload.Status == AckPendingApproval {
			timeout := time.Duration(payload.ApprovalTimeoutMS) * time.Millisecond
			if timeout <= 0 {
				timeout = defaultApprovalTimeout
			}
			conn.extendForApproval(payload.CmdID, timeout)
			h.log.InfoContext(ctx, "指令进入人工审批等待",
				slog.String("tenant_code", conn.tenantCode),
				slog.String("device_id", conn.deviceID),
				slog.String("cmd_id", payload.CmdID),
				slog.Duration("approval_timeout", timeout))
		}

	case KindResult:
		payload, err := decodePayload[ResultPayload](env)
		if err != nil {
			h.log.WarnContext(ctx, "result 帧解析失败", slog.String("error", err.Error()))
			return
		}
		conn.deliverResult(payload)

	case KindEvent:
		payload, err := decodePayload[EventPayload](env)
		if err != nil {
			h.log.WarnContext(ctx, "event 帧解析失败", slog.String("error", err.Error()))
			return
		}
		h.log.InfoContext(ctx, "收到设备事件",
			slog.String("tenant_code", conn.tenantCode),
			slog.String("device_id", conn.deviceID),
			slog.String("event", string(payload.Event)))

	case KindAuth:
		// 已认证的连接上重复认证：可能是插件的状态机出了问题。忽略即可，
		// 不重新认证——那会让一条已建立的连接中途切换身份。
		h.log.WarnContext(ctx, "已认证连接上收到重复的 auth 帧，已忽略",
			slog.String("tenant_code", conn.tenantCode),
			slog.String("device_id", conn.deviceID))

	default:
		h.log.WarnContext(ctx, "收到未知类型的帧，已忽略",
			slog.String("tenant_code", conn.tenantCode),
			slog.String("device_id", conn.deviceID),
			slog.String("kind", string(env.Kind)))
	}
}
