package middleware

import (
	"log/slog"
	"net/http"
	"runtime/debug"
)

// Recover catches panics from downstream handlers, logs them with a
// stack trace via slog (tagged with tenant/path like every other log
// line here, unlike Go's default per-connection recovery which writes an
// untagged raw stack trace straight to os.Stderr), and returns a 500
// instead of letting the panic take down the request's goroutine
// silently. Must wrap every handler chain, since an unrecovered panic
// also skips Logging's post-handler log line entirely. Identity is read
// directly off request headers (see Logging's doc comment for why) so
// this works regardless of nesting order relative to WithIdentity.
func Recover(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				slog.Error("panic recovered",
					"method", r.Method,
					"path", r.URL.Path,
					"tenant_code", r.Header.Get(TenantCodeHeader),
					"user_id", r.Header.Get(UserIDHeader),
					"panic", rec,
					"stack", string(debug.Stack()),
				)
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte(`{"error":"internal server error"}`))
			}
		}()
		next.ServeHTTP(w, r)
	})
}
