package shortterm

import (
	"fmt"
	"os"
	"strconv"
)

// RedisConfig 保存短期记忆所用 Redis 实例的连接配置。
type RedisConfig struct {
	// Addr 是 Redis 服务器的 host:port。
	Addr string
	// Username 用于对 Addr 鉴权（Redis 6+ 的 ACL 用户名；对于仅使用
	// 传统 requirepass 的部署，留空也是合法的）。
	Username string
	// Password 用于对 Addr 鉴权，来源于 Secret，绝不来自 ConfigMap
	// （参见 deployments/k8s/secret.yaml）。
	Password string
	// DB 选择 Redis 的逻辑数据库编号（原生 Redis 默认范围是 0-15）。
	DB int
}

// RedisConfigFromEnv 从环境变量读取 Redis 连接配置：
// REDIS_ADDR（必填）、REDIS_USERNAME、REDIS_PASSWORD、
// REDIS_DB（未设置或无效时默认为 0）。
func RedisConfigFromEnv() (RedisConfig, error) {
	cfg := RedisConfig{
		Addr:     os.Getenv("REDIS_ADDR"),
		Username: os.Getenv("REDIS_USERNAME"),
		Password: os.Getenv("REDIS_PASSWORD"),
	}

	if cfg.Addr == "" {
		return RedisConfig{}, fmt.Errorf("REDIS_ADDR is required")
	}

	if raw := os.Getenv("REDIS_DB"); raw != "" {
		db, err := strconv.Atoi(raw)
		if err != nil {
			return RedisConfig{}, fmt.Errorf("REDIS_DB must be an integer: %w", err)
		}
		cfg.DB = db
	}

	return cfg, nil
}
