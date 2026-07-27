package plugin

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/schema"
)

func TestModelPageAnalyzer_ReturnsOnlyLeadWithContact(t *testing.T) {
	t.Parallel()

	analyzerModel := &queuedModel{responses: []*schema.Message{
		schema.AssistantMessage("", []schema.ToolCall{{
			ID:   "report_1",
			Type: "function",
			Function: schema.FunctionCall{
				Name: _reportLeadToolName,
				Arguments: `{
					"companyName":"Example Solar",
					"website":"https://example.com",
					"evidence":"官网说明提供太阳能组件",
					"contact":"sales@example.com"
				}`,
			},
		}}),
	}}
	analyzer := NewModelPageAnalyzer(analyzerModel)
	lead, err := analyzer.Analyze(context.Background(), PageAnalysisInput{
		Workflow: Workflow{Name: "Google 搜索获客"},
		Inputs: map[string]any{
			"product": "solar panel",
			"country": "Germany",
		},
		Observation: Observation{
			PageID:        "page_1",
			ObservationID: "obs_1",
			URL:           "https://example.com/contact",
			Title:         "Example Solar",
			Text:          "Solar modules. Email sales@example.com",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if lead == nil ||
		lead.Contact != "sales@example.com" ||
		lead.SourceURL != "https://example.com/contact" {
		t.Fatalf("lead = %#v", lead)
	}
}

func TestModelPageAnalyzer_NoContactReturnsNothing(t *testing.T) {
	t.Parallel()

	t.Run("no tool call", func(t *testing.T) {
		analyzer := NewModelPageAnalyzer(&queuedModel{
			responses: []*schema.Message{schema.AssistantMessage("", nil)},
		})
		lead, err := analyzer.Analyze(context.Background(), PageAnalysisInput{
			Observation: Observation{URL: "https://example.com"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if lead != nil {
			t.Fatalf("lead = %#v, want nil", lead)
		}
	})

	t.Run("tool result without contact", func(t *testing.T) {
		analyzer := NewModelPageAnalyzer(&queuedModel{
			responses: []*schema.Message{
				schema.AssistantMessage("", []schema.ToolCall{{
					ID:   "report_1",
					Type: "function",
					Function: schema.FunctionCall{
						Name: _reportLeadToolName,
						Arguments: `{
							"companyName":"Example Solar",
							"website":"https://example.com",
							"evidence":"官网说明提供太阳能组件",
							"contact":""
						}`,
					},
				}}),
			},
		})
		lead, err := analyzer.Analyze(context.Background(), PageAnalysisInput{
			Observation: Observation{URL: "https://example.com"},
		})
		if err != nil {
			t.Fatal(err)
		}
		if lead != nil {
			t.Fatalf("lead = %#v, want nil", lead)
		}
	})
}
