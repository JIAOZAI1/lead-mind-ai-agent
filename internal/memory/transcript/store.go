// Package transcript 在每个租户的 MySQL 数据库中持久化保存一个会话
// 完整、未删减的对话记录，不设 TTL、不做压缩——不同于
// internal/memory/shortterm（Redis，受 TTL 限制）和
// internal/memory/compaction.go（为了模型上下文窗口而对较早轮次做
// 摘要/丢弃），本包是用户可以从会话列表中一直翻回去查看的持久化记录，
// 与对话发生的时间无关。它是只追加（append-only）的：轮次一旦写入
// 便不会被就地改写或摘要。
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

// Turn 是会话持久化记录中的一条消息。
type Turn struct {
	SessionID string
	UserID    string
	Message   pkgschema.Message
	CreatedAt time.Time
}

// Store 负责追加写入并读取一个会话的持久化对话记录。
type Store interface {
	// AppendTurns 按给定顺序持久化记录 (tenant, session) 的新增轮次。
	// 调用方只需传入本次请求新产生的轮次，而不是完整的累积历史——本
	// store 是只追加的，写入后不会被改写，这一点与 shortterm.Store
	// 不同。
	AppendTurns(ctx context.Context, tenantCode, userID, sessionID string, turns []pkgschema.Message) error

	// ListTurns 按时间顺序返回 (tenant, session) 已持久化的全部轮次；
	// 如果还没有任何记录，返回空切片。
	ListTurns(ctx context.Context, tenantCode, sessionID string) ([]Turn, error)
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
