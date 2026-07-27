package plugin

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

type recordingModel struct {
	input   []*schema.Message
	tools   []*schema.ToolInfo
	options *model.Options
}

func (m *recordingModel) Generate(
	_ context.Context,
	input []*schema.Message,
	opts ...model.Option,
) (*schema.Message, error) {
	m.input = input
	m.options = model.GetCommonOptions(nil, opts...)
	return schema.AssistantMessage("", []schema.ToolCall{{
		ID:   "model_call_2",
		Type: "function",
		Function: schema.FunctionCall{
			Name: _actionToolName,
			Arguments: `{
				"type":"CLICK",
				"pageId":"page_1",
				"observationId":"obs_2",
				"elementRef":"el_7",
				"timeoutMs":10000,
				"memory":"已打开 Google；待分析 Example Solar"
			}`,
		},
	}}), nil
}

func (m *recordingModel) Stream(
	_ context.Context,
	_ []*schema.Message,
	_ ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	return nil, nil
}

func (m *recordingModel) WithTools(
	tools []*schema.ToolInfo,
) (model.ToolCallingChatModel, error) {
	m.tools = tools
	return m, nil
}

func TestModelBrowserAgent_UsesGoalHistoryAndHTML(t *testing.T) {
	t.Parallel()

	chatModel := &recordingModel{}
	agent := NewModelBrowserAgent(chatModel)
	decision, err := agent.NextAction(context.Background(), AgentInput{
		TaskID: "task_1",
		Workflow: Workflow{
			WorkflowID:  "google-lead-search",
			Name:        "Google 搜索获客",
			Description: "搜索并分析潜在客户",
		},
		Inputs: map[string]any{
			"product": "solar panel",
			"country": "Germany",
		},
		Steps: []AgentStep{{
			Command: AgentCommand{
				CommandID: "cmd_1",
				Action: BrowserAction{
					Type: "OPEN_TAB",
					URL:  "https://www.google.com/search?q=solar",
				},
			},
			Result: CommandResult{
				CommandID: "cmd_1",
				Sequence:  1,
				OK:        true,
				Data:      []byte(`{"pageId":"page_1"}`),
			},
		}},
		Observations: map[string]Observation{
			"page_1": {
				PageID:        "page_1",
				ObservationID: "obs_2",
				Level:         "FULL",
				URL:           "https://www.google.com/search?q=solar",
				Title:         "Google",
				TextSummary:   "Search results",
				HTML:          `<a href="https://example.com">Example Solar</a>`,
			},
		},
		LatestObservationID: "obs_2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if decision.Action.Type != "CLICK" ||
		decision.PageID != "page_1" ||
		decision.ObservationID != "obs_2" ||
		decision.Memory == "" {
		t.Fatalf("decision = %#v", decision)
	}
	if len(chatModel.tools) != 1 || chatModel.tools[0].Name != _actionToolName {
		t.Fatalf("bound tools = %#v", chatModel.tools)
	}
	if chatModel.options.ToolChoice != nil {
		t.Fatalf("tool choice = %q, want provider default", *chatModel.options.ToolChoice)
	}
	if chatModel.options.Temperature == nil || *chatModel.options.Temperature != 0 {
		t.Fatalf("temperature = %#v, want 0", chatModel.options.Temperature)
	}
	if len(chatModel.input) != 5 {
		t.Fatalf("model messages = %d, want 5", len(chatModel.input))
	}
	if len(chatModel.input[2].ToolCalls) != 1 ||
		chatModel.input[2].ToolCalls[0].ID != "cmd_1" {
		t.Fatalf("history tool call = %#v", chatModel.input[2])
	}
	if chatModel.input[3].ToolCallID != "cmd_1" {
		t.Fatalf("history tool result = %#v", chatModel.input[3])
	}
	if !strings.Contains(chatModel.input[4].Content, "Example Solar") {
		t.Fatalf("current page state does not contain HTML: %s", chatModel.input[4].Content)
	}
}
