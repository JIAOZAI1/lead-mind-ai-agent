package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/identity"
	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/session"
	pkgschema "github.com/JIAOZAI1/lead-mind-ai-agent/pkg/schema"
)

type sessionResponse struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Pinned       bool   `json:"pinned"`
	Archived     bool   `json:"archived"`
	CreatedAt    string `json:"created_at"`
	LastActiveAt string `json:"last_active_at"`
}

func toSessionResponse(s session.Session) sessionResponse {
	return sessionResponse{
		ID:           s.ID,
		Title:        s.Title,
		Pinned:       s.Pinned,
		Archived:     s.Archived,
		CreatedAt:    s.CreatedAt.Format(time.RFC3339),
		LastActiveAt: s.LastActiveAt.Format(time.RFC3339),
	}
}

type sessionPatchRequest struct {
	Title    *string `json:"title"`
	Pinned   *bool   `json:"pinned"`
	Archived *bool   `json:"archived"`
}

// ListSessions 处理 GET /ai-agent/v1/sessions?include_archived=false，
// 返回调用方自己的会话列表——只包含元数据（标题、置顶/归档状态、
// 时间戳），绝不包含对话内容（对话内容见 internal/memory/shortterm，
// 其 TTL 与本列表的生命周期相互独立）。
func (d AgentDeps) ListSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()
	id, _ := identity.FromContext(ctx)

	includeArchived := r.URL.Query().Get("include_archived") == "true"

	sessions, err := d.Sessions.List(ctx, id.TenantCode, id.UserID, includeArchived)
	if err != nil {
		httpError(ctx, w, r, err, "failed to list sessions", http.StatusInternalServerError)
		return
	}

	out := make([]sessionResponse, len(sessions))
	for i, s := range sessions {
		out[i] = toSessionResponse(s)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// PatchSession 处理 PATCH /ai-agent/v1/sessions/{id}，支持对
// title/pinned/archived 做部分字段更新。
func (d AgentDeps) PatchSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPatch {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()
	id, _ := identity.FromContext(ctx)
	sessionID := r.PathValue("id")

	owned, err := d.ownsSession(ctx, id.TenantCode, id.UserID, sessionID)
	if err != nil {
		httpError(ctx, w, r, err, "failed to look up session", http.StatusInternalServerError)
		return
	}
	if !owned {
		http.Error(w, `{"error":"session not found"}`, http.StatusNotFound)
		return
	}

	var req sessionPatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(ctx, w, r, err, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.Title != nil {
		if err := d.Sessions.Rename(ctx, id.TenantCode, sessionID, *req.Title); err != nil {
			httpError(ctx, w, r, err, "failed to rename session", http.StatusInternalServerError)
			return
		}
	}
	if req.Pinned != nil {
		if err := d.Sessions.SetPinned(ctx, id.TenantCode, sessionID, *req.Pinned); err != nil {
			httpError(ctx, w, r, err, "failed to update session", http.StatusInternalServerError)
			return
		}
	}
	if req.Archived != nil {
		if err := d.Sessions.SetArchived(ctx, id.TenantCode, sessionID, *req.Archived); err != nil {
			httpError(ctx, w, r, err, "failed to update session", http.StatusInternalServerError)
			return
		}
	}

	sess, ok, err := d.Sessions.Get(ctx, id.TenantCode, sessionID)
	if err != nil {
		httpError(ctx, w, r, err, "failed to reload session", http.StatusInternalServerError)
		return
	}
	if !ok {
		httpError(ctx, w, r, fmt.Errorf("session %s vanished after patch", sessionID), "failed to reload session", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(toSessionResponse(sess))
}

type sessionMessageResponse struct {
	Role       pkgschema.Role       `json:"role"`
	Content    string               `json:"content"`
	ToolCalls  []pkgschema.ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string               `json:"tool_call_id,omitempty"`
	ToolName   string               `json:"tool_name,omitempty"`
	CreatedAt  string               `json:"created_at"`
}

// GetSessionMessages 处理 GET /ai-agent/v1/sessions/{id}/messages，
// 按时间顺序返回该会话完整的持久化对话记录
// （internal/memory/transcript）——不同于驱动 agent 的短期 Redis
// 历史记录，本接口的数据不受 TTL 限制，因此它是客户端重新打开一个
// 旧会话、回看历史对话内容的数据来源。
func (d AgentDeps) GetSessionMessages(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()
	id, _ := identity.FromContext(ctx)
	sessionID := r.PathValue("id")

	owned, err := d.ownsSession(ctx, id.TenantCode, id.UserID, sessionID)
	if err != nil {
		httpError(ctx, w, r, err, "failed to look up session", http.StatusInternalServerError)
		return
	}
	if !owned {
		http.Error(w, `{"error":"session not found"}`, http.StatusNotFound)
		return
	}

	turns, err := d.Transcript.ListTurns(ctx, id.TenantCode, sessionID)
	if err != nil {
		httpError(ctx, w, r, err, "failed to load session transcript", http.StatusInternalServerError)
		return
	}

	out := make([]sessionMessageResponse, len(turns))
	for i, t := range turns {
		out[i] = sessionMessageResponse{
			Role:       t.Message.Role,
			Content:    t.Message.Content,
			ToolCalls:  t.Message.ToolCalls,
			ToolCallID: t.Message.ToolCallID,
			ToolName:   t.Message.ToolName,
			CreatedAt:  t.CreatedAt.Format(time.RFC3339),
		}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// DeleteSession 处理 DELETE /ai-agent/v1/sessions/{id}：删除会话元数据，
// 并清除尚未过期的短期历史记录。持久化的完整对话记录
// （internal/memory/transcript）被刻意保留——这是一个产品决策，不是
// 遗漏：删除会话记录会使其从会话列表中消失，并使 GetSessionMessages
// 返回 404（ownsSession 依赖元数据行是否存在），但底层的对话记录行
// 出于审计/合规目的仍会被保留；一旦其所属会话被删除，这些记录也就
// 无法再通过本 API 访问到了。
func (d AgentDeps) DeleteSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()
	id, _ := identity.FromContext(ctx)
	sessionID := r.PathValue("id")

	owned, err := d.ownsSession(ctx, id.TenantCode, id.UserID, sessionID)
	if err != nil {
		httpError(ctx, w, r, err, "failed to look up session", http.StatusInternalServerError)
		return
	}
	if !owned {
		http.Error(w, `{"error":"session not found"}`, http.StatusNotFound)
		return
	}

	if err := d.Sessions.Delete(ctx, id.TenantCode, sessionID); err != nil {
		httpError(ctx, w, r, err, "failed to delete session", http.StatusInternalServerError)
		return
	}
	if err := d.ShortTerm.Reset(ctx, id.TenantCode, sessionID); err != nil {
		httpError(ctx, w, r, err, "failed to clear session history", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ownsSession 报告 sessionID 是否存在且属于 userID。调用方应将"不存在"
// 和"不属于你"这两种情况一视同仁地处理（统一返回 404，而不是 403），
// 这样调用方就无法区分"会话不存在"和"会话存在但不是你的"。
func (d AgentDeps) ownsSession(ctx context.Context, tenantCode, userID, sessionID string) (bool, error) {
	sess, ok, err := d.Sessions.Get(ctx, tenantCode, sessionID)
	if err != nil {
		return false, err
	}
	if !ok || sess.UserID != userID {
		return false, nil
	}
	return true, nil
}
