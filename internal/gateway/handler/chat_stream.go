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

// ChatStream 通过 SSE 处理
// GET /ai-agent/v1/chat/stream?message=...&session_id=...，将 ReAct
// agent 生成的内容增量实时流式返回。与 Chat 类似，本接口也会
// 加载/持久化会话级对话历史、注册/刷新会话元数据、并将本轮内容追加
// 写入持久化记录；与 Chat 不同的是，session_id 是通过专门的第一帧
// SSE 事件传递的，而不是 JSON 响应体中的字段。工具调用的中间步骤
// 目前还不会展示给客户端——只有最终的 assistant 消息内容会被流式
// 输出（参见 internal/agent/react.New，它目前尚未对调用方区分工具
// 调用的数据块和内容数据块）。
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

	agent, err := d.newAgent(ctx)
	if err != nil {
		httpError(ctx, w, r, err, "agent unavailable", http.StatusInternalServerError)
		return
	}

	input := pkgschema.ToEinoMessages(history)
	input = append(input, schema.UserMessage(message))

	stream, err := agent.Stream(ctx, input)
	if err != nil {
		httpError(ctx, w, r, err, "agent stream failed", http.StatusBadGateway)
		return
	}
	defer stream.Close()

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // 关闭 nginx 对 SSE 的缓冲

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
			// 此时响应头（200）已经被 flush 出去了，所以错误只能通过
			// SSE 帧发送——但如果这里不显式记录日志，就不会留下任何
			// 服务端痕迹，因为外层的 middleware.Logging 只会把这个
			// 请求记录成普通的 200。
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

	newTurns := []pkgschema.Message{
		pkgschema.FromEinoMessage(schema.UserMessage(message)),
		{Role: pkgschema.RoleAssistant, Content: reply.String()},
	}
	newHistory := append(history, newTurns...)
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
	if err := d.Transcript.AppendTurns(ctx, id.TenantCode, id.UserID, sessionID, newTurns); err != nil {
		slog.Error("chat stream error",
			"method", r.Method,
			"path", r.URL.Path,
			"tenant_code", id.TenantCode,
			"user_id", id.UserID,
			"session_id", sessionID,
			"error", err,
		)
		data, _ := json.Marshal(map[string]string{"error": "failed to persist conversation transcript"})
		fmt.Fprintf(w, "event: error\ndata: %s\n\n", data)
		flusher.Flush()
		return
	}

	fmt.Fprint(w, "event: done\ndata: {}\n\n")
	flusher.Flush()
}
