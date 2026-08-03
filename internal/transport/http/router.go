// Package http 提供基于 Gin 的 HTTP 服务装配和生命周期管理。
package http

import (
	"log/slog"

	"github.com/gin-gonic/gin"

	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/transport/http/chat"
	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/transport/http/health"
)

// NewRouter 创建并注册应用的 HTTP 路由。
func NewRouter(agentStreamer chat.AgentStreamer, logger *slog.Logger) *gin.Engine {
	router := gin.New()
	router.Use(gin.Recovery())
	router.GET("/healthz", health.Check)

	chatHandler := chat.NewHandler(agentStreamer, logger)
	aiAgent := router.Group("/ai-agent")
	aiAgent.POST("/chat", chatHandler.Chat)

	return router
}
