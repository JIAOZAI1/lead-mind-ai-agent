// Package identity provides the caller identity carried through a
// request's context: which tenant the request belongs to and which user
// made it. These are injected upstream (API gateway / auth proxy) as
// request headers — this package only reads and threads them through,
// it does not authenticate anyone. See PROJECT.md §4: TenantCode is used
// for gateway-level routing to a tenant's dedicated database, not as a
// row-level filter.
package identity

import "context"

// Identity is the caller identity resolved from request headers.
type Identity struct {
	// TenantCode identifies which tenant's resources to route to.
	TenantCode string
	// UserID, Username, Roles describe the calling user within that
	// tenant. Roles is whatever the upstream auth layer put in
	// X-User-Roles (raw, comma-separated as received — not parsed into
	// a fixed enum here since role semantics belong to the auth layer).
	UserID   string
	Username string
	Roles    string
}

type contextKey struct{}

// FromContext returns the Identity carried on ctx and whether one was set.
func FromContext(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(contextKey{}).(Identity)
	return id, ok
}

// NewContext returns a copy of ctx carrying id.
func NewContext(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, contextKey{}, id)
}
