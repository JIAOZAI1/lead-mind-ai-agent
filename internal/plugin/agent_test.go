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
				"memory":"已打开 Google；待调用页面分析从 Agent"
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
	if len(decision.Leads) != 0 {
		t.Fatalf("decision leads = %#v", decision.Leads)
	}
	if len(chatModel.tools) != 2 ||
		chatModel.tools[0].Name != _actionToolName ||
		chatModel.tools[1].Name != _analyzePageToolName {
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

type fakePageAnalyzer struct {
	lead  *Lead
	input PageAnalysisInput
	calls int
}

func (a *fakePageAnalyzer) Analyze(
	_ context.Context,
	input PageAnalysisInput,
) (*Lead, error) {
	a.calls++
	a.input = input
	return a.lead, nil
}

type queuedModel struct {
	responses []*schema.Message
	inputs    [][]*schema.Message
}

func (m *queuedModel) Generate(
	_ context.Context,
	input []*schema.Message,
	_ ...model.Option,
) (*schema.Message, error) {
	m.inputs = append(m.inputs, append([]*schema.Message(nil), input...))
	response := m.responses[0]
	m.responses = m.responses[1:]
	return response, nil
}

func (m *queuedModel) Stream(
	_ context.Context,
	_ []*schema.Message,
	_ ...model.Option,
) (*schema.StreamReader[*schema.Message], error) {
	return nil, nil
}

func (m *queuedModel) WithTools(
	_ []*schema.ToolInfo,
) (model.ToolCallingChatModel, error) {
	return m, nil
}

func TestModelBrowserAgent_InvokesPageAnalyzerAsTool(t *testing.T) {
	t.Parallel()

	mainModel := &queuedModel{responses: []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "analyze_1",
			Type: "function",
			Function: schema.FunctionCall{
				Name: _analyzePageToolName,
				Arguments: `{
					"pageId":"page_1",
					"observationId":"obs_1"
				}`,
			},
		}}),
		schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "action_1",
			Type: "function",
			Function: schema.FunctionCall{
				Name: _actionToolName,
				Arguments: `{
					"type":"NAVIGATE",
					"pageId":"page_1",
					"url":"https://example.org",
					"memory":"已分析 Example Solar，继续下一个候选"
				}`,
			},
		}}),
	}}
	pageAnalyzer := &fakePageAnalyzer{lead: &Lead{
		CompanyName: "Example Solar",
		Website:     "https://example.com",
		SourceURL:   "https://example.com/about",
		Evidence:    "官网说明提供太阳能组件。",
		Contact:     "sales@example.com",
	}}
	agent := NewModelBrowserAgentWithPageAnalyzer(mainModel, pageAnalyzer)
	decision, err := agent.NextAction(context.Background(), AgentInput{
		TaskID: "task_1",
		Workflow: Workflow{
			WorkflowID: "google-lead-search",
			Name:       "Google 搜索获客",
		},
		Inputs: map[string]any{
			"product": "solar panel", "country": "Germany", "resultLimit": float64(2),
		},
		Observations: map[string]Observation{
			"page_1": {
				PageID:        "page_1",
				ObservationID: "obs_1",
				URL:           "https://example.com/about",
				Title:         "Example Solar",
				Level:         "STANDARD",
				TextSummary:   "Solar panels; contact sales@example.com",
			},
		},
		LatestObservationID: "obs_1",
	})
	if err != nil {
		t.Fatal(err)
	}
	if pageAnalyzer.calls != 1 ||
		pageAnalyzer.input.Observation.ObservationID != "obs_1" {
		t.Fatalf("page analyzer input = %#v, calls = %d", pageAnalyzer.input, pageAnalyzer.calls)
	}
	if len(decision.Leads) != 1 ||
		decision.Leads[0].Contact != "sales@example.com" {
		t.Fatalf("decision leads = %#v", decision.Leads)
	}
	if decision.Action.Type != "NAVIGATE" {
		t.Fatalf("decision = %#v", decision)
	}
	if len(mainModel.inputs) != 2 {
		t.Fatalf("main model calls = %d", len(mainModel.inputs))
	}
	lastMessages := mainModel.inputs[1]
	lastMessage := lastMessages[len(lastMessages)-1]
	if lastMessage.Role != schema.Tool ||
		lastMessage.ToolCallID != "analyze_1" ||
		!strings.Contains(lastMessage.Content, `"found":true`) {
		t.Fatalf("analyzer tool result = %#v", lastMessage)
	}
}

func TestModelBrowserAgent_PageAnalyzerEmptyResultAddsNoLead(t *testing.T) {
	t.Parallel()

	mainModel := &queuedModel{responses: []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "analyze_1",
			Type: "function",
			Function: schema.FunctionCall{
				Name:      _analyzePageToolName,
				Arguments: `{"pageId":"page_1","observationId":"obs_1"}`,
			},
		}}),
		schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "action_1",
			Type: "function",
			Function: schema.FunctionCall{
				Name: _actionToolName,
				Arguments: `{
					"type":"GO_BACK",
					"pageId":"page_1",
					"memory":"页面没有公开联系方式，继续下一个流程"
				}`,
			},
		}}),
	}}
	agent := NewModelBrowserAgentWithPageAnalyzer(
		mainModel,
		&fakePageAnalyzer{},
	)
	decision, err := agent.NextAction(context.Background(), AgentInput{
		TaskID:   "task_1",
		Workflow: Workflow{WorkflowID: "google-lead-search"},
		Observations: map[string]Observation{
			"page_1": {
				PageID:        "page_1",
				ObservationID: "obs_1",
				URL:           "https://example.com",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(decision.Leads) != 0 || decision.Action.Type != "GO_BACK" {
		t.Fatalf("decision = %#v", decision)
	}
	lastMessages := mainModel.inputs[1]
	if !strings.Contains(lastMessages[len(lastMessages)-1].Content, `"found":false`) {
		t.Fatalf("empty analyzer result = %#v", lastMessages[len(lastMessages)-1])
	}
}
