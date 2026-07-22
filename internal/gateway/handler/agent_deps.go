package handler

import (
	"context"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	einoreact "github.com/cloudwego/eino/flow/agent/react"

	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/agent/react"
	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/memory"
	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/memory/longterm"
	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/memory/shortterm"
	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/session"
)

// AgentDeps holds the shared, expensive-to-build resources the chat
// handlers need to construct a per-request ReAct agent and to read/write
// conversation memory: the ChatModel connection, the tool set, and the
// session/short-term/long-term memory stores. It is built once at
// startup (see cmd/server/main.go) and injected into the handlers.
//
// A new react.Agent is built per request rather than shared, since
// AgentConfig (system prompt, tool set) is expected to become
// per-tenant once tenant-configured agents land (PROJECT.md §1.2);
// react.NewAgent itself is cheap (graph construction, no I/O).
type AgentDeps struct {
	ChatModel    einomodel.ToolCallingChatModel
	Tools        []tool.BaseTool
	SystemPrompt string

	Sessions   session.Store
	ShortTerm  shortterm.Store
	LongTerm   longterm.Store
	Compaction memory.CompactionConfig
}

func (d AgentDeps) newAgent(ctx context.Context) (*einoreact.Agent, error) {
	a, err := react.New(ctx, react.Config{
		ChatModel:       d.ChatModel,
		Tools:           d.Tools,
		SystemPrompt:    d.SystemPrompt,
		MessageRewriter: memory.NewMessageRewriter(d.Compaction),
	})
	if err != nil {
		return nil, err
	}
	return a, nil
}
