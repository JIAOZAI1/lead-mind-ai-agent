package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/redis/go-redis/v9"

	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/gateway"
	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/gateway/handler"
	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/memory"
	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/memory/longterm"
	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/memory/shortterm"
	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/memory/transcript"
	modelcfg "github.com/JIAOZAI1/lead-mind-ai-agent/internal/model"
	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/model/provider"
	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/session"
	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/tenantdb"
	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/tools/builtin"
)

// envDurationSeconds 从环境变量 name 中读取一个整数（单位：秒），转换为
// time.Duration；如果该环境变量未设置或不是合法整数，则返回 fallback。
func envDurationSeconds(name string, fallback time.Duration) time.Duration {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback
	}
	seconds, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return time.Duration(seconds) * time.Second
}

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

	redisCfg, err := shortterm.RedisConfigFromEnv()
	if err != nil {
		slog.Error("redis config error", "error", err)
		os.Exit(1)
	}
	redisClient := redis.NewClient(&redis.Options{
		Addr:     redisCfg.Addr,
		Username: redisCfg.Username,
		Password: redisCfg.Password,
		DB:       redisCfg.DB,
	})

	shortTermTTL := envDurationSeconds("SHORTTERM_SESSION_TTL_SECONDS", 6*time.Hour)
	shortTermStore := shortterm.NewRedisStore(redisClient, shortTermTTL)

	ssoBaseURL := os.Getenv("SSO_SERVICE_BASE_URL")
	if ssoBaseURL == "" {
		ssoBaseURL = "http://sso-service.default.svc.cluster.local"
	}
	ssoClient := tenantdb.NewSSOClient(ssoBaseURL, os.Getenv("SSO_INTERNAL_TOKEN"))
	dbInfoCacheTTL := envDurationSeconds("TENANTDB_INFO_CACHE_TTL_SECONDS", 10*time.Minute)
	idleEvictAfter := envDurationSeconds("TENANTDB_IDLE_EVICT_SECONDS", 30*time.Minute)
	registry := tenantdb.NewRegistry(ssoClient, dbInfoCacheTTL, idleEvictAfter)
	defer registry.Close()

	// 三个 store 共用同一个 registry，但各自对应不同的表/键空间
	// （会话元数据、长期记忆事实、完整对话记录）——具体职责边界与
	// shortterm 的 TTL 限时 Redis 历史记录有何不同，参见各自包的注释说明。
	sessionStore := session.NewMySQLStore(registry)
	longTermStore := longterm.NewMySQLStore(registry)
	transcriptStore := transcript.NewMySQLStore(registry)

	compactionCfg := memory.DefaultCompactionConfig(chatModel)

	deps := handler.AgentDeps{
		ChatModel:  chatModel,
		Tools:      []tool.BaseTool{timeTool},
		Sessions:   sessionStore,
		ShortTerm:  shortTermStore,
		LongTerm:   longTermStore,
		Transcript: transcriptStore,
		Compaction: compactionCfg,
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
