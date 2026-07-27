package device

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/tenantdb"
)

// MySQLStore 把设备登记在租户自己的 MySQL 库中。按 PROJECT.md §6.2，连接
// 一律通过 registry 解析——不接受任何形式的直连字符串。
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

func (s *MySQLStore) Create(ctx context.Context, tenantCode string, d Device) error {
	db, err := s.db(ctx, tenantCode)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO agent_browser_devices
			(id, user_id, name, fingerprint, token_hash, expires_at)
		VALUES (?, ?, ?, ?, ?, ?)`,
		d.ID, d.UserID, d.Name, d.Fingerprint, d.TokenHash, d.ExpiresAt.UTC())
	if err != nil {
		return fmt.Errorf("create device %s for user %s: %w", d.ID, d.UserID, err)
	}
	return nil
}

func (s *MySQLStore) FindByTokenHash(ctx context.Context, tenantCode, tokenHash string) (Device, bool, error) {
	db, err := s.db(ctx, tenantCode)
	if err != nil {
		return Device{}, false, err
	}

	var d Device
	var revokedAt sql.NullTime
	err = db.QueryRowContext(ctx, `
		SELECT id, user_id, name, fingerprint, token_hash, created_at, last_seen_at, expires_at, revoked_at
		FROM agent_browser_devices WHERE token_hash = ?`, tokenHash).
		Scan(&d.ID, &d.UserID, &d.Name, &d.Fingerprint, &d.TokenHash,
			&d.CreatedAt, &d.LastSeenAt, &d.ExpiresAt, &revokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Device{}, false, nil
	}
	if err != nil {
		// 错误信息里不带 tokenHash——它虽是哈希不是明文，但仍是能直接用于
		// 查表的凭证派生物，日志里出现就等于把定位凭证的能力泄露给了日志读者
		// （PROJECT.md §6.5 要求入参摘要脱敏）。
		return Device{}, false, fmt.Errorf("find device by token for tenant %s: %w", tenantCode, err)
	}
	if revokedAt.Valid {
		d.RevokedAt = &revokedAt.Time
	}
	return d, true, nil
}

func (s *MySQLStore) TouchLastSeen(ctx context.Context, tenantCode, deviceID string) error {
	db, err := s.db(ctx, tenantCode)
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `
		UPDATE agent_browser_devices SET last_seen_at = CURRENT_TIMESTAMP(3) WHERE id = ?`, deviceID)
	if err != nil {
		return fmt.Errorf("touch device %s: %w", deviceID, err)
	}
	return nil
}

func (s *MySQLStore) ListByUser(ctx context.Context, tenantCode, userID string) ([]Device, error) {
	db, err := s.db(ctx, tenantCode)
	if err != nil {
		return nil, err
	}

	rows, err := db.QueryContext(ctx, `
		SELECT id, user_id, name, fingerprint, token_hash, created_at, last_seen_at, expires_at, revoked_at
		FROM agent_browser_devices
		WHERE user_id = ?
		ORDER BY last_seen_at DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("list devices for user %s: %w", userID, err)
	}
	defer rows.Close()

	var out []Device
	for rows.Next() {
		var d Device
		var revokedAt sql.NullTime
		if err := rows.Scan(&d.ID, &d.UserID, &d.Name, &d.Fingerprint, &d.TokenHash,
			&d.CreatedAt, &d.LastSeenAt, &d.ExpiresAt, &revokedAt); err != nil {
			return nil, fmt.Errorf("scan device row: %w", err)
		}
		if revokedAt.Valid {
			d.RevokedAt = &revokedAt.Time
		}
		out = append(out, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate device rows: %w", err)
	}
	return out, nil
}

func (s *MySQLStore) Revoke(ctx context.Context, tenantCode, deviceID string) error {
	db, err := s.db(ctx, tenantCode)
	if err != nil {
		return err
	}
	// 只对尚未吊销的记录写入，保留首次吊销时间——重复吊销不该把时间往后推，
	// 那会让审计追溯不到真正的吊销时刻。
	_, err = db.ExecContext(ctx, `
		UPDATE agent_browser_devices
		SET revoked_at = CURRENT_TIMESTAMP(3)
		WHERE id = ? AND revoked_at IS NULL`, deviceID)
	if err != nil {
		return fmt.Errorf("revoke device %s: %w", deviceID, err)
	}
	return nil
}
