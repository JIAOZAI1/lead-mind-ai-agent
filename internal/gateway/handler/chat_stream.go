package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/cloudwego/eino/schema"

	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/tenant"
)

type streamDeltaEvent struct {
	TenantID string `json:"tenant_id"`
	Delta    string `json:"delta"`
}

// ChatStream handles GET /v1/chat/stream?message=... over SSE, streaming
// the ReAct agent's content deltas as they're generated. Tool-call
// intermediate steps are not surfaced to the client yet — only the final
// assistant message stream (see internal/agent/react.New, which does not
// yet distinguish tool-call chunks from content chunks for the caller).
func (d AgentDeps) ChatStream(w http.ResponseWriter, r *http.Request) {
	message := r.URL.Query().Get("message")
	if message == "" {
		http.Error(w, `{"error":"message query param is required"}`, http.StatusBadRequest)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, `{"error":"streaming unsupported"}`, http.StatusInternalServerError)
		return
	}

	ctx := r.Context()
	tenantID, _ := tenant.FromContext(ctx)

	a, err := d.newAgent(ctx)
	if err != nil {
		http.Error(w, `{"error":"agent unavailable"}`, http.StatusInternalServerError)
		return
	}

	stream, err := a.Stream(ctx, []*schema.Message{schema.UserMessage(message)})
	if err != nil {
		http.Error(w, `{"error":"agent stream failed"}`, http.StatusBadGateway)
		return
	}
	defer stream.Close()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable nginx buffering for SSE

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		chunk, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			data, _ := json.Marshal(map[string]string{"error": err.Error()})
			fmt.Fprintf(w, "event: error\ndata: %s\n\n", data)
			flusher.Flush()
			return
		}
		if chunk.Content == "" {
			continue
		}

		data, _ := json.Marshal(streamDeltaEvent{TenantID: tenantID, Delta: chunk.Content})
		fmt.Fprintf(w, "event: message\ndata: %s\n\n", data)
		flusher.Flush()
	}

	fmt.Fprint(w, "event: done\ndata: {}\n\n")
	flusher.Flush()
}
