package session

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/tenantdb"
)

// Session 是一个对话会话元数据的持久化记录。它刻意不携带对话内容
// ——对话内容存在 internal/memory/shortterm 中，独立过期（参见
// PROJECT.md 中"会话列表需要在短期记忆 TTL 之后依然保持持久"的决策）。
type Session struct {
	ID           string
	UserID       string
	Title        string
	Pinned       bool
	Archived     bool
	CreatedAt    time.Time
	LastActiveAt time.Time
}

// Store 在租户的 MySQL 数据库中管理会话元数据。
type Store interface {
	// Create 注册一个新会话的元数据。仅在首次生成某个 session ID 时
	// 调用一次。
	Create(ctx context.Context, tenantCode string, s Session) error

	// Touch 将 last_active_at 更新为当前时间。在已有会话的每一轮对话
	// 中都会被调用。
	Touch(ctx context.Context, tenantCode, sessionID string) error

	// Rename 更新会话标题。
	Rename(ctx context.Context, tenantCode, sessionID, title string) error

	// SetPinned 切换置顶标记。
	SetPinned(ctx context.Context, tenantCode, sessionID string, pinned bool) error

	// SetArchived 切换归档标记。
	SetArchived(ctx context.Context, tenantCode, sessionID string, archived bool) error

	// Delete 删除一条会话元数据记录。
	Delete(ctx context.Context, tenantCode, sessionID string) error

	// List 返回 userID 的会话列表，置顶的排在前面，其余按最近活跃排序，
	// 可选择是否包含已归档的会话。
	List(ctx context.Context, tenantCode, userID string, includeArchived bool) ([]Session, error)

	// Get 返回单个会话的元数据；如果不存在，返回 (Session{}, false, nil)。
	Get(ctx context.Context, tenantCode, sessionID string) (Session, bool, error)
}

// MySQLStore 是一个基于每个租户自有 MySQL 数据库实现的 Store，根据
// PROJECT.md §6.2，一律通过 registry 获取连接——绝不使用直连字符串。
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

func (s *MySQLStore) Create(ctx context.Context, tenantCode string, sess Session) error {
	db, err := s.db(ctx, tenantCode)
	if err != nil {
		return fmt.Errorf("resolve tenant db: %w", err)
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
