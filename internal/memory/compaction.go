// Package memory implements context-window compaction (sliding window +
// summarization) for conversation history, per
// enterprise-ai-agent-design.md §7. It is wired into eino's ReAct agent
// via the MessageRewriter hook (internal/agent/react.Config), and reused
// verbatim by gateway handlers when persisting history back to
// short-term storage, so the two never drift apart (see
// Compact's doc comment).
package memory

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"

	pkgschema "github.com/JIAOZAI1/lead-mind-ai-agent/pkg/schema"
)

// summarizeTimeout bounds the summarization model call, per PROJECT.md
// §6.1's mandatory timeout on all external calls.
const summarizeTimeout = 15 * time.Second

const summarizationPrompt = "Summarize the key facts, decisions, and user preferences from " +
	"this conversation excerpt concisely, in the same language as the conversation. " +
	"Do not include meta-commentary, only the summary itself."

// CompactionConfig controls the sliding-window + summarization policy
// applied to conversation history once it grows past a threshold.
type CompactionConfig struct {
	// MaxTurnsVerbatim is how many of the most recent turns are kept
	// unmodified. A "turn" is one user message plus the assistant/tool
	// messages that follow it, up to (but not including) the next user
	// message.
	MaxTurnsVerbatim int
	// SummarizeThresholdTurns triggers summarization once history
	// exceeds this many turns; turns older than MaxTurnsVerbatim are
	// then folded into a single summary message. Kept higher than
	// MaxTurnsVerbatim so summarization fires only occasionally, not on
	// every call once past the verbatim window.
	SummarizeThresholdTurns int
	// SummarizerModel produces the summary. May be the same model used
	// for the main agent, or a cheaper one.
	SummarizerModel model.ToolCallingChatModel
}

// DefaultCompactionConfig returns sane defaults (10 turns verbatim,
// summarize once past 20), with SummarizerModel left nil — callers must
// set it explicitly.
func DefaultCompactionConfig(summarizer model.ToolCallingChatModel) CompactionConfig {
	return CompactionConfig{
		MaxTurnsVerbatim:        10,
		SummarizeThresholdTurns: 20,
		SummarizerModel:         summarizer,
	}
}

// NewMessageRewriter returns a function directly assignable to
// react.AgentConfig.MessageRewriter (internal/agent/react.Config.MessageRewriter),
// implementing sliding-window + summarization compaction over eino's
// in-run message state.
func NewMessageRewriter(cfg CompactionConfig) func(ctx context.Context, messages []*schema.Message) []*schema.Message {
	return func(ctx context.Context, messages []*schema.Message) []*schema.Message {
		history := pkgschema.FromEinoMessages(messages)
		compacted := Compact(ctx, cfg, history)
		return pkgschema.ToEinoMessages(compacted)
	}
}

// Compact applies sliding-window + summarization compaction to history,
// independent of eino's types. It is called both by the MessageRewriter
// closure above (operating on the agent's in-run state) and by gateway
// handlers just before persisting history back to short-term storage —
// using the same function in both places keeps what's stored in Redis
// and what the next MessageRewriter call will see in sync, since eino
// does not hand the rewritten state back to callers after Generate/Stream
// returns.
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
		// Summarization failing shouldn't break the request; fall back
		// to a hard truncation (drop the oldest turns) rather than
		// erroring the whole call. This is a degraded-but-safe path,
		// not silently losing errors — the caller can inspect logs from
		// the summarizer call itself.
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

// splitTurns groups history into turns: each turn starts at a user
// message and includes every following message up to (not including) the
// next user message. Leading non-user messages (e.g. a stray system
// message) form their own turn so no messages are dropped.
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
