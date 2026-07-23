// Package provider 将外部模型 API 适配到 Eino 的 model.ChatModel 接口。
// NewOpenAICompatible 是任何支持 OpenAI chat-completions 协议的供应商的
// 统一入口，覆盖了大多数国内供应商的兼容模式（豆包/Ark、通义千问/
// DashScope 等）以及 OpenAI 本身——参见 PROJECT.md §1.3 及 §7 决策记录。
package provider

import (
	"context"
	"fmt"

	openaicomp "github.com/cloudwego/eino-ext/components/model/openai"
	einomodel "github.com/cloudwego/eino/components/model"

	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/model"
)

// NewOpenAICompatible 基于任意 OpenAI 兼容协议的接口构建一个
// ToolCallingChatModel。
func NewOpenAICompatible(ctx context.Context, cfg model.Config) (einomodel.ToolCallingChatModel, error) {
	cm, err := openaicomp.NewChatModel(ctx, &openaicomp.ChatModelConfig{
		BaseURL: cfg.BaseURL,
		APIKey:  cfg.APIKey,
		Model:   cfg.ModelName,
	})
	if err != nil {
		return nil, fmt.Errorf("create openai-compatible chat model: %w", err)
	}
	return cm, nil
}
