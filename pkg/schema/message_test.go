package schema

import (
	"testing"

	einoschema "github.com/cloudwego/eino/schema"
)

func TestRoundTrip_UserMessage(t *testing.T) {
	orig := einoschema.UserMessage("hello there")

	dto := FromEinoMessage(orig)
	if dto.Role != RoleUser || dto.Content != "hello there" {
		t.Fatalf("unexpected DTO: %+v", dto)
	}

	back := ToEinoMessage(dto)
	if back.Role != orig.Role || back.Content != orig.Content {
		t.Fatalf("round-trip mismatch: got %+v, want %+v", back, orig)
	}
}

func TestRoundTrip_AssistantMessageWithToolCalls(t *testing.T) {
	orig := einoschema.AssistantMessage("", []einoschema.ToolCall{
		{
			ID:   "call_123",
			Type: "function",
			Function: einoschema.FunctionCall{
				Name:      "current_time",
				Arguments: `{"timezone":"Asia/Shanghai"}`,
			},
		},
	})

	dto := FromEinoMessage(orig)
	if len(dto.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call, got %d", len(dto.ToolCalls))
	}
	if dto.ToolCalls[0].ID != "call_123" || dto.ToolCalls[0].FunctionName != "current_time" {
		t.Fatalf("unexpected tool call DTO: %+v", dto.ToolCalls[0])
	}

	back := ToEinoMessage(dto)
	if len(back.ToolCalls) != 1 {
		t.Fatalf("expected 1 tool call after round-trip, got %d", len(back.ToolCalls))
	}
	tc := back.ToolCalls[0]
	if tc.ID != "call_123" || tc.Function.Name != "current_time" || tc.Function.Arguments != `{"timezone":"Asia/Shanghai"}` {
		t.Fatalf("tool call round-trip mismatch: %+v", tc)
	}
}

func TestRoundTrip_ToolMessage(t *testing.T) {
	orig := einoschema.ToolMessage(`{"now":"2026-07-23T00:00:00Z"}`, "call_123")

	dto := FromEinoMessage(orig)
	if dto.Role != RoleTool || dto.ToolCallID != "call_123" {
		t.Fatalf("unexpected DTO: %+v", dto)
	}

	back := ToEinoMessage(dto)
	if back.ToolCallID != orig.ToolCallID || back.Content != orig.Content {
		t.Fatalf("round-trip mismatch: got %+v, want %+v", back, orig)
	}
}

func TestFromEinoMessages_SkipsNil(t *testing.T) {
	msgs := []*einoschema.Message{
		einoschema.UserMessage("a"),
		nil,
		einoschema.UserMessage("b"),
	}
	out := FromEinoMessages(msgs)
	if len(out) != 2 {
		t.Fatalf("expected 2 messages (nil skipped), got %d", len(out))
	}
}

func TestToEinoMessages_PreservesOrder(t *testing.T) {
	dtos := []Message{
		{Role: RoleUser, Content: "first"},
		{Role: RoleAssistant, Content: "second"},
	}
	out := ToEinoMessages(dtos)
	if len(out) != 2 || out[0].Content != "first" || out[1].Content != "second" {
		t.Fatalf("unexpected order/content: %+v", out)
	}
}
