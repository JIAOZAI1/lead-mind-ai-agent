// Package agent 负责 Eino Agent 的装配和运行策略。
package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/config"
)

var (
	// ErrMessageRequired 表示用户没有提供有效的消息内容。
	ErrMessageRequired = errors.New("消息不能为空")
	// ErrMessageTooLong 表示用户消息超过配置允许的最大长度。
	ErrMessageTooLong = errors.New("消息超过最大长度")
)

// TextStream 表示 Agent 按文本片段输出的流。
type TextStream interface {
	Recv() (string, error)
	Close()
}

// Service 使用 Eino Runner 执行 ChatModelAgent。
type Service struct {
	runner         *adk.Runner // runner 负责执行 Eino Agent 并产生事件流。
	maxInputLength int         // maxInputLength 限制单次输入的 Unicode 字符数。
}

// NewService 创建一个支持流式输出的 Eino ChatModelAgent 服务。
func NewService(ctx context.Context, chatModel model.ToolCallingChatModel, cfg config.AgentConfig) (*Service, error) {
	chatModelAgent, err := adk.NewChatModelAgent(ctx, &adk.ChatModelAgentConfig{
		Name:          cfg.Name,
		Description:   cfg.Description,
		Instruction:   cfg.Instruction,
		Model:         chatModel,
		MaxIterations: cfg.MaxIterations,
	})
	if err != nil {
		return nil, fmt.Errorf("创建 Eino ChatModelAgent: %w", err)
	}

	runner := adk.NewRunner(ctx, adk.RunnerConfig{
		Agent:           chatModelAgent,
		EnableStreaming: true,
	})
	return &Service{
		runner:         runner,
		maxInputLength: cfg.MaxInputLength,
	}, nil
}

// Stream 校验用户输入并启动一次无状态的 Agent 流式执行。
func (s *Service) Stream(ctx context.Context, message string) (TextStream, error) {
	message = strings.TrimSpace(message)
	if message == "" {
		return nil, ErrMessageRequired
	}
	if len([]rune(message)) > s.maxInputLength {
		return nil, fmt.Errorf("%w，最多允许 %d 个字符", ErrMessageTooLong, s.maxInputLength)
	}

	return &einoTextStream{
		events: s.runner.Query(ctx, message),
	}, nil
}

// einoTextStream 将 Eino AgentEvent 和 MessageStream 转换为连续文本片段。
type einoTextStream struct {
	events  *adk.AsyncIterator[*adk.AgentEvent]   // events 是 Eino Agent 的顶层事件迭代器。
	current *schema.StreamReader[*schema.Message] // current 是当前正在消费的模型输出流。
}

// Recv 返回下一个非空的助手文本片段。
func (s *einoTextStream) Recv() (string, error) {
	for {
		if s.current != nil {
			content, finished, err := s.recvCurrent()
			if err != nil {
				return "", err
			}
			if finished || content == "" {
				continue
			}
			return content, nil
		}

		event, ok := s.events.Next()
		if !ok {
			return "", io.EOF
		}
		content, err := s.consumeEvent(event)
		if err != nil {
			return "", err
		}
		if content != "" {
			return content, nil
		}
	}
}

// Close 释放当前仍在消费的 Eino 消息流。
func (s *einoTextStream) Close() {
	if s.current != nil {
		s.current.Close()
		s.current = nil
	}
}

func (s *einoTextStream) recvCurrent() (string, bool, error) {
	message, err := s.current.Recv()
	if errors.Is(err, io.EOF) {
		s.current.Close()
		s.current = nil
		return "", true, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("读取 Eino 消息流: %w", err)
	}
	if message == nil {
		return "", false, nil
	}

	return message.Content, false, nil
}

func (s *einoTextStream) consumeEvent(event *adk.AgentEvent) (string, error) {
	if event == nil {
		return "", nil
	}
	if event.Err != nil {
		return "", fmt.Errorf("执行 Eino Agent: %w", event.Err)
	}
	if event.Output == nil || event.Output.MessageOutput == nil {
		return "", nil
	}

	output := event.Output.MessageOutput
	if output.Role != schema.Assistant {
		return "", nil
	}
	if output.IsStreaming {
		s.current = output.MessageStream
		return "", nil
	}
	if output.Message == nil {
		return "", nil
	}

	return output.Message.Content, nil
}
