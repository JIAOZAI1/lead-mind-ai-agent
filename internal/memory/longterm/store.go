// Package longterm persists durable, cross-session facts about a user —
// preferences and session summaries — in each tenant's MySQL database.
// It never stores full raw conversation transcripts (that's
// internal/memory/shortterm's job, TTL-bounded); this package is
// explicitly not a substitute for vector-based recall, which
// PROJECT.md defers until RAG is a validated need.
package longterm

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/tenantdb"
)

// FactKind distinguishes the two durable fact shapes this store holds.
type FactKind string

const (
	// FactKindPreference is a named user preference (fact_key = preference
	// name, e.g. "timezone").
	FactKindPreference FactKind = "preference"
	// FactKindSessionSummary is a session-level summary distilled at
	// compaction/session-end time (fact_key = source session ID, so the
	// UNIQUE(user_id, kind, fact_key) constraint gives "one summary per
	// session" upsert semantics for free).
	FactKindSessionSummary FactKind = "session_summary"
)

// Fact is one durable, persisted fact about a user.
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

// Store manages durable facts in a tenant's MySQL database. Every method
// takes tenantCode explicitly and uses it only to pick which *sql.DB to
// route to (via tenantdb.Registry) — isolation is physical (a separate
// database per tenant), so no method filters rows by tenant_code as a
// WHERE-clause column.
type Store interface {
	// UpsertFact writes or updates a fact. For FactKindPreference with a
	// non-empty Key, this upserts by (user_id, kind, key). For
	// FactKindSessionSummary, Key must be the source session ID so the
	// same unique constraint gives one-row-per-session semantics.
	UpsertFact(ctx context.Context, tenantCode string, fact Fact) error

	// ListFacts returns a user's durable facts, optionally filtered by
	// kind (pass "" for all kinds).
	ListFacts(ctx context.Context, tenantCode, userID string, kind FactKind) ([]Fact, error)
}

// MySQLStore is a Store backed by each tenant's own MySQL database,
// reached exclusively through registry per PROJECT.md §6.2.
type MySQLStore struct {
	registry *tenantdb.Registry
}

// NewMySQLStore builds a Store that resolves connections via registry.
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
