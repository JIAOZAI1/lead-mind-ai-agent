package device

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisCodeStore 是基于 Redis 的 CodeStore。
//
// 按 PROJECT.md §4.3/§6.2，key 一律以 tenant:{tenant_code}: 为前缀——Redis
// 是跨租户共享的基础设施，缺了前缀就等于让 A 租户的配对码可能被 B 租户兑换。
type RedisCodeStore struct {
	client *redis.Client
}

// NewRedisCodeStore 构建一个 CodeStore。
func NewRedisCodeStore(client *redis.Client) *RedisCodeStore {
	return &RedisCodeStore{client: client}
}

func codeKey(tenantCode, code string) string {
	return fmt.Sprintf("tenant:%s:device:paircode:%s", tenantCode, code)
}

type storedCode struct {
	TenantCode string `json:"tenant_code"`
	UserID     string `json:"user_id"`
	ExpiresAt  string `json:"expires_at"`
}

func (s *RedisCodeStore) Issue(ctx context.Context, code PairingCode, ttl time.Duration) error {
	payload, err := json.Marshal(storedCode{
		TenantCode: code.TenantCode,
		UserID:     code.UserID,
		// 存 UTC：跨服务/跨时区比较时间只允许比 UTC 或带偏移的完整值
		// （PROJECT.md §6.6）。
		ExpiresAt: code.ExpiresAt.UTC().Format(time.RFC3339),
	})
	if err != nil {
		return fmt.Errorf("marshal pairing code for tenant %s: %w", code.TenantCode, err)
	}

	// SetNX 而非 Set：万一撞码（6 位共 100 万种组合，生日碰撞并非不可能），
	// 宁可让本次签发失败让调用方重试，也不能覆盖掉另一个用户正在等待兑换的码
	// ——覆盖会让先来的用户配对到后来者的账号上。
	ok, err := s.client.SetNX(ctx, codeKey(code.TenantCode, code.Code), payload, ttl).Result()
	if err != nil {
		return fmt.Errorf("store pairing code for tenant %s: %w", code.TenantCode, err)
	}
	if !ok {
		return fmt.Errorf("pairing code collision for tenant %s", code.TenantCode)
	}
	return nil
}

func (s *RedisCodeStore) Redeem(ctx context.Context, tenantCode, code string) (PairingCode, bool, error) {
	// GetDel 是单条 Redis 命令，天然原子——保证并发兑换只有一个能成功。
	raw, err := s.client.GetDel(ctx, codeKey(tenantCode, code)).Bytes()
	if err == redis.Nil {
		return PairingCode{}, false, nil
	}
	if err != nil {
		return PairingCode{}, false, fmt.Errorf("redeem pairing code for tenant %s: %w", tenantCode, err)
	}

	var stored storedCode
	if err := json.Unmarshal(raw, &stored); err != nil {
		return PairingCode{}, false, fmt.Errorf("unmarshal pairing code for tenant %s: %w", tenantCode, err)
	}

	expiresAt, err := time.Parse(time.RFC3339, stored.ExpiresAt)
	if err != nil {
		return PairingCode{}, false, fmt.Errorf("parse pairing code expiry for tenant %s: %w", tenantCode, err)
	}

	// Redis TTL 已经保证过期码会消失，这里再判一次是为了防御 TTL 被误设或
	// 写入时钟与读取时钟不一致的情况——配对码是能换长期凭证的秘密，多一道
	// 显式检查的成本可以忽略。
	if time.Now().After(expiresAt) {
		return PairingCode{}, false, nil
	}

	return PairingCode{
		Code:       code,
		TenantCode: stored.TenantCode,
		UserID:     stored.UserID,
		ExpiresAt:  expiresAt,
	}, true, nil
}
