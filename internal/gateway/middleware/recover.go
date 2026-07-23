package middleware

import (
	"log/slog"
	"net/http"
	"runtime/debug"
)

// Recover 捕获下游 handler 抛出的 panic，通过 slog 记录带堆栈信息的
// 日志（与本文件其他日志一样打上 tenant/path 标签，不同于 Go 默认的
// 按连接级别恢复机制——那种方式会把不带标签的原始堆栈直接写到
// os.Stderr），并返回 500，而不是让 panic 悄无声息地拖垮该请求的
// goroutine。必须包裹每一条 handler 调用链，因为一个未被捕获的
// panic 也会导致 Logging 的请求结束日志完全不会被打印。身份信息直接
// 从请求头读取（原因见 Logging 的文档注释），因此无论相对于
// WithIdentity 的嵌套顺序如何，这里都能正常工作。
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
