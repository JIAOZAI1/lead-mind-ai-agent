package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/redis/go-redis/v9"

	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/browser"
	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/browser/device"
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
	browsertools "github.com/JIAOZAI1/lead-mind-ai-agent/internal/tools/browser"
	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/tools/builtin"
)

// envStrings 读取一个逗号分隔的环境变量并切分成非空字符串切片。
func envStrings(name string) []string {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if trimmed := strings.TrimSpace(p); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

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

	// 浏览器执行端（Chrome 扩展 lead-mind-ai-plugin）：设备注册表 + WS Hub +
	// 把浏览器指令暴露成 Agent 工具。契约见 internal/browser/protocol.go。
	deviceStore := device.NewMySQLStore(registry)
	codeStore := device.NewRedisCodeStore(redisClient)

	browserHub := browser.NewHub(browser.HubConfig{
		Authenticator: browser.NewStoreAuthenticator(deviceStore),
		Devices:       deviceStore,
		Logger:        slog.Default(),
	})

	browserHandlers := &browser.Handlers{
		Hub:     browserHub,
		Codes:   codeStore,
		Devices: deviceStore,
		Logger:  slog.Default(),
		// 生产环境必须配置：留空会放行任意 Origin 发起的 WS 连接。扩展的
		// Origin 形如 chrome-extension://<extension-id>。
		AllowedOrigins: envStrings("BROWSER_ALLOWED_ORIGINS"),
	}
	if len(browserHandlers.AllowedOrigins) == 0 {
		slog.Warn("BROWSER_ALLOWED_ORIGINS 未配置，将不校验 WebSocket 的 Origin——仅可用于本地开发")
	}

	browserToolSet, err := browsertools.NewTools(browser.NewDispatcher(browserHub, slog.Default()))
	if err != nil {
		slog.Error("failed to init browser tools", "error", err)
		os.Exit(1)
	}

	// 浏览器工具是进程级共享的，但这不会串租户：具体发给哪台设备完全由
	// Dispatcher 在调用时从 context 的 identity 解析（见 dispatch.go），
	// 工具本身不持有任何租户状态。
	agentTools := append([]tool.BaseTool{timeTool}, browserToolSet...)

	deps := handler.AgentDeps{
		ChatModel:  chatModel,
		Tools:      agentTools,
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
		Handler: gateway.NewRouter(deps, browserHandlers),
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
