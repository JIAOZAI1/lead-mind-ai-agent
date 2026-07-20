package middleware

import (
	"net/http"

	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/tenant"
)

// TenantHeader is the request header carrying the routing tenant ID.
// See PROJECT.md §4: tenant_id is read at the gateway and used for
// routing, not as an auth credential.
const TenantHeader = "tenant_id"

// WithTenant reads TenantHeader off the request and attaches it to the
// request context. It does not authenticate the caller — it only
// establishes which tenant's resources the request should be routed to.
// Requests without the header are rejected, since there is no default
// tenant to route to.
func WithTenant(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tenantID := r.Header.Get(TenantHeader)
		if tenantID == "" {
			http.Error(w, `{"error":"missing tenant_id header"}`, http.StatusBadRequest)
			return
		}

		ctx := tenant.NewContext(r.Context(), tenantID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
