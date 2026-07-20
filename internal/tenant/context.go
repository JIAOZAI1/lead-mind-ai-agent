// Package tenant provides the tenant identity carried through a request's
// context. Per PROJECT.md §4, tenant_id is used for gateway-level routing
// to a tenant's dedicated database, not as a row-level filter.
package tenant

import "context"

type contextKey struct{}

// FromContext returns the tenant ID carried on ctx and whether one was set.
func FromContext(ctx context.Context) (string, bool) {
	id, ok := ctx.Value(contextKey{}).(string)
	return id, ok && id != ""
}

// NewContext returns a copy of ctx carrying the given tenant ID.
func NewContext(ctx context.Context, tenantID string) context.Context {
	return context.WithValue(ctx, contextKey{}, tenantID)
}
