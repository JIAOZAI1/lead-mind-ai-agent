// Package gateway wires HTTP routing and middleware. Per PROJECT.md §3,
// the gateway layer owns tenant routing (via the X-Tenant-Code header,
// alongside X-User-Id/X-Username/X-User-Roles for caller identity — all
// injected upstream) and SSE streaming; auth itself and rate limiting are
// out of scope for now.
package gateway

import (
	"net/http"

	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/gateway/handler"
	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/gateway/middleware"
)

// NewRouter builds the top-level HTTP handler for the gateway. deps
// supplies the shared ChatModel/tool set used to construct a ReAct agent
// per request.
func NewRouter(deps handler.AgentDeps) http.Handler {
	mux := http.NewServeMux()

	mux.Handle("GET /healthz", middleware.Recover(middleware.Logging(http.HandlerFunc(handler.Health))))

	// All externally-exposed business routes are namespaced under
	// /ai-agent, matching the k8s Service/Deployment name (see
	// deployments/k8s/). /healthz is intentionally excluded — it's a
	// cluster-internal probe endpoint, not a public API route.
	tenantScoped := http.NewServeMux()
	tenantScoped.HandleFunc("POST /ai-agent/v1/chat", deps.Chat)
	tenantScoped.HandleFunc("GET /ai-agent/v1/chat/stream", deps.ChatStream)
	tenantScoped.HandleFunc("GET /ai-agent/v1/sessions", deps.ListSessions)
	tenantScoped.HandleFunc("GET /ai-agent/v1/sessions/{id}/messages", deps.GetSessionMessages)
	tenantScoped.HandleFunc("PATCH /ai-agent/v1/sessions/{id}", deps.PatchSession)
	tenantScoped.HandleFunc("DELETE /ai-agent/v1/sessions/{id}", deps.DeleteSession)
	// Recover and Logging wrap WithIdentity from the outside — ahead of
	// identity resolution — so that a request rejected by WithIdentity
	// (missing X-Tenant-Code) still produces a log line, and a panic
	// anywhere downstream (including inside WithIdentity itself) is
	// still caught and logged. Both read identity straight off request
	// headers rather than context, so this ordering is for panic/error
	// coverage, not identity visibility.
	mux.Handle("/ai-agent/", middleware.Recover(middleware.Logging(middleware.WithIdentity(tenantScoped))))

	return mux
}
