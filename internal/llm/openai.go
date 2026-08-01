// Package llm 负责创建 Eino ChatModel 及模型供应商适配。
package llm

import (
	"context"
	"fmt"

	openaiModel "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"

	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/config"
)

// NewOpenAIChatModel 创建支持 OpenAI 与 OpenAI 兼容服务的 Eino ChatModel。
func NewOpenAIChatModel(ctx context.Context, cfg config.OpenAIConfig) (model.ToolCallingChatModel, error) {
	chatModel, err := openaiModel.NewChatModel(ctx, &openaiModel.ChatModelConfig{
		APIKey:  cfg.APIKey,
		BaseURL: cfg.BaseURL,
		Model:   cfg.Model,
		Timeout: cfg.Timeout,
	})
	if err != nil {
		return nil, fmt.Errorf("创建 OpenAI ChatModel: %w", err)
	}

	return chatModel, nil
}
