// Package http 提供基于 Gin 的 HTTP 接口。
package http

import (
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"
)

// HealthResponse 表示健康检查接口的响应。
type HealthResponse struct {
	Status string `json:"status"`
}

// NewRouter 创建并注册应用的 HTTP 路由。
func NewRouter(agentStreamer AgentStreamer, logger *slog.Logger) *gin.Engine {
	router := gin.New()
	router.Use(gin.Recovery())
	router.GET("/healthz", Health)

	chatHandler := NewChatHandler(agentStreamer, logger)
	aiAgent := router.Group("/ai-agent")
	aiAgent.POST("/chat", chatHandler.Chat)

	return router
}

// Health 返回当前进程的存活状态。
func Health(c *gin.Context) {
	c.JSON(http.StatusOK, HealthResponse{Status: "ok"})
}
