package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cloudwego/eino/components/tool"

	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/gateway"
	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/gateway/handler"
	modelcfg "github.com/JIAOZAI1/lead-mind-ai-agent/internal/model"
	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/model/provider"
	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/tools/builtin"
)

func main() {
	ctx := context.Background()

	cfg, err := modelcfg.ConfigFromEnv()
	if err != nil {
		slog.Error("model config error", "error", err)
		os.Exit(1)
	}

	chatModel, err := provider.NewOpenAICompatible(ctx, cfg)
	if err != nil {
		slog.Error("failed to init chat model", "error", err)
		os.Exit(1)
	}

	timeTool, err := builtin.NewCurrentTimeTool()
	if err != nil {
		slog.Error("failed to init builtin tools", "error", err)
		os.Exit(1)
	}

	deps := handler.AgentDeps{
		ChatModel: chatModel,
		Tools:     []tool.BaseTool{timeTool},
	}

	addr := os.Getenv("SERVER_ADDR")
	if addr == "" {
		addr = ":8080"
	}

	srv := &http.Server{
		Addr:    addr,
		Handler: gateway.NewRouter(deps),
	}

	runCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go func() {
		slog.Info("gateway listening", "addr", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	<-runCtx.Done()
	slog.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		slog.Error("graceful shutdown failed", "error", err)
		os.Exit(1)
	}
}
