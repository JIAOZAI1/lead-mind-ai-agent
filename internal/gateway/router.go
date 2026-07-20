// Package gateway wires HTTP routing and middleware. Per PROJECT.md §3,
// the gateway layer owns tenant routing (via the tenant_id header) and
// SSE streaming; auth and rate limiting are out of scope for now.
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

	mux.Handle("GET /healthz", middleware.Logging(http.HandlerFunc(handler.Health)))

	tenantScoped := http.NewServeMux()
	tenantScoped.HandleFunc("POST /v1/chat", deps.Chat)
	tenantScoped.HandleFunc("GET /v1/chat/stream", deps.ChatStream)
	// Logging wraps WithTenant's *output*, i.e. runs inside tenant
	// resolution, so it observes the request after tenant_id has been
	// attached to context and can log it.
	mux.Handle("/v1/", middleware.WithTenant(middleware.Logging(tenantScoped)))

	return mux
}
