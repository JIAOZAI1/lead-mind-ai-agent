package middleware

import (
	"log/slog"
	"net/http"
	"time"
)

// responseRecorder captures the status code written by downstream handlers
// so it can be logged after the request completes.
type responseRecorder struct {
	http.ResponseWriter
	status int
}

func (r *responseRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}

// Flush delegates to the underlying ResponseWriter's Flusher so wrapping
// this recorder doesn't break SSE handlers that need to flush per chunk.
func (r *responseRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Logging logs each request's method, path, tenant/user identity, status,
// and duration. Tenant/user tagging here is the minimum observability
// bar from PROJECT.md §6.3. Identity is read directly off the request
// headers (the same ones middleware.WithIdentity reads) rather than out
// of context, so this works regardless of whether Logging sits inside or
// outside WithIdentity in the chain — in particular, a request
// WithIdentity itself rejects (missing X-Tenant-Code) still gets a
// meaningful log line instead of one with an empty tenant_code. Status
// is escalated to warn/error so failed requests are easy to filter for
// without needing per-call-site error logging everywhere (though
// handlers should still log the underlying error — this line only ever
// carries the HTTP status, not the Go error value).
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
