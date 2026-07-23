// Package identity 提供贯穿请求 context 的调用方身份信息：请求属于哪个
// 租户、由哪个用户发起。这些信息由上游（API 网关 / 认证代理）以请求头的
// 形式注入——本包只负责读取并透传，不做任何身份认证。参见 PROJECT.md
// §4：TenantCode 用于网关层路由到该租户专属的数据库，而不是作为行级
// 过滤条件使用。
package identity

import "context"

// Identity 是从请求头解析出的调用方身份信息。
type Identity struct {
	// TenantCode 标识该请求的资源应路由到哪个租户。
	TenantCode string
	// UserID、Username、Roles 描述该租户下的具体调用用户。Roles 是上游
	// 认证层写入 X-User-Roles 的原始内容（逗号分隔，按原样保留——不在
	// 此处解析成固定枚举，因为角色语义应由认证层定义）。
	UserID   string
	Username string
	Roles    string
}

type contextKey struct{}

// FromContext 返回 ctx 上携带的 Identity，以及是否存在该值。
func FromContext(ctx context.Context) (Identity, bool) {
	id, ok := ctx.Value(contextKey{}).(Identity)
	return id, ok
}

// NewContext 返回携带 id 的 ctx 副本。
func NewContext(ctx context.Context, id Identity) context.Context {
	return context.WithValue(ctx, contextKey{}, id)
}
