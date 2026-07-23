package middleware

import (
	"net/http"

	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/identity"
)

// 携带调用方身份信息的请求头，由本服务上游（API 网关/认证代理）在到达
// 本服务之前注入。本服务对这些请求头的内容全盘信任——它自己不做任何
// 调用方认证。
const (
	TenantCodeHeader = "X-Tenant-Code"
	UserIDHeader     = "X-User-Id"
	UsernameHeader   = "X-Username"
	UserRolesHeader  = "X-User-Roles"
)

// WithIdentity 从请求中读取身份相关请求头，并将其附加到请求的 context
// 上。X-Tenant-Code 是必填的——没有它就无法确定路由到哪个租户，因此
// 该请求会被拒绝。用户相关请求头（X-User-Id/X-Username/X-User-Roles）
// 属于信息性字段，非必填，因为某些内部/系统调用方可能只携带租户信息。
func WithIdentity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tenantCode := r.Header.Get(TenantCodeHeader)
		if tenantCode == "" {
			http.Error(w, `{"error":"missing X-Tenant-Code header"}`, http.StatusBadRequest)
			return
		}

		id := identity.Identity{
			TenantCode: tenantCode,
			UserID:     r.Header.Get(UserIDHeader),
			Username:   r.Header.Get(UsernameHeader),
			Roles:      r.Header.Get(UserRolesHeader),
		}

		ctx := identity.NewContext(r.Context(), id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
