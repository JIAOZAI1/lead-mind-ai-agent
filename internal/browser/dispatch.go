package browser

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"

	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/identity"
)

// DefaultCommandTimeout 是一条指令的默认等待上限，与插件侧 dispatcher 的
// DEFAULT_TIMEOUT_MS 一致。高危指令进入人工审批后会由 ack 自动放宽
// （见 Conn.Execute）。
const DefaultCommandTimeout = 15 * time.Second

// Dispatcher 把一条浏览器指令下发到调用方对应的设备并等待结果。
//
// 这是工具层与传输层之间唯一的接缝：工具只描述"做什么"，由本层解决"发给谁"
// ——而"发给谁"完全由 context 里的 identity 决定，工具无法指定，也就无法
// 越权操作别人的浏览器。
type Dispatcher struct {
	hub *Hub
	log *slog.Logger
}

// NewDispatcher 构建一个 Dispatcher。
func NewDispatcher(hub *Hub, log *slog.Logger) *Dispatcher {
	if log == nil {
		log = slog.Default()
	}
	return &Dispatcher{hub: hub, log: log}
}

// Dispatch 下发一条指令并返回其结果。
//
// 租户与用户一律取自 ctx 里的 identity（即本次请求自己的 header），绝不接受
// 调用方传参指定——这是 PROJECT.md §6.2 多租户红线在本模块的落点：模型被
// prompt injection 劫持后也无法把指令发到另一个租户的浏览器上。
func (d *Dispatcher) Dispatch(ctx context.Context, cmdType CommandType, args map[string]any) (ResultPayload, error) {
	id, ok := identity.FromContext(ctx)
	if !ok || id.TenantCode == "" {
		return ResultPayload{}, fmt.Errorf("dispatch %s: no tenant identity in context", cmdType)
	}

	conn, found := d.hub.Lookup(id.TenantCode, id.UserID, "")
	if !found {
		return ResultPayload{}, errDeviceOffline
	}

	if !conn.Supports(cmdType) {
		return ResultPayload{}, fmt.Errorf("device %s does not support command %s", conn.DeviceID(), cmdType)
	}

	if args == nil {
		args = map[string]any{}
	}

	cmd := Command{
		CmdID: uuid.NewString(),
		Type:  cmdType,
		Args:  args,
		Risk:  RiskOf(cmdType),
	}

	started := time.Now()
	result, err := conn.Execute(ctx, cmd, DefaultCommandTimeout)

	logAttrs := []any{
		slog.String("tenant_code", id.TenantCode),
		slog.String("user_id", id.UserID),
		slog.String("device_id", conn.DeviceID()),
		slog.String("cmd_id", cmd.CmdID),
		slog.String("command_type", string(cmdType)),
		slog.Duration("duration", time.Since(started)),
	}

	if err != nil {
		// 超时/断连属于可预期的运行时状况（用户关了浏览器、审批没人点），
		// 不是需要人工介入的服务端异常——记 WARN 而非 ERROR，避免报警噪音
		// （PROJECT.md §6.5）。
		d.log.WarnContext(ctx, "浏览器指令未能完成", append(logAttrs, slog.String("error", err.Error()))...)
		return ResultPayload{}, err
	}

	if !result.OK && result.Error != nil {
		d.log.InfoContext(ctx, "浏览器指令返回失败",
			append(logAttrs, slog.String("error_code", string(result.Error.Code)))...)
	} else {
		d.log.InfoContext(ctx, "浏览器指令执行成功", logAttrs...)
	}

	return result, nil
}

// errDeviceOffline 表示当前用户没有在线的浏览器设备。
var errDeviceOffline = fmt.Errorf("no browser device is currently connected")
