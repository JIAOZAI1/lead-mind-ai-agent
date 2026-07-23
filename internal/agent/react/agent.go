// Package react 在 Eino 的 ReAct agent 流程之上封装本项目的默认配置。
// 参考模式见 enterprise-ai-agent-design.md §3.2，范围界定见
// PROJECT.md §5 阶段一（单个 ReAct agent，暂不做多 agent 编排）。
package react

import (
	"context"
	"fmt"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/compose"
	"github.com/cloudwego/eino/flow/agent/react"
	"github.com/cloudwego/eino/schema"
)

// defaultMaxStep 限制工具调用循环的步数，防止一个行为异常的工具或模型
// 无限循环下去。PROJECT.md §6.1 要求所有外部调用都必须有超时控制，这
// 里是针对 agent 步数预算的等价保护措施。参见
// enterprise-ai-agent-design.md §3.2：推荐取值范围是 8~15。
const defaultMaxStep = 12

// Config 配置平台默认的 ReAct agent。
type Config struct {
	// ChatModel 是驱动该 agent 的支持工具调用的模型（参见
	// internal/model/provider）。
	ChatModel model.ToolCallingChatModel
	// Tools 是本次运行 agent 可使用的工具集。调用方按租户组装
	// （内置工具 + 租户自定义工具）。
	Tools []tool.BaseTool
	// SystemPrompt 如果设置，会在每次调用时作为 system 消息前置插入。
	// 这是租户/Agent 级别的定制点（PROJECT.md §1.2）。
	SystemPrompt string
	// MaxStep 非零时会覆盖 defaultMaxStep。
	MaxStep int
	// MessageRewriter 如果设置，会在调用 ChatModel 之前应用到累积的
	// 消息历史上——这是本仓库的上下文窗口压缩钩子（参见
	// internal/memory.NewMessageRewriter）。它先于 SystemPrompt 的注入
	// 执行（Eino 会先调用 MessageRewriter 再调用 MessageModifier），
	// 因此它永远不需要专门处理"剥离 system 消息"这种特殊情况。
	MessageRewriter react.MessageModifier
}

// New 根据 cfg 构建一个 ReAct agent。
func New(ctx context.Context, cfg Config) (*react.Agent, error) {
	if cfg.ChatModel == nil {
		return nil, fmt.Errorf("react agent: ChatModel is required")
	}

	maxStep := defaultMaxStep
	if cfg.MaxStep > 0 {
		maxStep = cfg.MaxStep
	}

	agentCfg := &react.AgentConfig{
		ToolCallingModel: cfg.ChatModel,
		ToolsConfig: compose.ToolsNodeConfig{
			Tools: cfg.Tools,
		},
		MaxStep:         maxStep,
		MessageRewriter: cfg.MessageRewriter,
	}

	if cfg.SystemPrompt != "" {
		prompt := cfg.SystemPrompt
		agentCfg.MessageModifier = func(_ context.Context, input []*schema.Message) []*schema.Message {
			out := make([]*schema.Message, 0, len(input)+1)
			out = append(out, schema.SystemMessage(prompt))
			out = append(out, input...)
			return out
		}
	}

	agent, err := react.NewAgent(ctx, agentCfg)
	if err != nil {
		return nil, fmt.Errorf("build react agent: %w", err)
	}
	return agent, nil
}
