// Package memory 实现对话历史的上下文窗口压缩（滑动窗口 + 摘要），
// 依据 enterprise-ai-agent-design.md §7。它通过 MessageRewriter 钩子
// 接入 eino 的 ReAct agent（internal/agent/react.Config），网关 handler
// 在把历史写回短期存储时也复用同一份逻辑，确保两处永远不会产生偏差
// （参见 Compact 的文档注释）。
package memory

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	pkgschema "github.com/JIAOZAI1/lead-mind-ai-agent/pkg/schema"
)

// summarizeTimeout 限制摘要模型调用的最长耗时，依据 PROJECT.md §6.1
// 对所有外部调用强制要求超时控制。
const summarizeTimeout = 15 * time.Second

const summarizationPrompt = "Summarize the key facts, decisions, and user preferences from " +
	"this conversation excerpt concisely, in the same language as the conversation. " +
	"Do not include meta-commentary, only the summary itself."

// CompactionConfig 控制对话历史超过阈值后应用的滑动窗口 + 摘要策略。
type CompactionConfig struct {
	// MaxTurnsVerbatim 是最近多少轮对话会被原样保留、不做任何修改。
	// 一"轮"（turn）指一条用户消息，加上其后紧跟的 assistant/tool
	// 消息，直到（但不包括）下一条用户消息为止。
	MaxTurnsVerbatim int
	// SummarizeThresholdTurns 是触发摘要的轮数阈值：一旦历史轮数超过
	// 该值，早于 MaxTurnsVerbatim 的那些轮次就会被折叠成一条摘要消息。
	// 该值应设置得比 MaxTurnsVerbatim 更大，这样摘要只会偶尔触发，
	// 而不是一旦超过保留窗口就每次调用都触发。
	SummarizeThresholdTurns int
	// SummarizerModel 用于生成摘要。可以是主 agent 使用的同一个模型，
	// 也可以是成本更低的模型。
	SummarizerModel model.ToolCallingChatModel
}

// DefaultCompactionConfig 返回一组合理的默认值（保留最近 10 轮，超过
// 20 轮后触发摘要），SummarizerModel 留空为 nil——调用方必须显式设置。
func DefaultCompactionConfig(summarizer model.ToolCallingChatModel) CompactionConfig {
	return CompactionConfig{
		MaxTurnsVerbatim:        10,
		SummarizeThresholdTurns: 20,
		SummarizerModel:         summarizer,
	}
}

// NewMessageRewriter 返回一个可直接赋值给
// react.AgentConfig.MessageRewriter（internal/agent/react.Config.MessageRewriter）
// 的函数，对 eino 运行期内的消息状态实施滑动窗口 + 摘要压缩。
func NewMessageRewriter(cfg CompactionConfig) func(ctx context.Context, messages []*schema.Message) []*schema.Message {
	return func(ctx context.Context, messages []*schema.Message) []*schema.Message {
		history := pkgschema.FromEinoMessages(messages)
		compacted := Compact(ctx, cfg, history)
		return pkgschema.ToEinoMessages(compacted)
	}
}

// Compact 对 history 应用滑动窗口 + 摘要压缩，不依赖 eino 的具体类型。
// 它既被上面的 MessageRewriter 闭包调用（作用于 agent 运行期内的状态），
// 也在网关 handler 把历史写回短期存储之前被直接调用——两处复用同一个
// 函数，能保证 Redis 中存储的内容与下一次 MessageRewriter 调用看到的
// 内容保持一致，因为 eino 在 Generate/Stream 返回后并不会把改写后的
// 状态回传给调用方。
func Compact(ctx context.Context, cfg CompactionConfig, history []pkgschema.Message) []pkgschema.Message {
	turns := splitTurns(history)
	if len(turns) <= cfg.SummarizeThresholdTurns {
		return history
	}

	keepFrom := len(turns) - cfg.MaxTurnsVerbatim
	if keepFrom < 0 {
		keepFrom = 0
	}
	older, recent := turns[:keepFrom], turns[keepFrom:]

	summary, err := summarize(ctx, cfg.SummarizerModel, older)
	if err != nil {
		// 摘要失败不应导致整个请求失败；这里降级为硬截断（直接丢弃
		// 最早的若干轮），而不是让整个调用报错。这是一条"降级但安全"
		// 的路径，因此仍必须记录日志——如果静默降级，对话历史会在
		// 不知不觉中变短，且没有任何痕迹可查。
		slog.WarnContext(ctx, "compaction: summarization failed, falling back to hard truncation",
			"error", err,
			"dropped_turns", len(older),
		)
		out := make([]pkgschema.Message, 0)
		for _, t := range recent {
			out = append(out, t...)
		}
		return out
	}

	out := []pkgschema.Message{{Role: pkgschema.RoleSystem, Content: "Earlier conversation summary: " + summary}}
	for _, t := range recent {
		out = append(out, t...)
	}
	return out
}

// splitTurns 将 history 按轮次分组：每一轮从一条用户消息开始，包含其后
// 所有消息，直到（不包括）下一条用户消息为止。开头的非用户消息（例如
// 一条孤立的 system 消息）会自成一轮，确保不会有消息被遗漏。
func splitTurns(history []pkgschema.Message) [][]pkgschema.Message {
	var turns [][]pkgschema.Message
	for _, m := range history {
		if m.Role == pkgschema.RoleUser || len(turns) == 0 {
			turns = append(turns, []pkgschema.Message{m})
			continue
		}
		turns[len(turns)-1] = append(turns[len(turns)-1], m)
	}
	return turns
}

func summarize(ctx context.Context, summarizer model.ToolCallingChatModel, turns [][]pkgschema.Message) (string, error) {
	if summarizer == nil {
		return "", fmt.Errorf("compaction: no summarizer model configured")
	}

	var sb strings.Builder
	for _, turn := range turns {
		for _, m := range turn {
			sb.WriteString(string(m.Role))
			sb.WriteString(": ")
			sb.WriteString(m.Content)
			sb.WriteString("\n")
		}
	}

	ctx, cancel := context.WithTimeout(ctx, summarizeTimeout)
	defer cancel()

	reply, err := summarizer.Generate(ctx, []*schema.Message{
		schema.SystemMessage(summarizationPrompt),
		schema.UserMessage(sb.String()),
	})
	if err != nil {
		return "", fmt.Errorf("summarize older turns: %w", err)
	}
	return reply.Content, nil
}
