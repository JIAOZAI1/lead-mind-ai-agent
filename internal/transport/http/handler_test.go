package http

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

// mockAgentStreamer 为 HTTP 测试提供可预测的 Agent 输出。
type mockAgentStreamer struct {
	chunks []string // chunks 是依次返回的 Agent 文本片段。
	err    error    // err 是文本片段读取完毕后返回的错误。
}

func (m *mockAgentStreamer) Stream(context.Context, string) (agent.TextStream, error) {
	return &mockTextStream{chunks: m.chunks, finalErr: m.err}, nil
}

// mockTextStream 实现 HTTP 测试所需的 Agent 文本流。
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

func TestHealth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/healthz", nil)

	NewRouter(&mockAgentStreamer{}, testLogger()).ServeHTTP(recorder, request)

	if got, want := recorder.Code, http.StatusOK; got != want {
		t.Errorf("状态码 = %d，期望 %d", got, want)
	}
	if got, want := recorder.Body.String(), "{\"status\":\"ok\"}"; got != want {
		t.Errorf("响应体 = %q，期望 %q", got, want)
	}
}

func TestChatStreamsSSE(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/ai-agent/chat",
		bytes.NewBufferString(`{"message":"你好"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	agentStreamer := &mockAgentStreamer{chunks: []string{"你好，", "世界"}}

	NewRouter(agentStreamer, testLogger()).ServeHTTP(recorder, request)

	if got, want := recorder.Code, http.StatusOK; got != want {
		t.Errorf("状态码 = %d，期望 %d", got, want)
	}
	if got := recorder.Header().Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
		t.Errorf("Content-Type = %q，期望 text/event-stream", got)
	}
	assertBodyContains(t, recorder.Body.String(), "event:message", `data:{"content":"你好，"}`)
	assertBodyContains(t, recorder.Body.String(), `data:{"content":"世界"}`, "event:done")
}

func TestChatStreamsSanitizedError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(
		http.MethodPost,
		"/ai-agent/chat",
		bytes.NewBufferString(`{"message":"你好"}`),
	)
	request.Header.Set("Content-Type", "application/json")
	agentStreamer := &mockAgentStreamer{err: errors.New("provider secret error")}

	NewRouter(agentStreamer, testLogger()).ServeHTTP(recorder, request)

	assertBodyContains(t, recorder.Body.String(), "event:error", "Agent 生成响应失败")
	if strings.Contains(recorder.Body.String(), "provider secret error") {
		t.Error("SSE 响应泄露了内部错误")
	}
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
