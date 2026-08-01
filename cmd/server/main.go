// Command server 启动 lead-mind-ai-agent HTTP 服务。
package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/agent"
	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/config"
	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/llm"
	httptransport "github.com/JIAOZAI1/lead-mind-ai-agent/internal/transport/http"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(logger); err != nil {
		logger.Error("HTTP 服务退出", "error", err)
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("加载应用配置: %w", err)
	}

	chatModel, err := llm.NewOpenAIChatModel(context.Background(), cfg.OpenAI)
	if err != nil {
		return err
	}
	agentService, err := agent.NewService(context.Background(), chatModel, cfg.Agent)
	if err != nil {
		return err
	}

	gin.SetMode(cfg.HTTP.Mode)
	server := httptransport.NewServer(cfg.HTTP, agentService, logger)
	logger.Info(
		"HTTP 服务开始监听",
		"app", cfg.App.Name,
		"environment", cfg.App.Environment,
		"address", cfg.HTTP.Address(),
	)

	return serveUntilSignal(server, cfg.HTTP.ShutdownTimeout, logger)
}

func serveUntilSignal(server *httptransport.Server, shutdownTimeout time.Duration, logger *slog.Logger) error {
	serverErrors := make(chan error, 1)
	go listen(server, serverErrors)

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)

	select {
	case err := <-serverErrors:
		return err
	case received := <-signals:
		logger.Info("收到退出信号，开始优雅关闭", "signal", received.String())
		return shutdown(server, shutdownTimeout)
	}
}

func listen(server *httptransport.Server, serverErrors chan<- error) {
	serverErrors <- server.ListenAndServe()
}

func shutdown(server *httptransport.Server, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	return server.Shutdown(ctx)
}
