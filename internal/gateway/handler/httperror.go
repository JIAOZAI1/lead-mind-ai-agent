package handler

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/identity"
)

// httpError writes a JSON error body to the client and, unlike a bare
// http.Error call, logs the underlying Go error server-side via slog —
// tagged with tenant/user/session so it's traceable per PROJECT.md §6.3.
// Without this, a 5xx written straight to the client leaves no
// server-side record of why the request failed. msg is the
// client-facing message (kept generic/stable, no internal detail); err
// is the real cause, logged but never exposed to the caller.
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
