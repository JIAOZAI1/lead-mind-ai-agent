package handler

import (
	"encoding/json"
	"net/http"

	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/tenant"
)

type chatRequest struct {
	SessionID string `json:"session_id"`
	Message   string `json:"message"`
}

type chatResponse struct {
	TenantID string `json:"tenant_id"`
	Reply    string `json:"reply"`
}

// Chat handles POST /v1/chat. It is a routing/wiring placeholder: the
// Agent orchestration layer (internal/agent) does not exist yet, so this
// only proves the request reaches the handler with tenant context intact.
// TODO: replace the mock body with a call into internal/agent once the
// ReAct agent lands (see PROJECT.md §5 阶段一).
func Chat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	var req chatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, `{"error":"invalid request body"}`, http.StatusBadRequest)
		return
	}

	tenantID, _ := tenant.FromContext(r.Context())

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(chatResponse{
		TenantID: tenantID,
		Reply:    "not implemented yet: agent layer is not wired up",
	})
}
