// Package longterm 在每个租户的 MySQL 数据库中持久化保存关于用户的、
// 跨会话的持久事实——偏好设置与会话摘要。它绝不存储完整的原始对话记录
// （那是 internal/memory/shortterm 的职责，受 TTL 限制）；本包也明确
// 不是基于向量检索能力的替代品，PROJECT.md 将向量检索（RAG）作为
// 后续待验证需求，暂缓引入。
package longterm

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/tenantdb"
)

// FactKind 区分本 store 保存的两种持久事实类型。
type FactKind string

const (
	// FactKindPreference 是一条具名的用户偏好（fact_key = 偏好名称，
	// 例如 "timezone"）。
	FactKindPreference FactKind = "preference"
	// FactKindSessionSummary 是在压缩/会话结束时提炼出的会话级摘要
	// （fact_key = 来源会话 ID，这样 UNIQUE(user_id, kind, fact_key)
	// 约束天然就实现了"每个会话一条摘要"的 upsert 语义）。
	FactKindSessionSummary FactKind = "session_summary"
)

// Fact 是一条关于用户的持久化事实记录。
type Fact struct {
	ID              int64
	UserID          string
	Kind            FactKind
	Key             string
	Value           string
	SourceSessionID string
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// Store 在租户的 MySQL 数据库中管理持久化事实。每个方法都显式接收
// tenantCode，且仅用它来选择路由到哪个 *sql.DB（通过
// tenantdb.Registry）——隔离是物理层面的（每个租户独立数据库），
// 因此没有任何方法会把 tenant_code 作为 WHERE 条件列来过滤行。
type Store interface {
	// UpsertFact 写入或更新一条事实。对于 Key 非空的 FactKindPreference，
	// 按 (user_id, kind, key) 做 upsert；对于 FactKindSessionSummary，
	// Key 必须是来源会话 ID，这样同一个唯一约束就能实现"每会话一行"
	// 的语义。
	UpsertFact(ctx context.Context, tenantCode string, fact Fact) error

	// ListFacts 返回某个用户的持久化事实，可选按 kind 过滤（传 ""
	// 表示不限类型）。
	ListFacts(ctx context.Context, tenantCode, userID string, kind FactKind) ([]Fact, error)
}

// MySQLStore 是一个基于每个租户自有 MySQL 数据库实现的 Store，根据
// PROJECT.md §6.2，一律通过 registry 获取连接。
type MySQLStore struct {
	registry *tenantdb.Registry
}

// NewMySQLStore 构建一个通过 registry 解析连接的 Store。
func NewMySQLStore(registry *tenantdb.Registry) *MySQLStore {
	return &MySQLStore{registry: registry}
}

func (s *MySQLStore) db(ctx context.Context, tenantCode string) (*sql.DB, error) {
	db, err := s.registry.Get(ctx, tenantCode)
	if err != nil {
		return nil, fmt.Errorf("resolve tenant db: %w", err)
	}
	return db, nil
}

func (s *MySQLStore) UpsertFact(ctx context.Context, tenantCode string, fact Fact) error {
	db, err := s.db(ctx, tenantCode)
	if err != nil {
		return err
	}

	_, err = db.ExecContext(ctx, `
		INSERT INTO agent_memory_facts (user_id, kind, fact_key, fact_value, source_session_id)
		VALUES (?, ?, ?, ?, ?)
		ON DUPLICATE KEY UPDATE fact_value = VALUES(fact_value), source_session_id = VALUES(source_session_id)`,
		fact.UserID, fact.Kind, fact.Key, fact.Value, fact.SourceSessionID)
	if err != nil {
		return fmt.Errorf("upsert fact (user=%s, kind=%s, key=%s): %w", fact.UserID, fact.Kind, fact.Key, err)
	}
	return nil
}

func (s *MySQLStore) ListFacts(ctx context.Context, tenantCode, userID string, kind FactKind) ([]Fact, error) {
	db, err := s.db(ctx, tenantCode)
	if err != nil {
		return nil, err
	}

	query := `
		SELECT id, user_id, kind, fact_key, fact_value, source_session_id, created_at, updated_at
		FROM agent_memory_facts WHERE user_id = ?`
	args := []any{userID}
	if kind != "" {
		query += ` AND kind = ?`
		args = append(args, kind)
	}
	query += ` ORDER BY updated_at DESC`

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list facts for user %s: %w", userID, err)
	}
	defer rows.Close()

	var out []Fact
	for rows.Next() {
		var f Fact
		if err := rows.Scan(&f.ID, &f.UserID, &f.Kind, &f.Key, &f.Value, &f.SourceSessionID, &f.CreatedAt, &f.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan fact row: %w", err)
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate fact rows: %w", err)
	}
	return out, nil
}
