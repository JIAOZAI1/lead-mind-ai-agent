// Package chat 提供 Agent 对话 HTTP 接口。
package chat

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/agent"
	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/transport/http/response"
)

// AgentStreamer 表示对话 HTTP 层所需的最小 Agent 流式能力。
type AgentStreamer interface {
	Stream(ctx context.Context, message string) (agent.TextStream, error)
}

// Handler 负责校验对话请求并输出 SSE 事件。
type Handler struct {
	agent  AgentStreamer // agent 提供与具体模型供应商无关的文本流。
	logger *slog.Logger  // logger 记录无法安全返回给客户端的内部错误。
}

// Request 表示单轮 Agent 对话请求。
type Request struct {
	Message string `json:"message" binding:"required"`
}

// Chunk 表示一个 SSE 文本增量事件。
type Chunk struct {
	Content string `json:"content"`
}

// Done 表示 Agent 流已正常结束。
type Done struct {
	FinishReason string `json:"finish_reason"`
}

// NewHandler 创建 Agent SSE 请求处理器。
func NewHandler(agentStreamer AgentStreamer, logger *slog.Logger) *Handler {
	return &Handler{
		agent:  agentStreamer,
		logger: logger,
	}
}

// Chat 处理一次无状态 Agent 对话并以 SSE 持续输出结果。
func (h *Handler) Chat(c *gin.Context) {
	var request Request
	if err := c.ShouldBindJSON(&request); err != nil {
		response.Error(c, http.StatusBadRequest, "请求体必须包含非空的 message 字段")
		return
	}

	stream, err := h.agent.Stream(c.Request.Context(), request.Message)
	if err != nil {
		if errors.Is(err, agent.ErrMessageRequired) || errors.Is(err, agent.ErrMessageTooLong) {
			response.Error(c, http.StatusBadRequest, err.Error())
			return
		}
		h.logger.Error("启动 Agent 流失败", "error", err)
		response.Error(c, http.StatusInternalServerError, "Agent 服务暂时不可用")
		return
	}
	defer stream.Close()

	setSSEHeaders(c)
	h.streamResponse(c, stream)
}

func (h *Handler) streamResponse(c *gin.Context, stream agent.TextStream) {
	for {
		content, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			response.SSESuccess(c, "done", Done{FinishReason: "stop"})
			c.Writer.Flush()
			return
		}
		if err != nil {
			h.writeStreamError(c, err)
			return
		}
		if content == "" {
			continue
		}

		response.SSESuccess(c, "message", Chunk{Content: content})
		c.Writer.Flush()
	}
}

func (h *Handler) writeStreamError(c *gin.Context, err error) {
	// 客户端断开会取消请求，此时无需记录为服务故障或继续写响应。
	if c.Request.Context().Err() != nil {
		return
	}

	h.logger.Error("Agent 流式响应失败", "error", err)
	response.SSEError(c, "error", http.StatusInternalServerError, "Agent 生成响应失败")
	c.Writer.Flush()
}

func setSSEHeaders(c *gin.Context) {
	c.Header("Content-Type", "text/event-stream; charset=utf-8")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")
}
