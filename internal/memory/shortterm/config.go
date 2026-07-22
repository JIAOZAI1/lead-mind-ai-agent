package shortterm

import (
	"fmt"
	"os"
	"strconv"
)

// RedisConfig holds the connection settings for the short-term memory
// Redis instance.
type RedisConfig struct {
	// Addr is host:port of the Redis server.
	Addr string
	// Username authenticates against Addr (Redis 6+ ACL username; empty
	// is valid for legacy requirepass-only setups).
	Username string
	// Password authenticates against Addr. Sourced from a Secret, never
	// a ConfigMap (see deployments/k8s/secret.yaml).
	Password string
	// DB selects the logical Redis database index (0-15 by default on
	// stock Redis).
	DB int
}

// RedisConfigFromEnv reads Redis connection settings from environment
// variables: REDIS_ADDR (required), REDIS_USERNAME, REDIS_PASSWORD,
// REDIS_DB (defaults to 0 if unset or invalid).
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
