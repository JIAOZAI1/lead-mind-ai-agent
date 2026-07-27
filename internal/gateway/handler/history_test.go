package handler

import (
	"context"
	"errors"
	"testing"

	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/memory"
	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/memory/transcript"
	pkgschema "github.com/JIAOZAI1/lead-mind-ai-agent/pkg/schema"
)

type fakeShortTerm struct {
	history     []pkgschema.Message
	loadErr     error
	replaceErr  error
	replaced    []pkgschema.Message
	replaceCall int
}

func (f *fakeShortTerm) LoadHistory(context.Context, string, string) ([]pkgschema.Message, error) {
	if f.loadErr != nil {
		return nil, f.loadErr
	}
	return f.history, nil
}

func (f *fakeShortTerm) AppendTurns(context.Context, string, string, string, []pkgschema.Message) error {
	return nil
}

func (f *fakeShortTerm) ReplaceHistory(_ context.Context, _, _ string, turns []pkgschema.Message) error {
	f.replaceCall++
	if f.replaceErr != nil {
		return f.replaceErr
	}
	f.replaced = turns
	return nil
}

func (f *fakeShortTerm) Reset(context.Context, string, string) error { return nil }

type fakeTranscript struct {
	turns    []transcript.Turn
	listErr  error
	listCall int
}

func (f *fakeTranscript) AppendTurns(context.Context, string, string, string, []pkgschema.Message) error {
	return nil
}

func (f *fakeTranscript) ListTurns(context.Context, string, string) ([]transcript.Turn, error) {
	f.listCall++
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.turns, nil
}

func userTurn(content string) transcript.Turn {
	return transcript.Turn{Message: pkgschema.Message{Role: pkgschema.RoleUser, Content: content}}
}

func assistantTurn(content string) transcript.Turn {
	return transcript.Turn{Message: pkgschema.Message{Role: pkgschema.RoleAssistant, Content: content}}
}

func TestLoadHistory(t *testing.T) {
	shortTermHistory := []pkgschema.Message{
		{Role: pkgschema.RoleUser, Content: "cached question"},
		{Role: pkgschema.RoleAssistant, Content: "cached answer"},
	}
	archived := []transcript.Turn{
		userTurn("archived question"),
		assistantTurn("archived answer"),
	}

	tests := []struct {
		name string

		shortTerm  *fakeShortTerm
		transcript *fakeTranscript
		newSession bool

		wantContents  []string
		wantErr       bool
		wantListCalls int
		wantRewarm    bool
	}{
		{
			name:          "short-term hit skips transcript entirely",
			shortTerm:     &fakeShortTerm{history: shortTermHistory},
			transcript:    &fakeTranscript{turns: archived},
			wantContents:  []string{"cached question", "cached answer"},
			wantListCalls: 0,
		},
		{
			name:          "new session with empty history never touches transcript",
			shortTerm:     &fakeShortTerm{history: []pkgschema.Message{}},
			transcript:    &fakeTranscript{turns: archived},
			newSession:    true,
			wantContents:  nil,
			wantListCalls: 0,
		},
		{
			name:          "expired ttl rebuilds from transcript and re-warms cache",
			shortTerm:     &fakeShortTerm{history: []pkgschema.Message{}},
			transcript:    &fakeTranscript{turns: archived},
			wantContents:  []string{"archived question", "archived answer"},
			wantListCalls: 1,
			wantRewarm:    true,
		},
		{
			name:          "old session with genuinely empty transcript yields no context",
			shortTerm:     &fakeShortTerm{history: []pkgschema.Message{}},
			transcript:    &fakeTranscript{},
			wantContents:  nil,
			wantListCalls: 1,
		},
		{
			name:          "transcript failure degrades to empty context, not an error",
			shortTerm:     &fakeShortTerm{history: []pkgschema.Message{}},
			transcript:    &fakeTranscript{listErr: errors.New("mysql down")},
			wantContents:  nil,
			wantListCalls: 1,
		},
		{
			name:          "re-warm failure still returns the rebuilt context",
			shortTerm:     &fakeShortTerm{history: []pkgschema.Message{}, replaceErr: errors.New("redis down")},
			transcript:    &fakeTranscript{turns: archived},
			wantContents:  []string{"archived question", "archived answer"},
			wantListCalls: 1,
			wantRewarm:    true,
		},
		{
			name:       "short-term failure is a hard error",
			shortTerm:  &fakeShortTerm{loadErr: errors.New("redis down")},
			transcript: &fakeTranscript{turns: archived},
			wantErr:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deps := AgentDeps{
				ShortTerm:  tt.shortTerm,
				Transcript: tt.transcript,
				// 阈值取得足够大，让这些用例的短历史原样通过 Compact，
				// 把压缩行为本身留给 TestLoadHistoryCompactsRebuiltContext。
				Compaction: memory.CompactionConfig{MaxTurnsVerbatim: 10, SummarizeThresholdTurns: 20},
			}

			got, err := deps.loadHistory(context.Background(), "acme", "user-1", "session-1", tt.newSession)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected an error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			if len(got) != len(tt.wantContents) {
				t.Fatalf("got %d messages, want %d: %+v", len(got), len(tt.wantContents), got)
			}
			for i, want := range tt.wantContents {
				if got[i].Content != want {
					t.Errorf("message %d = %q, want %q", i, got[i].Content, want)
				}
			}

			if tt.transcript.listCall != tt.wantListCalls {
				t.Errorf("transcript.ListTurns called %d times, want %d", tt.transcript.listCall, tt.wantListCalls)
			}

			rewarmed := tt.shortTerm.replaceCall > 0
			if rewarmed != tt.wantRewarm {
				t.Errorf("short-term re-warm happened = %v, want %v", rewarmed, tt.wantRewarm)
			}
		})
	}
}

// TestLoadHistoryCompactsRebuiltContext 覆盖重建路径必须过 Compact 这个
// 关键约束：transcript 不做压缩，直接整段喂给模型会打爆上下文窗口。
func TestLoadHistoryCompactsRebuiltContext(t *testing.T) {
	var turns []transcript.Turn
	for i := 0; i < 40; i++ {
		turns = append(turns, userTurn("question"), assistantTurn("answer"))
	}

	shortTerm := &fakeShortTerm{history: []pkgschema.Message{}}
	deps := AgentDeps{
		ShortTerm:  shortTerm,
		Transcript: &fakeTranscript{turns: turns},
		// SummarizerModel 为 nil，摘要必然失败，Compact 会降级为硬截断，
		// 正好让本用例不依赖任何模型调用也能验证"确实压缩过了"。
		Compaction: memory.CompactionConfig{MaxTurnsVerbatim: 10, SummarizeThresholdTurns: 20},
	}

	got, err := deps.loadHistory(context.Background(), "acme", "user-1", "session-1", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(got) >= len(turns) {
		t.Fatalf("rebuilt context was not compacted: got %d messages from %d transcript turns", len(got), len(turns))
	}
	if len(shortTerm.replaced) != len(got) {
		t.Errorf("re-warmed cache holds %d messages, want the %d returned to the caller", len(shortTerm.replaced), len(got))
	}
}

// TestLoadHistoryNeverReturnsEmptyContextWhenTranscriptHasTurns 固定住
// 那条兜底逻辑：Compact 在配置退化（这里用零值 CompactionConfig 模拟）
// 时可能把历史截断到一条不剩，此时必须退回未压缩的原始消息，否则重建
// 路径就退化成了它本要修复的"模型失忆"。
func TestLoadHistoryNeverReturnsEmptyContextWhenTranscriptHasTurns(t *testing.T) {
	deps := AgentDeps{
		ShortTerm:  &fakeShortTerm{history: []pkgschema.Message{}},
		Transcript: &fakeTranscript{turns: []transcript.Turn{userTurn("q"), assistantTurn("a")}},
	}

	got, err := deps.loadHistory(context.Background(), "acme", "user-1", "session-1", false)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("rebuilt context is empty even though the transcript has turns")
	}
}
