package plugin_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/gateway"
	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/gateway/handler"
	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/plugin"
)

type scriptedAgent struct {
	decisions []plugin.Decision
	inputs    []plugin.AgentInput
}

func (a *scriptedAgent) NextAction(
	_ context.Context,
	input plugin.AgentInput,
) (plugin.Decision, error) {
	a.inputs = append(a.inputs, input)
	return a.decisions[len(a.inputs)-1], nil
}

func TestPluginAPI_CommandLoop(t *testing.T) {
	t.Parallel()

	agent := &scriptedAgent{decisions: []plugin.Decision{
		{
			Action: plugin.BrowserAction{
				Type: "OPEN_TAB",
				URL:  "https://www.google.com/search?q=solar+panel+Germany",
			},
			TimeoutMS: 30000,
		},
		{
			PageID: "page_1",
			Action: plugin.BrowserAction{
				Type:  "OBSERVE",
				Level: "STANDARD",
			},
			TimeoutMS: 30000,
		},
		{
			Action: plugin.BrowserAction{
				Type:    "COMPLETE",
				Summary: "已完成页面分析",
			},
		},
	}}
	service := plugin.NewService(plugin.NewMemoryStore(), agent)
	router := gateway.NewRouter(handler.AgentDeps{Plugin: plugin.NewHandler(service)})

	workflows := request(t, router, http.MethodGet, "/ai-agent/api/plugin/workflows", nil, "tenant-a", "user-a")
	if workflows.Code != http.StatusOK {
		t.Fatalf("list workflows status = %d, body = %s", workflows.Code, workflows.Body.String())
	}

	created := request(t, router, http.MethodPost, "/ai-agent/api/plugin/tasks", map[string]any{
		"workflowId": "google-lead-search",
		"inputs": map[string]any{
			"product":     "solar panel",
			"country":     "Germany",
			"resultLimit": 5,
		},
	}, "tenant-a", "user-a")
	if created.Code != http.StatusCreated {
		t.Fatalf("create task status = %d, body = %s", created.Code, created.Body.String())
	}
	var createResponse struct {
		TaskID string `json:"taskId"`
	}
	decode(t, created.Body, &createResponse)

	first := poll(t, router, createResponse.TaskID)
	if first.Command.Action.Type != "OPEN_TAB" || first.Command.Sequence != 1 {
		t.Fatalf("first command = %#v", first.Command)
	}
	submitResult(t, router, createResponse.TaskID, first.Command,
		json.RawMessage(`{"pageId":"page_1"}`))

	second := poll(t, router, createResponse.TaskID)
	if second.Command.Action.Type != "OBSERVE" ||
		second.Command.PageID != "page_1" ||
		second.Command.Sequence != 2 {
		t.Fatalf("second command = %#v", second.Command)
	}

	observationResponse := request(t, router, http.MethodPost,
		"/ai-agent/api/plugin/tasks/"+createResponse.TaskID+"/observations",
		map[string]any{
			"taskId":        createResponse.TaskID,
			"tenantCode":    "tenant-a",
			"pageId":        "page_1",
			"observationId": "obs_1",
			"pageVersion":   1,
			"level":         "STANDARD",
			"url":           "https://www.google.com/search?q=solar",
			"title":         "Google",
			"language":      "en",
			"collectedAt":   "2026-07-27T12:00:00Z",
			"textSummary":   "Search results",
			"truncated": map[string]bool{
				"text": false, "links": false, "elements": false, "html": false,
			},
		}, "tenant-a", "user-a")
	if observationResponse.Code != http.StatusNoContent {
		t.Fatalf("submit observation status = %d, body = %s",
			observationResponse.Code, observationResponse.Body.String())
	}
	submitResult(t, router, createResponse.TaskID, second.Command,
		json.RawMessage(`{"observationId":"obs_1","pageVersion":1}`))

	third := poll(t, router, createResponse.TaskID)
	if third.Command.Action.Type != "COMPLETE" || third.Command.Sequence != 3 {
		t.Fatalf("third command = %#v", third.Command)
	}
	if len(agent.inputs) != 3 {
		t.Fatalf("agent calls = %d, want 3", len(agent.inputs))
	}
	if len(agent.inputs[0].Steps) != 0 {
		t.Fatalf("first agent call received %d prior steps", len(agent.inputs[0].Steps))
	}
	if len(agent.inputs[1].Steps) != 1 ||
		agent.inputs[1].Steps[0].Command.Action.Type != "OPEN_TAB" {
		t.Fatalf("second agent call history = %#v", agent.inputs[1].Steps)
	}
	if len(agent.inputs[2].Steps) != 2 {
		t.Fatalf("third agent call received %d steps, want 2", len(agent.inputs[2].Steps))
	}
	observation, ok := agent.inputs[2].Observations["page_1"]
	if !ok || observation.ObservationID != "obs_1" {
		t.Fatalf("third agent call observations = %#v", agent.inputs[2].Observations)
	}

	submitResult(t, router, createResponse.TaskID, third.Command,
		json.RawMessage(`{"summary":"done"}`))
	submitResult(t, router, createResponse.TaskID, third.Command,
		json.RawMessage(`{"summary":"done"}`))

	done := poll(t, router, createResponse.TaskID)
	if done.Command != nil {
		t.Fatalf("completed task returned command %#v", done.Command)
	}
}

func TestPluginAPI_TenantIsolation(t *testing.T) {
	t.Parallel()

	service := plugin.NewService(plugin.NewMemoryStore(), &scriptedAgent{
		decisions: []plugin.Decision{{
			Action: plugin.BrowserAction{
				Type: "OPEN_TAB",
				URL:  "https://www.google.com/search?q=battery+Japan",
			},
		}},
	})
	router := gateway.NewRouter(handler.AgentDeps{Plugin: plugin.NewHandler(service)})
	created := request(t, router, http.MethodPost, "/ai-agent/api/plugin/tasks", map[string]any{
		"workflowId": "google-lead-search",
		"inputs": map[string]any{
			"product": "battery", "country": "Japan", "resultLimit": 1,
		},
	}, "tenant-a", "user-a")
	var response struct {
		TaskID string `json:"taskId"`
	}
	decode(t, created.Body, &response)

	otherTenant := request(t, router, http.MethodGet,
		"/ai-agent/api/plugin/tasks/"+response.TaskID+"/commands", nil, "tenant-b", "user-a")
	if otherTenant.Code != http.StatusNotFound {
		t.Fatalf("cross-tenant poll status = %d, want %d",
			otherTenant.Code, http.StatusNotFound)
	}
}

type pollResponse struct {
	Command      *plugin.AgentCommand `json:"command"`
	RetryAfterMS int                  `json:"retryAfterMs"`
}

func poll(t *testing.T, router http.Handler, taskID string) pollResponse {
	t.Helper()
	response := request(t, router, http.MethodGet,
		"/ai-agent/api/plugin/tasks/"+taskID+"/commands", nil, "tenant-a", "user-a")
	if response.Code != http.StatusOK {
		t.Fatalf("poll status = %d, body = %s", response.Code, response.Body.String())
	}
	var value pollResponse
	decode(t, response.Body, &value)
	return value
}

func submitResult(
	t *testing.T,
	router http.Handler,
	taskID string,
	command *plugin.AgentCommand,
	data json.RawMessage,
) {
	t.Helper()
	response := request(t, router, http.MethodPost,
		"/ai-agent/api/plugin/tasks/"+taskID+"/command-results",
		map[string]any{
			"commandId":  command.CommandID,
			"sequence":   command.Sequence,
			"ok":         true,
			"startedAt":  "2026-07-27T12:00:00Z",
			"finishedAt": "2026-07-27T12:00:01Z",
			"data":       data,
		}, "tenant-a", "user-a")
	if response.Code != http.StatusNoContent {
		t.Fatalf("submit result status = %d, body = %s", response.Code, response.Body.String())
	}
}

func request(
	t *testing.T,
	router http.Handler,
	method string,
	path string,
	body any,
	tenantCode string,
	userID string,
) *httptest.ResponseRecorder {
	t.Helper()
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		reader = bytes.NewReader(data)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("X-Tenant-Code", tenantCode)
	req.Header.Set("X-User-Id", userID)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	return recorder
}

func decode(t *testing.T, reader io.Reader, target any) {
	t.Helper()
	if err := json.NewDecoder(reader).Decode(target); err != nil {
		t.Fatal(err)
	}
}
