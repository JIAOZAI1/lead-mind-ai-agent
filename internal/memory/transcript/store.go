// Package transcript persists the full, unabridged conversation
// transcript for a session in each tenant's MySQL database, with no TTL
// and no compaction — unlike internal/memory/shortterm (Redis,
// TTL-bounded) and internal/memory/compaction.go (summarizes/drops older
// turns for the model's context window), this package is the durable
// record a user can page back through from the session list, independent
// of how long ago the conversation happened. It is append-only: turns are
// never rewritten or summarized in place.
package transcript

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/tenantdb"
	pkgschema "github.com/JIAOZAI1/lead-mind-ai-agent/pkg/schema"
)

// Turn is one persisted message in a session's durable transcript.
type Turn struct {
	SessionID string
	UserID    string
	Message   pkgschema.Message
	CreatedAt time.Time
}

// Store appends to and reads back a session's durable transcript.
type Store interface {
	// AppendTurns durably records new turns for (tenant, session), in
	// the given order. Callers pass only the newly produced turns for
	// this request, not the full accumulated history — this store is
	// append-only and never rewritten, unlike shortterm.Store.
	AppendTurns(ctx context.Context, tenantCode, userID, sessionID string, turns []pkgschema.Message) error

	// ListTurns returns every persisted turn for (tenant, session) in
	// chronological order, or an empty slice if none exist yet.
	ListTurns(ctx context.Context, tenantCode, sessionID string) ([]Turn, error)
}

// MySQLStore is a Store backed by each tenant's own MySQL database,
// reached exclusively through registry — never a direct connection
// string — per PROJECT.md §6.2.
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

func (s *MySQLStore) AppendTurns(ctx context.Context, tenantCode, userID, sessionID string, turns []pkgschema.Message) error {
	if len(turns) == 0 {
		return nil
	}

	db, err := s.db(ctx, tenantCode)
	if err != nil {
		return err
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transcript append for session %s: %w", sessionID, err)
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO agent_conversation_turns
			(session_id, user_id, role, content, name, tool_calls, tool_call_id, tool_name)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare transcript append for session %s: %w", sessionID, err)
	}
	defer stmt.Close()

	for _, m := range turns {
		var toolCalls any
		if len(m.ToolCalls) > 0 {
			raw, err := json.Marshal(m.ToolCalls)
			if err != nil {
				return fmt.Errorf("encode tool calls for session %s: %w", sessionID, err)
			}
			toolCalls = raw
		}

		if _, err := stmt.ExecContext(ctx,
			sessionID, userID, string(m.Role), m.Content, m.Name, toolCalls, m.ToolCallID, m.ToolName,
		); err != nil {
			return fmt.Errorf("append transcript turn for session %s: %w", sessionID, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transcript append for session %s: %w", sessionID, err)
	}
	return nil
}

func (s *MySQLStore) ListTurns(ctx context.Context, tenantCode, sessionID string) ([]Turn, error) {
	db, err := s.db(ctx, tenantCode)
	if err != nil {
		return nil, err
	}

	rows, err := db.QueryContext(ctx, `
		SELECT session_id, user_id, role, content, name, tool_calls, tool_call_id, tool_name, created_at
		FROM agent_conversation_turns
		WHERE session_id = ?
		ORDER BY created_at ASC, id ASC`, sessionID)
	if err != nil {
		return nil, fmt.Errorf("list transcript for session %s: %w", sessionID, err)
	}
	defer rows.Close()

	out := make([]Turn, 0)
	for rows.Next() {
		var (
			t            Turn
			role         string
			toolCallsRaw sql.NullString
		)
		if err := rows.Scan(&t.SessionID, &t.UserID, &role, &t.Message.Content, &t.Message.Name,
			&toolCallsRaw, &t.Message.ToolCallID, &t.Message.ToolName, &t.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan transcript row for session %s: %w", sessionID, err)
		}
		t.Message.Role = pkgschema.Role(role)
		if toolCallsRaw.Valid && toolCallsRaw.String != "" {
			if err := json.Unmarshal([]byte(toolCallsRaw.String), &t.Message.ToolCalls); err != nil {
				return nil, fmt.Errorf("decode tool calls for session %s: %w", sessionID, err)
			}
		}
		out = append(out, t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate transcript rows for session %s: %w", sessionID, err)
	}
	return out, nil
}
