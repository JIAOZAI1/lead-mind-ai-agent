package chat

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/agent"
)

// mockAgentStreamer 为对话 HTTP 测试提供可预测的 Agent 输出。
type mockAgentStreamer struct {
	chunks    []string // chunks 是依次返回的 Agent 文本片段。
	startErr  error    // startErr 是启动 Agent 流时返回的错误。
	streamErr error    // streamErr 是文本片段读取完毕后返回的错误。
}

func (m *mockAgentStreamer) Stream(context.Context, string) (agent.TextStream, error) {
	if m.startErr != nil {
		return nil, m.startErr
	}
	return &mockTextStream{chunks: m.chunks, finalErr: m.streamErr}, nil
}

// mockTextStream 实现对话 HTTP 测试所需的 Agent 文本流。
type mockTextStream struct {
	chunks   []string // chunks 保存尚未消费的文本片段。
	index    int      // index 指向下一条文本片段。
	finalErr error    // finalErr 在所有片段消费后返回。
}

func (s *mockTextStream) Recv() (string, error) {
	if s.index < len(s.chunks) {
		chunk := s.chunks[s.index]
		s.index++
		return chunk, nil
	}
	if s.finalErr != nil {
		return "", s.finalErr
	}
	return "", io.EOF
}

func (s *mockTextStream) Close() {}

func TestHandlerReturnsUnifiedValidationError(t *testing.T) {
	recorder := serveChatRequest(t, &mockAgentStreamer{}, `{}`)

	if got, want := recorder.Code, http.StatusBadRequest; got != want {
		t.Errorf("状态码 = %d，期望 %d", got, want)
	}
	if got, want := recorder.Body.String(), "{\"code\":400,\"message\":\"请求体必须包含非空的 message 字段\",\"data\":null}"; got != want {
		t.Errorf("响应体 = %q，期望 %q", got, want)
	}
}

func TestHandlerReturnsUnifiedInternalError(t *testing.T) {
	agentStreamer := &mockAgentStreamer{startErr: errors.New("provider secret error")}
	recorder := serveChatRequest(t, agentStreamer, `{"message":"你好"}`)

	if got, want := recorder.Code, http.StatusInternalServerError; got != want {
		t.Errorf("状态码 = %d，期望 %d", got, want)
	}
	assertBodyContains(t, recorder.Body.String(), `"code":500`, `"message":"Agent 服务暂时不可用"`, `"data":null`)
	if strings.Contains(recorder.Body.String(), "provider secret error") {
		t.Error("HTTP 响应泄露了内部错误")
	}
}

func TestHandlerStreamsSSE(t *testing.T) {
	agentStreamer := &mockAgentStreamer{chunks: []string{"你好，", "世界"}}
	recorder := serveChatRequest(t, agentStreamer, `{"message":"你好"}`)

	if got, want := recorder.Code, http.StatusOK; got != want {
		t.Errorf("状态码 = %d，期望 %d", got, want)
	}
	if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Errorf("Content-Type = %q，期望 text/event-stream", got)
	}
	assertBodyContains(t, recorder.Body.String(), "event:message", `data:{"code":0,"message":"success","data":{"content":"你好，"}}`)
	assertBodyContains(
		t,
		recorder.Body.String(),
		`data:{"code":0,"message":"success","data":{"content":"世界"}}`,
		"event:done",
		`data:{"code":0,"message":"success","data":{"finish_reason":"stop"}}`,
	)
}

func TestHandlerStreamsSanitizedError(t *testing.T) {
	agentStreamer := &mockAgentStreamer{streamErr: errors.New("provider secret error")}
	recorder := serveChatRequest(t, agentStreamer, `{"message":"你好"}`)

	assertBodyContains(t, recorder.Body.String(), "event:error", `data:{"code":500,"message":"Agent 生成响应失败","data":null}`)
	if strings.Contains(recorder.Body.String(), "provider secret error") {
		t.Error("SSE 响应泄露了内部错误")
	}
}

func serveChatRequest(t *testing.T, agentStreamer AgentStreamer, body string) *httptest.ResponseRecorder {
	t.Helper()

	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/ai-agent/chat",
		bytes.NewBufferString(body),
	)
	request.Header.Set("Content-Type", "application/json")
	router := gin.New()
	handler := NewHandler(agentStreamer, testLogger())
	router.POST("/ai-agent/chat", handler.Chat)

	router.ServeHTTP(recorder, request)
	return recorder
}

func assertBodyContains(t *testing.T, body string, values ...string) {
	t.Helper()

	for _, value := range values {
		if !strings.Contains(body, value) {
			t.Errorf("响应体 %q 不包含 %q", body, value)
		}
	}
}

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
