package middleware

import (
	"net/http"

	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/identity"
)

// Headers carrying caller identity, injected upstream (API
// gateway/auth proxy) ahead of this service. This service trusts them as
// given — it does not authenticate the caller itself.
const (
	TenantCodeHeader = "X-Tenant-Code"
	UserIDHeader     = "X-User-Id"
	UsernameHeader   = "X-Username"
	// UserRolesHeader  = "X-User-Roles"
)

// WithIdentity reads the identity headers off the request and attaches
// them to the request context. X-Tenant-Code is required — without it
// there is no tenant to route to, so the request is rejected. The user
// headers (X-User-Id/X-Username/X-User-Roles) are informational and not
// required, since some internal/system callers may carry only a tenant.
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
			// Roles:      r.Header.Get(UserRolesHeader),
		}

		ctx := identity.NewContext(r.Context(), id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
