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
	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/memory/transcript"
	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/session"
)

// AgentDeps 保存 chat handler 构建单次请求 ReAct agent、以及读写对话
// 记忆所需的、构建成本较高的共享资源：ChatModel 连接、工具集，以及
// 会话/短期/长期记忆的 store。它在启动时构建一次（参见
// cmd/server/main.go），并注入到各个 handler 中。
//
// react.Agent 按请求单独构建而不是共享，是因为 AgentConfig（system
// prompt、工具集）预计会在租户自定义 agent 落地后变为按租户区分
// （PROJECT.md §1.2）；而 react.NewAgent 本身构建成本很低（只是图结构
// 构建，不涉及 I/O）。
type AgentDeps struct {
	ChatModel    einomodel.ToolCallingChatModel
	Tools        []tool.BaseTool
	SystemPrompt string

	Sessions   session.Store
	ShortTerm  shortterm.Store
	LongTerm   longterm.Store
	Transcript transcript.Store
	Compaction memory.CompactionConfig
}

func (d AgentDeps) newAgent(ctx context.Context) (*einoreact.Agent, error) {
	agent, err := react.New(ctx, react.Config{
		ChatModel:       d.ChatModel,
		Tools:           d.Tools,
		SystemPrompt:    d.SystemPrompt,
		MessageRewriter: memory.NewMessageRewriter(d.Compaction),
	})
	if err != nil {
		return nil, err
	}
	return agent, nil
}
