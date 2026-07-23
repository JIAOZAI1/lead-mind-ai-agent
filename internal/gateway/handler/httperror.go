package handler

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/identity"
)

// httpError 向客户端写入 JSON 格式的错误响应体，并且——不同于直接调用
// http.Error——会通过 slog 在服务端记录底层的 Go error，并打上
// tenant/user/session 标签，以便按 PROJECT.md §6.3 的要求做到可追溯。
// 如果没有这一步，直接写给客户端的 5xx 响应将不会留下任何服务端记录，
// 无法追查请求失败的原因。msg 是面向客户端展示的消息（保持通用/稳定，
// 不包含内部细节）；err 才是真正的失败原因，仅记录日志，绝不暴露给
// 调用方。
func httpError(ctx context.Context, w http.ResponseWriter, r *http.Request, err error, msg string, status int) {
	id, _ := identity.FromContext(ctx)
	level := slog.LevelError
	if status < http.StatusInternalServerError {
		level = slog.LevelWarn
	}
	slog.Log(ctx, level, "request error",
		"method", r.Method,
		"path", r.URL.Path,
		"tenant_code", id.TenantCode,
		"user_id", id.UserID,
		"status", status,
		"error", err,
	)
	http.Error(w, `{"error":"`+msg+`"}`, status)
}
