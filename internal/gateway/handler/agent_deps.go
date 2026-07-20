package handler

import (
	"context"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	einoreact "github.com/cloudwego/eino/flow/agent/react"

	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/agent/react"
)

// AgentDeps holds the shared, expensive-to-build resources the chat
// handlers need to construct a per-request ReAct agent: the ChatModel
// connection and the tool set. It is built once at startup (see
// cmd/server/main.go) and injected into the handlers.
//
// A new react.Agent is built per request rather than shared, since
// AgentConfig (system prompt, tool set) is expected to become
// per-tenant once tenant-configured agents land (PROJECT.md §1.2);
// react.NewAgent itself is cheap (graph construction, no I/O).
type AgentDeps struct {
	ChatModel    einomodel.ToolCallingChatModel
	Tools        []tool.BaseTool
	SystemPrompt string
}

func (d AgentDeps) newAgent(ctx context.Context) (*einoreact.Agent, error) {
	a, err := react.New(ctx, react.Config{
		ChatModel:    d.ChatModel,
		Tools:        d.Tools,
		SystemPrompt: d.SystemPrompt,
	})
	if err != nil {
		return nil, err
	}
	return a, nil
}
