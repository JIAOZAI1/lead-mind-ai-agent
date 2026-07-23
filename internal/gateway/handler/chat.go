package handler

import (
	"encoding/json"
	"net/http"

	"github.com/cloudwego/eino/schema"

	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/identity"
	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/memory"
	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/session"
	pkgschema "github.com/JIAOZAI1/lead-mind-ai-agent/pkg/schema"
)

type chatRequest struct {
	SessionID string `json:"session_id"`
	Message   string `json:"message"`
}

type chatResponse struct {
	TenantCode string `json:"tenant_code"`
	SessionID  string `json:"session_id"`
	Reply      string `json:"reply"`
}

// Chat 处理 POST /ai-agent/v1/chat：将请求消息交给 ReAct agent 处理，
// 读取并持久化会话级对话历史（internal/memory/shortterm），
// 注册/刷新会话的持久化元数据（internal/session），使其即便在短期记忆
// TTL 过期后依然出现在会话列表（GET /ai-agent/v1/sessions）中；同时将
// 本轮原始消息追加写入持久化、不压缩的完整记录
// （internal/memory/transcript），以便调用方在短期 TTL 过期后仍可通过
// GET /ai-agent/v1/sessions/{id}/messages 翻阅完整对话内容。
func (d AgentDeps) Chat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(r.Context(), w, r, err, "invalid request body", http.StatusBadRequest)
		return
	}
	if req.Message == "" {
		http.Error(w, `{"error":"message is required"}`, http.StatusBadRequest)
		return
	}

	ctx := r.Context()
	id, _ := identity.FromContext(ctx)

	isNewSession := req.SessionID == ""
	sessionID := session.Resolve(req.SessionID)

	if isNewSession {
		if err := d.Sessions.Create(ctx, id.TenantCode, session.Session{
			ID:     sessionID,
			UserID: id.UserID,
			Title:  defaultTitle(req.Message),
		}); err != nil {
			httpError(ctx, w, r, err, "failed to create session", http.StatusInternalServerError)
			return
		}
	} else if err := d.Sessions.Touch(ctx, id.TenantCode, sessionID); err != nil {
		httpError(ctx, w, r, err, "failed to update session", http.StatusInternalServerError)
		return
	}

	history, err := d.ShortTerm.LoadHistory(ctx, id.TenantCode, sessionID)
	if err != nil {
		// 这里选择直接失败，而不是静默降级为无上下文的回复：一个看似
		// 正常、实际却悄悄丢失了历史上下文的回复，比一个明确的错误更
		// 难被发现和排查。
		httpError(ctx, w, r, err, "failed to load conversation history", http.StatusInternalServerError)
		return
	}

	agent, err := d.newAgent(ctx)
	if err != nil {
		httpError(ctx, w, r, err, "agent unavailable", http.StatusInternalServerError)
		return
	}

	input := pkgschema.ToEinoMessages(history)
	input = append(input, schema.UserMessage(req.Message))

	reply, err := agent.Generate(ctx, input)
	if err != nil {
		httpError(ctx, w, r, err, "agent generation failed", http.StatusBadGateway)
		return
	}

	newTurns := []pkgschema.Message{pkgschema.FromEinoMessage(schema.UserMessage(req.Message)), pkgschema.FromEinoMessage(reply)}
	newHistory := append(history, newTurns...)
	compacted := memory.Compact(ctx, d.Compaction, newHistory)
	if err := d.ShortTerm.ReplaceHistory(ctx, id.TenantCode, sessionID, compacted); err != nil {
		httpError(ctx, w, r, err, "failed to persist conversation history", http.StatusInternalServerError)
		return
	}
	if err := d.Transcript.AppendTurns(ctx, id.TenantCode, id.UserID, sessionID, newTurns); err != nil {
		httpError(ctx, w, r, err, "failed to persist conversation transcript", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(chatResponse{
		TenantCode: id.TenantCode,
		SessionID:  sessionID,
		Reply:      reply.Content,
	})
}

// defaultTitle 从首条用户消息中派生一个低成本的默认会话标题，采用截断
// 而非模型摘要——参见 PROJECT.md 决策记录：AI 生成标题是范围之外的
// 后续需求，不是本功能的职责。
func defaultTitle(message string) string {
	const maxLen = 20
	runes := []rune(message)
	if len(runes) <= maxLen {
		return message
	}
	return string(runes[:maxLen]) + "..."
}
