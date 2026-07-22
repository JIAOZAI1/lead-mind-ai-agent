// Package shortterm holds a session's raw conversation history in Redis,
// TTL-bounded and tenant-scoped. Long-term durable facts/preferences live
// in internal/memory/longterm instead — this package never persists past
// its TTL.
package shortterm

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	pkgschema "github.com/JIAOZAI1/lead-mind-ai-agent/pkg/schema"
)

// Store persists a session's turn-by-turn message history.
type Store interface {
	// LoadHistory returns the persisted turns for (tenant, session) in
	// chronological order, or an empty slice if none exist yet — a
	// fresh or TTL-expired session is not an error case.
	LoadHistory(ctx context.Context, tenantCode, sessionID string) ([]pkgschema.Message, error)

	// AppendTurns appends new turns and refreshes the TTL.
	AppendTurns(ctx context.Context, tenantCode, userID, sessionID string, turns []pkgschema.Message) error

	// ReplaceHistory overwrites the stored history wholesale (used by
	// the compaction path to atomically swap in a summarized+windowed
	// history in place of ever-growing raw turns) and refreshes the TTL.
	ReplaceHistory(ctx context.Context, tenantCode, sessionID string, turns []pkgschema.Message) error

	// Reset clears a session's history entirely (e.g. on session
	// deletion).
	Reset(ctx context.Context, tenantCode, sessionID string) error
}

// RedisStore is a Store backed by Redis. Keys are prefixed
// tenant:{tenant_code}:... per PROJECT.md §4.3/§6.2.
type RedisStore struct {
	client *redis.Client
	ttl    time.Duration
}

// NewRedisStore builds a Store using client, with ttl applied (and
// refreshed on every write) to each session's keys.
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
