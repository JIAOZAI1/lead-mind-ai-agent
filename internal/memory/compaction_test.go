package memory

import (
	"context"
	"testing"

	pkgschema "github.com/JIAOZAI1/lead-mind-ai-agent/pkg/schema"
)

func userTurn(content string) []pkgschema.Message {
	return []pkgschema.Message{{Role: pkgschema.RoleUser, Content: content}}
}

func buildHistory(nTurns int) []pkgschema.Message {
	var out []pkgschema.Message
	for i := 0; i < nTurns; i++ {
		out = append(out, pkgschema.Message{Role: pkgschema.RoleUser, Content: "user turn"})
		out = append(out, pkgschema.Message{Role: pkgschema.RoleAssistant, Content: "assistant reply"})
	}
	return out
}

func TestSplitTurns_GroupsByUserMessage(t *testing.T) {
	history := []pkgschema.Message{
		{Role: pkgschema.RoleUser, Content: "hi"},
		{Role: pkgschema.RoleAssistant, Content: "", ToolCalls: []pkgschema.ToolCall{{ID: "1", FunctionName: "f"}}},
		{Role: pkgschema.RoleTool, Content: "result", ToolCallID: "1"},
		{Role: pkgschema.RoleAssistant, Content: "final reply"},
		{Role: pkgschema.RoleUser, Content: "second question"},
		{Role: pkgschema.RoleAssistant, Content: "second reply"},
	}

	turns := splitTurns(history)
	if len(turns) != 2 {
		t.Fatalf("expected 2 turns, got %d", len(turns))
	}
	if len(turns[0]) != 4 {
		t.Fatalf("expected first turn to have 4 messages (user+tool call+tool result+reply), got %d", len(turns[0]))
	}
	if len(turns[1]) != 2 {
		t.Fatalf("expected second turn to have 2 messages, got %d", len(turns[1]))
	}
}

func TestCompact_BelowThreshold_ReturnsUnchanged(t *testing.T) {
	cfg := CompactionConfig{MaxTurnsVerbatim: 10, SummarizeThresholdTurns: 20}
	history := buildHistory(5)

	out := Compact(context.Background(), cfg, history)

	if len(out) != len(history) {
		t.Fatalf("expected history untouched below threshold, got %d messages, want %d", len(out), len(history))
	}
}

func TestCompact_AboveThreshold_WithoutSummarizer_FallsBackToTruncation(t *testing.T) {
	cfg := CompactionConfig{MaxTurnsVerbatim: 3, SummarizeThresholdTurns: 5, SummarizerModel: nil}
	history := buildHistory(10) // 10 turns, well above threshold

	out := Compact(context.Background(), cfg, history)

	// No summarizer configured -> summarize() errors -> fallback keeps
	// only the most recent MaxTurnsVerbatim turns, no summary message
	// prepended.
	wantMessages := cfg.MaxTurnsVerbatim * 2 // 2 messages per turn (user+assistant)
	if len(out) != wantMessages {
		t.Fatalf("expected fallback truncation to keep %d messages, got %d", wantMessages, len(out))
	}
	if out[0].Role == pkgschema.RoleSystem {
		t.Fatal("fallback path should not prepend a summary system message")
	}
}

func TestCompact_EmptyHistory(t *testing.T) {
	cfg := CompactionConfig{MaxTurnsVerbatim: 10, SummarizeThresholdTurns: 20}
	out := Compact(context.Background(), cfg, nil)
	if len(out) != 0 {
		t.Fatalf("expected empty output for empty input, got %d messages", len(out))
	}
}
