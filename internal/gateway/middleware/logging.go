package middleware

import (
	"log/slog"
	"net/http"
	"time"
)

// responseRecorder 记录下游 handler 写入的状态码，以便在请求结束后
// 记录日志。
type responseRecorder struct {
	http.ResponseWriter
	status int
}

func (r *responseRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// Flush 委托给底层 ResponseWriter 的 Flusher，这样包裹这个 recorder
// 不会破坏那些需要按数据块 flush 的 SSE handler。
func (r *responseRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Logging 记录每个请求的 method、path、tenant/user 身份、状态码和耗时。
// 这里的 tenant/user 标签是 PROJECT.md §6.3 要求的最低可观测性标准。
// 身份信息直接从请求头读取（与 middleware.WithIdentity 读取的是同一批
// 请求头），而不是从 context 读取，所以无论 Logging 在调用链中位于
// WithIdentity 内侧还是外侧都能正常工作——特别是，一个被 WithIdentity
// 自身拒绝的请求（缺少 X-Tenant-Code）依然能得到一条有意义的日志，而
// 不是一条 tenant_code 为空的日志。状态码会被提升为 warn/error 级别，
// 这样失败的请求可以很方便地被过滤出来，不需要在每个调用点都单独加
// 错误日志（不过 handler 仍然应该记录底层错误——这一行日志始终只携带
// HTTP 状态码，不携带 Go 的 error 值）。
func Logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &responseRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(rec, r)

		level := slog.LevelInfo
		if rec.status >= http.StatusInternalServerError {
			level = slog.LevelError
		} else if rec.status >= http.StatusBadRequest {
			level = slog.LevelWarn
		}
		slog.Log(r.Context(), level, "request",
			"method", r.Method,
			"path", r.URL.Path,
			"tenant_code", r.Header.Get(TenantCodeHeader),
			"user_id", r.Header.Get(UserIDHeader),
			"status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}
