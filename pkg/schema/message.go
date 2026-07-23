// Package schema 保存跨层的公共类型。根据 PROJECT.md §6.1，这是唯一一个
// 允许被所有内部分层依赖的包。Message 是面向存储、JSON 结构稳定的
// eino/schema.Message 子集镜像，专为持久化对话轮次
// （internal/memory、internal/session）而设计，这样 memory/session 的
// 代码就不需要直接依赖 cloudwego/eino/schema。
package schema

import einoschema "github.com/cloudwego/eino/schema"

// Role 镜像 eino/schema.RoleType 的取值。
type Role string

const (
	RoleSystem    Role = "system"
	RoleUser      Role = "user"
	RoleAssistant Role = "assistant"
	RoleTool      Role = "tool"
)

// ToolCall 镜像 eino/schema.ToolCall（仅保留 function-call 相关子集；
// Index 是流式合并过程中的状态，一旦持久化便不再有意义）。
type ToolCall struct {
	ID           string `json:"id"`
	FunctionName string `json:"function_name"`
	Arguments    string `json:"arguments"`
}

// Message 是一条对话轮次消息的持久化形式。它刻意省略了
// eino/schema.Message 中与流式/多模态相关的字段（MultiContent、
// ResponseMeta 等），因为回放对话的文本/工具调用历史并不需要这些字段。
type Message struct {
	Role       Role       `json:"role"`
	Content    string     `json:"content"`
	Name       string     `json:"name,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallID string     `json:"tool_call_id,omitempty"`
	ToolName   string     `json:"tool_name,omitempty"`
}

// FromEinoMessage 将 eino/schema.Message 转换为持久化用的 DTO。
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

// FromEinoMessages 转换一个切片，跳过其中的 nil 元素。
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

// ToEinoMessage 将持久化 DTO 转换回 eino/schema.Message，可直接作为输入
// 历史传给 react.Agent.Generate/Stream。
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

// ToEinoMessages 将一个切片转换回 eino/schema.Message 指针切片。
func ToEinoMessages(msgs []Message) []*einoschema.Message {
	out := make([]*einoschema.Message, len(msgs))
	for i, m := range msgs {
		out[i] = ToEinoMessage(m)
	}
	return out
}
