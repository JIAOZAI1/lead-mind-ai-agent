package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"github.com/cloudwego/eino/schema"

	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/identity"
	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/memory"
	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/session"
	pkgschema "github.com/JIAOZAI1/lead-mind-ai-agent/pkg/schema"
)

type streamSessionEvent struct {
	SessionID string `json:"session_id"`
}

type streamDeltaEvent struct {
	TenantCode string `json:"tenant_code"`
	Delta      string `json:"delta"`
}

// ChatStream handles GET /ai-agent/v1/chat/stream?message=...&session_id=...
// over SSE, streaming the ReAct agent's content deltas as they're
// generated. Like Chat, it loads/persists session-scoped conversation
// history and registers/touches session metadata; unlike Chat, the
// session_id is communicated via a dedicated first SSE frame rather than
// a JSON response body field. Tool-call intermediate steps are not
// surfaced to the client yet — only the final assistant message stream
// (see internal/agent/react.New, which does not yet distinguish tool-call
// chunks from content chunks for the caller).
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
	id, _ := identity.FromContext(ctx)

	clientSessionID := r.URL.Query().Get("session_id")
	isNewSession := clientSessionID == ""
	sessionID := session.Resolve(clientSessionID)

	if isNewSession {
		if err := d.Sessions.Create(ctx, id.TenantCode, session.Session{
			ID:     sessionID,
			UserID: id.UserID,
			Title:  defaultTitle(message),
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
		httpError(ctx, w, r, err, "failed to load conversation history", http.StatusInternalServerError)
		return
	}

	a, err := d.newAgent(ctx)
	if err != nil {
		httpError(ctx, w, r, err, "agent unavailable", http.StatusInternalServerError)
		return
	}

	input := pkgschema.ToEinoMessages(history)
	input = append(input, schema.UserMessage(message))

	stream, err := a.Stream(ctx, input)
	if err != nil {
		httpError(ctx, w, r, err, "agent stream failed", http.StatusBadGateway)
		return
	}
	defer stream.Close()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // disable nginx buffering for SSE

	sessionData, _ := json.Marshal(streamSessionEvent{SessionID: sessionID})
	fmt.Fprintf(w, "event: session\ndata: %s\n\n", sessionData)
	flusher.Flush()

	var reply strings.Builder

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
			// Headers (200) are already flushed at this point, so the
			// error can only go out as an SSE frame — but that leaves no
			// server-side trace unless logged explicitly here, since the
			// wrapping middleware.Logging will record this request as a
			// plain 200.
			slog.Error("chat stream error",
				"method", r.Method,
				"path", r.URL.Path,
				"tenant_code", id.TenantCode,
				"user_id", id.UserID,
				"session_id", sessionID,
				"error", err,
			)
			data, _ := json.Marshal(map[string]string{"error": err.Error()})
			fmt.Fprintf(w, "event: error\ndata: %s\n\n", data)
			flusher.Flush()
			return
		}
		if chunk.Content == "" {
			continue
		}
		reply.WriteString(chunk.Content)

		data, _ := json.Marshal(streamDeltaEvent{TenantCode: id.TenantCode, Delta: chunk.Content})
		fmt.Fprintf(w, "event: message\ndata: %s\n\n", data)
		flusher.Flush()
	}

	newHistory := append(history,
		pkgschema.FromEinoMessage(schema.UserMessage(message)),
		pkgschema.Message{Role: pkgschema.RoleAssistant, Content: reply.String()},
	)
	compacted := memory.Compact(ctx, d.Compaction, newHistory)
	if err := d.ShortTerm.ReplaceHistory(ctx, id.TenantCode, sessionID, compacted); err != nil {
		slog.Error("chat stream error",
			"method", r.Method,
			"path", r.URL.Path,
			"tenant_code", id.TenantCode,
			"user_id", id.UserID,
			"session_id", sessionID,
			"error", err,
		)
		data, _ := json.Marshal(map[string]string{"error": "failed to persist conversation history"})
		fmt.Fprintf(w, "event: error\ndata: %s\n\n", data)
		flusher.Flush()
		return
	}

	fmt.Fprint(w, "event: done\ndata: {}\n\n")
	flusher.Flush()
}
