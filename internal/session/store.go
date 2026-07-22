package session

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/tenantdb"
)

// Session is a durable record of a conversation session's metadata. It
// deliberately does not carry conversation content — that lives in
// internal/memory/shortterm and expires independently (see PROJECT.md
// decision to keep the session list persistent past the short-term
// memory TTL).
type Session struct {
	ID           string
	UserID       string
	Title        string
	Pinned       bool
	Archived     bool
	CreatedAt    time.Time
	LastActiveAt time.Time
}

// Store manages session metadata in a tenant's MySQL database.
type Store interface {
	// Create registers a new session's metadata. Called once, the first
	// time a session ID is minted.
	Create(ctx context.Context, tenantCode string, s Session) error

	// Touch bumps last_active_at to now. Called on every turn of an
	// existing session.
	Touch(ctx context.Context, tenantCode, sessionID string) error

	// Rename updates a session's title.
	Rename(ctx context.Context, tenantCode, sessionID, title string) error

	// SetPinned toggles the pinned flag.
	SetPinned(ctx context.Context, tenantCode, sessionID string, pinned bool) error

	// SetArchived toggles the archived flag.
	SetArchived(ctx context.Context, tenantCode, sessionID string, archived bool) error

	// Delete removes a session's metadata record.
	Delete(ctx context.Context, tenantCode, sessionID string) error

	// List returns userID's sessions, pinned first then most-recently-active,
	// optionally including archived ones.
	List(ctx context.Context, tenantCode, userID string, includeArchived bool) ([]Session, error)

	// Get returns a single session's metadata, or (Session{}, false, nil)
	// if it doesn't exist.
	Get(ctx context.Context, tenantCode, sessionID string) (Session, bool, error)
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

func (s *MySQLStore) Create(ctx context.Context, tenantCode string, sess Session) error {
	db, err := s.db(ctx, tenantCode)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO agent_sessions (id, user_id, title, pinned, archived)
		VALUES (?, ?, ?, ?, ?)`,
		sess.ID, sess.UserID, sess.Title, sess.Pinned, sess.Archived)
	if err != nil {
		return fmt.Errorf("create session %s: %w", sess.ID, err)
	}
	return nil
}

func (s *MySQLStore) Touch(ctx context.Context, tenantCode, sessionID string) error {
	db, err := s.db(ctx, tenantCode)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `
		UPDATE agent_sessions SET last_active_at = CURRENT_TIMESTAMP(3) WHERE id = ?`,
		sessionID)
	if err != nil {
		return fmt.Errorf("touch session %s: %w", sessionID, err)
	}
	return nil
}

func (s *MySQLStore) Rename(ctx context.Context, tenantCode, sessionID, title string) error {
	db, err := s.db(ctx, tenantCode)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `UPDATE agent_sessions SET title = ? WHERE id = ?`, title, sessionID)
	if err != nil {
		return fmt.Errorf("rename session %s: %w", sessionID, err)
	}
	return nil
}

func (s *MySQLStore) SetPinned(ctx context.Context, tenantCode, sessionID string, pinned bool) error {
	db, err := s.db(ctx, tenantCode)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `UPDATE agent_sessions SET pinned = ? WHERE id = ?`, pinned, sessionID)
	if err != nil {
		return fmt.Errorf("set pinned for session %s: %w", sessionID, err)
	}
	return nil
}

func (s *MySQLStore) SetArchived(ctx context.Context, tenantCode, sessionID string, archived bool) error {
	db, err := s.db(ctx, tenantCode)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `UPDATE agent_sessions SET archived = ? WHERE id = ?`, archived, sessionID)
	if err != nil {
		return fmt.Errorf("set archived for session %s: %w", sessionID, err)
	}
	return nil
}

func (s *MySQLStore) Delete(ctx context.Context, tenantCode, sessionID string) error {
	db, err := s.db(ctx, tenantCode)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `DELETE FROM agent_sessions WHERE id = ?`, sessionID)
	if err != nil {
		return fmt.Errorf("delete session %s: %w", sessionID, err)
	}
	return nil
}

func (s *MySQLStore) List(ctx context.Context, tenantCode, userID string, includeArchived bool) ([]Session, error) {
	db, err := s.db(ctx, tenantCode)
	if err != nil {
		return nil, err
	}

	query := `
		SELECT id, user_id, title, pinned, archived, created_at, last_active_at
		FROM agent_sessions
		WHERE user_id = ?`
	args := []any{userID}
	if !includeArchived {
		query += ` AND archived = 0`
	}
	query += ` ORDER BY pinned DESC, last_active_at DESC`

	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list sessions for user %s: %w", userID, err)
	}
	defer rows.Close()

	var out []Session
	for rows.Next() {
		var sess Session
		if err := rows.Scan(&sess.ID, &sess.UserID, &sess.Title, &sess.Pinned, &sess.Archived, &sess.CreatedAt, &sess.LastActiveAt); err != nil {
			return nil, fmt.Errorf("scan session row: %w", err)
		}
		out = append(out, sess)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate session rows: %w", err)
	}
	return out, nil
}

func (s *MySQLStore) Get(ctx context.Context, tenantCode, sessionID string) (Session, bool, error) {
	db, err := s.db(ctx, tenantCode)
	if err != nil {
		return Session{}, false, err
	}

	var sess Session
	err = db.QueryRowContext(ctx, `
		SELECT id, user_id, title, pinned, archived, created_at, last_active_at
		FROM agent_sessions WHERE id = ?`, sessionID).
		Scan(&sess.ID, &sess.UserID, &sess.Title, &sess.Pinned, &sess.Archived, &sess.CreatedAt, &sess.LastActiveAt)
	if err == sql.ErrNoRows {
		return Session{}, false, nil
	}
	if err != nil {
		return Session{}, false, fmt.Errorf("get session %s: %w", sessionID, err)
	}
	return sess, true, nil
}
