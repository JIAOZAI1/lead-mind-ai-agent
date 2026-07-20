package handler

import (
	"fmt"
	"net/http"

	"github.com/leadmind/lead-mind-ai-agent/internal/tenant"
)

// ChatStream handles GET /v1/chat/stream over SSE. Like Chat, this is a
// wiring placeholder ahead of the Agent orchestration layer — it proves
// out the SSE framing (event/data lines, flush-per-chunk, client
// disconnect via ctx.Done) that a real token-streaming handler will reuse.
// TODO: replace the mock chunks with streamed output from internal/agent.
func ChatStream(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, `{"error":"streaming unsupported"}`, http.StatusInternalServerError)
		return
	}

	tenantID, _ := tenant.FromContext(r.Context())

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable nginx buffering for SSE

	chunks := []string{"not ", "implemented ", "yet: ", "agent ", "layer ", "is ", "not ", "wired ", "up"}

	ctx := r.Context()
	for _, chunk := range chunks {
		select {
		case <-ctx.Done():
			return
		default:
		}

		fmt.Fprintf(w, "event: message\ndata: {\"tenant_id\":%q,\"delta\":%q}\n\n", tenantID, chunk)
		flusher.Flush()
	}

	fmt.Fprint(w, "event: done\ndata: {}\n\n")
	flusher.Flush()
}
