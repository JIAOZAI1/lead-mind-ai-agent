package agent

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/config"
)

// fakeChatModel 为测试提供确定性的 Eino 流式模型。
type fakeChatModel struct {
	chunks []*schema.Message // chunks 是模型依次返回的文本片段。
}

func (m *fakeChatModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return schema.AssistantMessage("测试响应", nil), nil
}

func (m *fakeChatModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return schema.StreamReaderFromArray(m.chunks), nil
}

func (m *fakeChatModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

func TestServiceStreamsEinoOutput(t *testing.T) {
	chatModel := &fakeChatModel{
		chunks: []*schema.Message{
			schema.AssistantMessage("你好，", nil),
			schema.AssistantMessage("世界", nil),
		},
	}
	service, err := NewService(context.Background(), chatModel, testAgentConfig())
	if err != nil {
		t.Fatalf("NewService() 返回错误: %v", err)
	}

	stream, err := service.Stream(context.Background(), "你好")
	if err != nil {
		t.Fatalf("Stream() 返回错误: %v", err)
	}
	defer stream.Close()

	assertNextChunk(t, stream, "你好，")
	assertNextChunk(t, stream, "世界")
	if _, err := stream.Recv(); !errors.Is(err, io.EOF) {
		t.Fatalf("流结束错误 = %v，期望 io.EOF", err)
	}
}

func TestServiceRejectsEmptyMessage(t *testing.T) {
	service, err := NewService(context.Background(), &fakeChatModel{}, testAgentConfig())
	if err != nil {
		t.Fatalf("NewService() 返回错误: %v", err)
	}

	_, err = service.Stream(context.Background(), "  ")
	if !errors.Is(err, ErrMessageRequired) {
		t.Fatalf("Stream() 错误 = %v，期望 ErrMessageRequired", err)
	}
}

func assertNextChunk(t *testing.T, stream TextStream, want string) {
	t.Helper()

	got, err := stream.Recv()
	if err != nil {
		t.Fatalf("Recv() 返回错误: %v", err)
	}
	if got != want {
		t.Errorf("Recv() = %q，期望 %q", got, want)
	}
}

func testAgentConfig() config.AgentConfig {
	return config.AgentConfig{
		Name:           "test-agent",
		Description:    "测试 Agent",
		Instruction:    "请回答测试问题。",
		MaxIterations:  2,
		MaxInputLength: 100,
	}
}
