// Package react wraps Eino's ReAct agent flow with this project's
// defaults. See enterprise-ai-agent-design.md §3.2 for the reference
// pattern and PROJECT.md §5 阶段一 for scope (single ReAct agent, no
// multi-agent orchestration yet).
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

// defaultMaxStep bounds the tool-call loop so a misbehaving tool or model
// can't spin forever. PROJECT.md §6.1 requires timeouts on all external
// calls; this is the equivalent guard for the agent's step budget.
// See enterprise-ai-agent-design.md §3.2: recommended range is 8~15.
const defaultMaxStep = 12

// Config configures the platform's default ReAct agent.
type Config struct {
	// ChatModel is the tool-calling model backing the agent (see
	// internal/model/provider).
	ChatModel model.ToolCallingChatModel
	// Tools are the tools available to the agent for this run. Callers
	// assemble this per-tenant (builtin + tenant-configured tools).
	Tools []tool.BaseTool
	// SystemPrompt, if set, is prepended as a system message on every
	// call. Tenant/Agent-level customization point (PROJECT.md §1.2).
	SystemPrompt string
	// MaxStep overrides defaultMaxStep when non-zero.
	MaxStep int
}

// New builds a ReAct agent from cfg.
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
		MaxStep: maxStep,
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

	a, err := react.NewAgent(ctx, agentCfg)
	if err != nil {
		return nil, fmt.Errorf("build react agent: %w", err)
	}
	return a, nil
}
