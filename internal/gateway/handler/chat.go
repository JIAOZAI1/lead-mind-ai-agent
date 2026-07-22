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

// Chat handles POST /ai-agent/v1/chat: it runs the request message
// through a ReAct agent, using and persisting session-scoped conversation
// history (internal/memory/shortterm), and registering/touching the
// session's durable metadata (internal/session) so it shows up in the
// session list (GET /ai-agent/v1/sessions) even past the short-term
// memory TTL.
func (d AgentDeps) Chat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
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
			http.Error(w, `{"error":"failed to create session"}`, http.StatusInternalServerError)
			return
		}
	} else if err := d.Sessions.Touch(ctx, id.TenantCode, sessionID); err != nil {
		http.Error(w, `{"error":"failed to update session"}`, http.StatusInternalServerError)
		return
	}

	history, err := d.ShortTerm.LoadHistory(ctx, id.TenantCode, sessionID)
	if err != nil {
		// Fail closed rather than silently degrading to a stateless
		// reply: a reply that looks normal but has silently lost prior
		// context is harder to notice and debug than an explicit error.
		http.Error(w, `{"error":"failed to load conversation history"}`, http.StatusInternalServerError)
		return
	}

	a, err := d.newAgent(ctx)
	if err != nil {
		http.Error(w, `{"error":"agent unavailable"}`, http.StatusInternalServerError)
		return
	}

	input := pkgschema.ToEinoMessages(history)
	input = append(input, schema.UserMessage(req.Message))

	reply, err := a.Generate(ctx, input)
	if err != nil {
		http.Error(w, `{"error":"agent generation failed"}`, http.StatusBadGateway)
		return
	}

	newHistory := append(history, pkgschema.FromEinoMessage(schema.UserMessage(req.Message)), pkgschema.FromEinoMessage(reply))
	compacted := memory.Compact(ctx, d.Compaction, newHistory)
	if err := d.ShortTerm.ReplaceHistory(ctx, id.TenantCode, sessionID, compacted); err != nil {
		http.Error(w, `{"error":"failed to persist conversation history"}`, http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(chatResponse{
		TenantCode: id.TenantCode,
		SessionID:  sessionID,
		Reply:      reply.Content,
	})
}

// defaultTitle derives a cheap default session title from the first
// user message, truncated rather than model-summarized — see
// PROJECT.md decision log: AI-generated titles are an out-of-scope
// follow-up, not this feature's job.
func defaultTitle(message string) string {
	const maxLen = 20
	runes := []rune(message)
	if len(runes) <= maxLen {
		return message
	}
	return string(runes[:maxLen]) + "..."
}
