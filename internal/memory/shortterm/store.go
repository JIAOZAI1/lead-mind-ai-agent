// Package shortterm 在 Redis 中保存会话的原始对话历史，按租户隔离且
// 受 TTL 限制。长期持久的事实/偏好则保存在 internal/memory/longterm
// 中——本包的数据绝不会存活超过其 TTL。
package shortterm

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	pkgschema "github.com/JIAOZAI1/lead-mind-ai-agent/pkg/schema"
)

// Store 持久化保存一个会话逐轮次的消息历史。
type Store interface {
	// LoadHistory 按时间顺序返回 (tenant, session) 已保存的轮次；
	// 如果还没有任何记录，返回空切片——一个全新会话或 TTL 已过期的
	// 会话不属于错误情形。
	LoadHistory(ctx context.Context, tenantCode, sessionID string) ([]pkgschema.Message, error)

	// AppendTurns 追加新的轮次并刷新 TTL。
	AppendTurns(ctx context.Context, tenantCode, userID, sessionID string, turns []pkgschema.Message) error

	// ReplaceHistory 整体覆盖已存储的历史记录（供压缩流程使用，将
	// 不断增长的原始轮次原子性地替换为摘要+窗口化后的历史）并刷新
	// TTL。
	ReplaceHistory(ctx context.Context, tenantCode, sessionID string, turns []pkgschema.Message) error

	// Reset 彻底清空一个会话的历史记录（例如在会话被删除时使用）。
	Reset(ctx context.Context, tenantCode, sessionID string) error
}

// RedisStore 是一个基于 Redis 实现的 Store。根据 PROJECT.md §4.3/§6.2，
// key 统一以 tenant:{tenant_code}:... 为前缀。
type RedisStore struct {
	client *redis.Client
	ttl    time.Duration
}

// NewRedisStore 使用 client 构建一个 Store，ttl 会应用到每个会话的
// key 上（并在每次写入时刷新）。
func NewRedisStore(client *redis.Client, ttl time.Duration) *RedisStore {
	return &RedisStore{client: client, ttl: ttl}
}

func historyKey(tenantCode, sessionID string) string {
	return fmt.Sprintf("tenant:%s:session:%s:history", tenantCode, sessionID)
}

func metaKey(tenantCode, sessionID string) string {
	return fmt.Sprintf("tenant:%s:session:%s:meta", tenantCode, sessionID)
}

func (s *RedisStore) LoadHistory(ctx context.Context, tenantCode, sessionID string) ([]pkgschema.Message, error) {
	raw, err := s.client.Get(ctx, historyKey(tenantCode, sessionID)).Bytes()
	if err == redis.Nil {
		return []pkgschema.Message{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("load history for session %s: %w", sessionID, err)
	}

	var msgs []pkgschema.Message
	if err := json.Unmarshal(raw, &msgs); err != nil {
		return nil, fmt.Errorf("decode history for session %s: %w", sessionID, err)
	}
	return msgs, nil
}

func (s *RedisStore) AppendTurns(ctx context.Context, tenantCode, userID, sessionID string, turns []pkgschema.Message) error {
	existing, err := s.LoadHistory(ctx, tenantCode, sessionID)
	if err != nil {
		return err
	}
	merged := append(existing, turns...)
	return s.writeHistory(ctx, tenantCode, userID, sessionID, merged)
}

func (s *RedisStore) ReplaceHistory(ctx context.Context, tenantCode, sessionID string, turns []pkgschema.Message) error {
	return s.writeHistory(ctx, tenantCode, "", sessionID, turns)
}

func (s *RedisStore) writeHistory(ctx context.Context, tenantCode, userID, sessionID string, msgs []pkgschema.Message) error {
	raw, err := json.Marshal(msgs)
	if err != nil {
		return fmt.Errorf("encode history for session %s: %w", sessionID, err)
	}

	pipe := s.client.TxPipeline()
	pipe.Set(ctx, historyKey(tenantCode, sessionID), raw, s.ttl)
	metaFields := map[string]any{
		"last_active_at": time.Now().UTC().Format(time.RFC3339),
		"turn_count":     len(msgs),
	}
	if userID != "" {
		metaFields["user_id"] = userID
	}
	pipe.HSet(ctx, metaKey(tenantCode, sessionID), metaFields)
	pipe.Expire(ctx, metaKey(tenantCode, sessionID), s.ttl)

	if _, err := pipe.Exec(ctx); err != nil {
		return fmt.Errorf("write history for session %s: %w", sessionID, err)
	}
	return nil
}

func (s *RedisStore) Reset(ctx context.Context, tenantCode, sessionID string) error {
	if err := s.client.Del(ctx, historyKey(tenantCode, sessionID), metaKey(tenantCode, sessionID)).Err(); err != nil {
		return fmt.Errorf("reset history for session %s: %w", sessionID, err)
	}
	return nil
}
