// Package gateway 负责组装 HTTP 路由与中间件链。根据 PROJECT.md §3，
// 网关层承担租户路由（通过 X-Tenant-Code 请求头，以及用于调用方身份的
// X-User-Id/X-Username/X-User-Roles —— 均由上游注入）与 SSE 流式响应；
// 认证本身和限流暂不在本层范围内。
package gateway

import (
	"net/http"

	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/browser"
	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/gateway/handler"
	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/gateway/middleware"
)

// NewRouter 构建网关的顶层 HTTP handler。deps 提供了每次请求构建
// ReAct agent 所需的共享 ChatModel/工具集；browserDevices 提供浏览器执行端
// （Chrome 扩展）的配对与 WebSocket 接入端点，为 nil 时不注册这些路由。
func NewRouter(deps handler.AgentDeps, browserDevices *browser.Handlers) http.Handler {
	mux := http.NewServeMux()

	mux.Handle("GET /healthz", middleware.Recover(middleware.Logging(http.HandlerFunc(handler.Health))))

	// 所有对外暴露的业务路由都统一挂在 /ai-agent 前缀下，与 k8s
	// Service/Deployment 的名称保持一致（参见 deployments/k8s/）。
	// /healthz 被特意排除在外——它是集群内部的探活端点，不是对外 API。
	tenantScoped := http.NewServeMux()
	tenantScoped.HandleFunc("POST /ai-agent/v1/chat", deps.Chat)
	tenantScoped.HandleFunc("GET /ai-agent/v1/chat/stream", deps.ChatStream)
	tenantScoped.HandleFunc("GET /ai-agent/v1/sessions", deps.ListSessions)
	tenantScoped.HandleFunc("GET /ai-agent/v1/sessions/{id}/messages", deps.GetSessionMessages)
	tenantScoped.HandleFunc("PATCH /ai-agent/v1/sessions/{id}", deps.PatchSession)
	tenantScoped.HandleFunc("DELETE /ai-agent/v1/sessions/{id}", deps.DeleteSession)

	if browserDevices != nil {
		// 浏览器执行端（Chrome 扩展）的对接端点，契约见
		// internal/browser/protocol.go 与插件仓库 DESIGN.md §3。
		//
		// /device/pair 与其他端点的身份要求不同：插件在配对完成前没有任何
		// 凭证，但它仍需经过 WithIdentity 拿到 X-Tenant-Code（由上游网关按
		// 访问的域名/路径注入），租户身份不能由插件自己声明。
		tenantScoped.HandleFunc("POST /ai-agent/v1/device/pair", browserDevices.Pair)
		tenantScoped.HandleFunc("GET /ai-agent/v1/device/connect", browserDevices.Connect)
		tenantScoped.HandleFunc("POST /ai-agent/v1/device/pairing-code", browserDevices.IssuePairingCode)
		tenantScoped.HandleFunc("GET /ai-agent/v1/devices", browserDevices.ListDevices)
		tenantScoped.HandleFunc("DELETE /ai-agent/v1/devices/{id}", browserDevices.RevokeDevice)
	}
	// Recover 和 Logging 从外层包裹 WithIdentity——也就是排在身份解析
	// 之前——这样即使请求被 WithIdentity 拒绝（缺少 X-Tenant-Code），
	// 依然会产生一条日志；而下游任何位置（包括 WithIdentity 内部本身）
	// 发生的 panic 也依然能被捕获并记录。两者都是直接从请求头读取身份
	// 信息而非从 context 读取，所以这个包裹顺序只是为了保证
	// panic/错误都能被覆盖到，与身份信息的可见性无关。
	mux.Handle("/ai-agent/", middleware.Recover(middleware.Logging(middleware.WithIdentity(tenantScoped))))

	return mux
}
