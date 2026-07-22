// Package schema holds cross-layer public types. Per PROJECT.md §6.1, this
// is the only package internal layers may all depend on. Message is a
// storage-oriented, JSON-stable mirror of eino/schema.Message's subset
// needed to persist conversation turns (internal/memory,
// internal/session), so memory/session code doesn't need to import
// cloudwego/eino/schema directly.
package schema

import einoschema "github.com/cloudwego/eino/schema"

// Role mirrors eino/schema.RoleType's values.
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// ToolCall mirrors eino/schema.ToolCall (function-call subset only; Index
// is stream-merging state that has no meaning once persisted).
type ToolCall struct {
	ID           string `json:"id"`
	FunctionName string `json:"function_name"`
	Arguments    string `json:"arguments"`
}

// Message is the persisted form of one conversation turn's message. It
// intentionally omits eino/schema.Message fields that are
// stream/multimodal-specific (MultiContent, ResponseMeta, etc.) and not
// needed to replay a conversation's text/tool-call history.
type Message struct {
	Role       Role       `json:"role"`
	Content    string     `json:"content"`
	Name       string     `json:"name,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolName   string     `json:"tool_name,omitempty"`
}

// FromEinoMessage converts an eino/schema.Message into the persisted DTO.
func FromEinoMessage(m *einoschema.Message) Message {
	out := Message{
		Role:       Role(m.Role),
		Content:    m.Content,
		Name:       m.Name,
		ToolCallID: m.ToolCallID,
		ToolName:   m.ToolName,
	}
	if len(m.ToolCalls) > 0 {
		out.ToolCalls = make([]ToolCall, len(m.ToolCalls))
		for i, tc := range m.ToolCalls {
			out.ToolCalls[i] = ToolCall{
				ID:           tc.ID,
				FunctionName: tc.Function.Name,
				Arguments:    tc.Function.Arguments,
			}
		}
	}
	return out
}

// FromEinoMessages converts a slice, skipping nil entries.
func FromEinoMessages(msgs []*einoschema.Message) []Message {
	out := make([]Message, 0, len(msgs))
	for _, m := range msgs {
		if m == nil {
			continue
		}
		out = append(out, FromEinoMessage(m))
	}
	return out
}

// ToEinoMessage converts the persisted DTO back into an eino/schema.Message
// suitable for feeding into react.Agent.Generate/Stream as input history.
func ToEinoMessage(m Message) *einoschema.Message {
	out := &einoschema.Message{
		Role:       einoschema.RoleType(m.Role),
		Content:    m.Content,
		Name:       m.Name,
		ToolCallID: m.ToolCallID,
		ToolName:   m.ToolName,
	}
	if len(m.ToolCalls) > 0 {
		out.ToolCalls = make([]einoschema.ToolCall, len(m.ToolCalls))
		for i, tc := range m.ToolCalls {
			out.ToolCalls[i] = einoschema.ToolCall{
				ID:   tc.ID,
				Type: "function",
				Function: einoschema.FunctionCall{
					Name:      tc.FunctionName,
					Arguments: tc.Arguments,
				},
			}
		}
	}
	return out
}

// ToEinoMessages converts a slice back into eino/schema.Message pointers.
func ToEinoMessages(msgs []Message) []*einoschema.Message {
	out := make([]*einoschema.Message, len(msgs))
	for i, m := range msgs {
		out[i] = ToEinoMessage(m)
	}
	return out
}
