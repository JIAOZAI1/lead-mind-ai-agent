package shortterm

import "testing"

func TestRedisConfigFromEnv_RequiresAddr(t *testing.T) {
	t.Setenv("REDIS_ADDR", "")
	if _, err := RedisConfigFromEnv(); err == nil {
		t.Fatal("expected error when REDIS_ADDR is unset")
	}
}

func TestRedisConfigFromEnv_ReadsAllFields(t *testing.T) {
	t.Setenv("REDIS_ADDR", "redis.internal:6379")
	t.Setenv("REDIS_USERNAME", "app")
	t.Setenv("REDIS_PASSWORD", "secret")
	t.Setenv("REDIS_DB", "3")

	cfg, err := RedisConfigFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Addr != "redis.internal:6379" || cfg.Username != "app" || cfg.Password != "secret" || cfg.DB != 3 {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestRedisConfigFromEnv_DBDefaultsToZero(t *testing.T) {
	t.Setenv("REDIS_ADDR", "redis.internal:6379")
	t.Setenv("REDIS_DB", "")

	cfg, err := RedisConfigFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.DB != 0 {
		t.Fatalf("expected DB to default to 0, got %d", cfg.DB)
	}
}

func TestRedisConfigFromEnv_InvalidDBErrors(t *testing.T) {
	t.Setenv("REDIS_ADDR", "redis.internal:6379")
	t.Setenv("REDIS_DB", "not-a-number")

	if _, err := RedisConfigFromEnv(); err == nil {
		t.Fatal("expected error for non-integer REDIS_DB")
	}
}
