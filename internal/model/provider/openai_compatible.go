// Package provider 将外部模型 API 适配到 Eino 的 model.ChatModel 接口。
// NewOpenAICompatible 是任何支持 OpenAI chat-completions 协议的供应商的
// 统一入口，覆盖了大多数国内供应商的兼容模式（豆包/Ark、通义千问/
// DashScope 等）以及 OpenAI 本身——参见 PROJECT.md §1.3 及 §7 决策记录。
package provider

import (
	"context"
	"fmt"
	"sync"

	openaicomp "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/callbacks"
	einomodel "github.com/cloudwego/eino/components/model"

	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/model"
)

// _registerLoggingOnce 确保日志 callbacks.Handler 只被全局注册一次，
// 即便 NewOpenAICompatible 被多次调用（例如测试）。
var _registerLoggingOnce sync.Once

// NewOpenAICompatible 基于任意 OpenAI 兼容协议的接口构建一个
// ToolCallingChatModel。会全局注册一个 callbacks.Handler，在每次模型调用
// 时打印请求与响应，便于调试供应商返回的原始内容。
func NewOpenAICompatible(ctx context.Context, cfg model.Config) (einomodel.ToolCallingChatModel, error) {
	_registerLoggingOnce.Do(func() {
		callbacks.AppendGlobalHandlers(newLoggingHandler())
	})

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
