package middleware

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/tenant"
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

// Logging logs each request's method, path, tenant, status, and duration.
// Tenant tagging here is the minimum observability bar from PROJECT.md §6.3.
func Logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &responseRecorder{ResponseWriter: w, status: http.StatusOK}

		next.ServeHTTP(rec, r)

		tenantID, _ := tenant.FromContext(r.Context())
		slog.Info("request",
			"method", r.Method,
			"path", r.URL.Path,
			"tenant_id", tenantID,
			"status", rec.status,
			"duration_ms", time.Since(start).Milliseconds(),
		)
	})
}
