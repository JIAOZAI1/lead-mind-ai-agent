package http

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/config"
)

// Server 封装 HTTP 服务的启动和关闭生命周期。
type Server struct {
	server *http.Server // server 是标准库 HTTP 服务实例。
}

// NewServer 根据配置创建 HTTP 服务，但不会立即开始监听端口。
func NewServer(cfg config.HTTPConfig, agentStreamer AgentStreamer, logger *slog.Logger) *Server {
	server := &http.Server{
		Addr:              cfg.Address(),
		Handler:           NewRouter(agentStreamer, logger),
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
	}

	return &Server{server: server}
}

// ListenAndServe 阻塞运行 HTTP 服务，正常关闭时返回 nil。
func (s *Server) ListenAndServe() error {
	if err := s.server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("运行 HTTP 服务: %w", err)
	}

	return nil
}

// Shutdown 在给定上下文的期限内优雅关闭 HTTP 服务。
func (s *Server) Shutdown(ctx context.Context) error {
	if err := s.server.Shutdown(ctx); err != nil {
		return fmt.Errorf("关闭 HTTP 服务: %w", err)
	}

	return nil
}
