package handler

import (
	"encoding/json"
	"net/http"

	"github.com/cloudwego/eino/schema"

	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/identity"
)

type chatRequest struct {
	SessionID string `json:"session_id"`
	Message   string `json:"message"`
}

type chatResponse struct {
	TenantCode string `json:"tenant_code"`
	Reply      string `json:"reply"`
}

// Chat handles POST /ai-agent/v1/chat: it runs the request message through a
// single-turn ReAct agent and returns the final reply. Session/multi-turn
// history is not wired up yet (see internal/memory, PROJECT.md §5 阶段一
// scope is single-turn tool calling).
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

	a, err := d.newAgent(ctx)
	if err != nil {
		http.Error(w, `{"error":"agent unavailable"}`, http.StatusInternalServerError)
		return
	}

	reply, err := a.Generate(ctx, []*schema.Message{schema.UserMessage(req.Message)})
	if err != nil {
		http.Error(w, `{"error":"agent generation failed"}`, http.StatusBadGateway)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(chatResponse{
		TenantCode: id.TenantCode,
		Reply:      reply.Content,
	})
}
