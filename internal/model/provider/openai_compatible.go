// Package provider adapts external model APIs to Eino's model.ChatModel
// interface. NewOpenAICompatible is the entry point for any provider that
// speaks the OpenAI chat-completions protocol, which covers most domestic
// providers' compatible mode (Doubao/Ark, Qwen/DashScope, etc.) as well as
// OpenAI itself — see PROJECT.md §1.3 and §7 decision log.
package provider

import (
	"context"
	"fmt"

	openaicomp "github.com/cloudwego/eino-ext/components/model/openai"
	einomodel "github.com/cloudwego/eino/components/model"

	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/model"
)

// NewOpenAICompatible builds a ToolCallingChatModel backed by any
// OpenAI-compatible endpoint.
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
