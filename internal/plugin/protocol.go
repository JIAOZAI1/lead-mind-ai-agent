// Package plugin 实现 Chrome 插件通过 HTTP 轮询驱动云端 Agent 的协议。
package plugin

import (
	"encoding/json"
	"fmt"
	"math"
	"net/url"
	"strings"
	"time"
)

const (
	ProtocolVersion = "browser-agent/v1"
	defaultRetryMS  = 3000
)

type InputProperty struct {
	Type        string `json:"type"`
	Title       string `json:"title"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required,omitempty"`
	Default     any    `json:"default,omitempty"`
}

type InputSchema struct {
	Properties map[string]InputProperty `json:"properties"`
}

type Workflow struct {
	WorkflowID   string      `json:"workflowId"`
	Name         string      `json:"name"`
	Description  string      `json:"description"`
	Icon         string      `json:"icon,omitempty"`
	Category     string      `json:"category"`
	Enabled      bool        `json:"enabled"`
	Status       string      `json:"status,omitempty"`
	Version      string      `json:"version"`
	InputSchema  InputSchema `json:"inputSchema"`
	Capabilities []string    `json:"capabilities"`
	AgentPrompt  string      `json:"-"`
}

type BrowserAction struct {
	Type       string `json:"type"`
	URL        string `json:"url,omitempty"`
	ElementRef string `json:"elementRef,omitempty"`
	Text       string `json:"text,omitempty"`
	Key        string `json:"key,omitempty"`
	Value      string `json:"value,omitempty"`
	X          *int   `json:"x,omitempty"`
	Y          *int   `json:"y,omitempty"`
	DurationMS *int   `json:"durationMs,omitempty"`
	Condition  string `json:"condition,omitempty"`
	Level      string `json:"level,omitempty"`
	Summary    string `json:"summary,omitempty"`
}

type AgentCommand struct {
	ProtocolVersion string        `json:"protocolVersion"`
	TaskID          string        `json:"taskId"`
	CommandID       string        `json:"commandId"`
	Sequence        int           `json:"sequence"`
	PageID          string        `json:"pageId,omitempty"`
	ObservationID   string        `json:"observationId,omitempty"`
	Action          BrowserAction `json:"action"`
	TimeoutMS       int           `json:"timeoutMs,omitempty"`
}

// Lead 是后端已核实并持久化的客户结果。Website 用于任务内去重，
// SourceURL/Evidence 用于保留该结果来自哪个已观察页面及其事实依据。
type Lead struct {
	CompanyName string `json:"companyName"`
	Website     string `json:"website"`
	SourceURL   string `json:"sourceUrl"`
	Evidence    string `json:"evidence"`
	Contact     string `json:"contact,omitempty"`
}

// CommandPoll 是后端对插件的任务状态指令。Status 是任务状态的唯一权威来源；
// Continue 明确告诉执行端是否应继续领取命令。COMPLETED 等终态下可能仍返回
// 一条待确认的兼容命令，执行端不应据此改变后端任务状态。
type CommandPoll struct {
	Status         TaskStatus    `json:"status"`
	Continue       bool          `json:"continue"`
	Command        *AgentCommand `json:"command,omitempty"`
	RetryAfterMS   int           `json:"retryAfterMs"`
	ResultSummary  string        `json:"resultSummary,omitempty"`
	Leads          []Lead        `json:"leads,omitempty"`
	CollectedCount int           `json:"collectedCount"`
	TargetCount    int           `json:"targetCount"`
}

type CommandError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
	PageID    string `json:"pageId,omitempty"`
	CommandID string `json:"commandId,omitempty"`
}

type CommandResult struct {
	CommandID  string          `json:"commandId"`
	Sequence   int             `json:"sequence"`
	OK         bool            `json:"ok"`
	StartedAt  string          `json:"startedAt"`
	FinishedAt string          `json:"finishedAt"`
	Data       json.RawMessage `json:"data,omitempty"`
	Error      *CommandError   `json:"error,omitempty"`
}

type Observation struct {
	TaskID        string          `json:"taskId"`
	TenantCode    string          `json:"tenantCode"`
	PageID        string          `json:"pageId"`
	ObservationID string          `json:"observationId"`
	PageVersion   int             `json:"pageVersion"`
	Level         string          `json:"level"`
	URL           string          `json:"url"`
	Title         string          `json:"title"`
	Language      string          `json:"language"`
	CollectedAt   string          `json:"collectedAt"`
	TextSummary   string          `json:"textSummary"`
	Text          string          `json:"text,omitempty"`
	Links         json.RawMessage `json:"links,omitempty"`
	Elements      json.RawMessage `json:"elements,omitempty"`
	HTML          string          `json:"html,omitempty"`
	Screenshot    string          `json:"screenshot,omitempty"`
	Truncated     json.RawMessage `json:"truncated"`
}

type Decision struct {
	PageID        string
	ObservationID string
	Action        BrowserAction
	TimeoutMS     int
	Memory        string
	Leads         []Lead
}

func workflows() []Workflow {
	return []Workflow{
		{
			WorkflowID:  "google-lead-search",
			Name:        "Google 搜索获客",
			Description: "按产品与目标市场搜索潜在客户，并分析企业官网公开信息。",
			Icon:        "◎",
			Category:    "获客",
			Enabled:     true,
			Status:      "available",
			Version:     "1.0.0",
			InputSchema: InputSchema{Properties: map[string]InputProperty{
				"product": {
					Type: "string", Title: "产品或服务",
					Description: "例如：solar panel", Required: true,
				},
				"country": {
					Type: "string", Title: "目标国家/地区",
					Description: "例如：Germany", Required: true,
				},
				"resultLimit": {
					Type: "number", Title: "目标客户数", Required: true, Default: 20,
				},
			}},
			Capabilities: []string{"多标签页", "页面正文", "链接与 DOM", "按需截图"},
			AgentPrompt: `使用 Google 搜索与目标产品、国家相关的潜在企业客户。阅读搜索结果并打开候选企业官网，
确认企业与产品的相关性。每发现一个已核实客户，立即在 send_browser_action 的 leads 中结构化上报：
companyName、website、sourceUrl、evidence，以及页面明确公开时才填写的 contact。sourceUrl 必须是当前任务实际观察过的页面。
排除广告、社交网络、聚合目录和无法从页面证实的信息。后端会按 website 去重、累计并在达到 resultLimit 时自动终止任务；
未达到目标时继续搜索和核实，不要仅把客户写进 memory。确实没有更多可靠候选时才可提前 COMPLETE，
并在 summary 中说明已核实数量和未完成原因。`,
		},
		{
			WorkflowID:   "google-maps-leads",
			Name:         "Google Maps 获客",
			Description:  "从地图商家结果中发现潜在客户。",
			Icon:         "◇",
			Category:     "获客",
			Enabled:      false,
			Status:       "coming_soon",
			Version:      "0.1.0",
			InputSchema:  InputSchema{Properties: map[string]InputProperty{}},
			Capabilities: []string{"地图页面"},
		},
	}
}

func findWorkflow(workflowID string) (Workflow, bool) {
	for _, workflow := range workflows() {
		if workflow.WorkflowID == workflowID {
			return workflow, true
		}
	}
	return Workflow{}, false
}

func validateInputs(workflow Workflow, inputs map[string]any) error {
	for name, property := range workflow.InputSchema.Properties {
		value, exists := inputs[name]
		if !exists || value == nil {
			if property.Required {
				return fmt.Errorf("input %q is required", name)
			}
			continue
		}
		switch property.Type {
		case "string":
			text, ok := value.(string)
			if !ok {
				return fmt.Errorf("input %q must be a string", name)
			}
			if property.Required && strings.TrimSpace(text) == "" {
				return fmt.Errorf("input %q is required", name)
			}
		case "number":
			number, ok := value.(float64)
			if !ok {
				return fmt.Errorf("input %q must be a number", name)
			}
			if name == "resultLimit" &&
				(math.Trunc(number) != number || number < 1 || number > 100) {
				return fmt.Errorf("input %q must be an integer between 1 and 100", name)
			}
		case "boolean":
			if _, ok := value.(bool); !ok {
				return fmt.Errorf("input %q must be a boolean", name)
			}
		}
	}
	return nil
}

func validateAction(decision Decision) error {
	action := decision.Action
	switch action.Type {
	case "OPEN_TAB", "NAVIGATE":
		parsed, err := url.ParseRequestURI(action.URL)
		if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return fmt.Errorf("action %s requires a safe HTTP/HTTPS URL", action.Type)
		}
		if action.Type == "NAVIGATE" && decision.PageID == "" {
			return fmt.Errorf("NAVIGATE requires pageId")
		}
	case "CLOSE_TAB", "SWITCH_TAB", "GO_BACK":
		if decision.PageID == "" {
			return fmt.Errorf("action %s requires pageId", action.Type)
		}
	case "CLICK":
		if decision.PageID == "" || decision.ObservationID == "" || action.ElementRef == "" {
			return fmt.Errorf("CLICK requires pageId, observationId and elementRef")
		}
	case "TYPE":
		if decision.PageID == "" || decision.ObservationID == "" ||
			action.ElementRef == "" {
			return fmt.Errorf("TYPE requires pageId, observationId and elementRef")
		}
	case "PRESS_KEY":
		if decision.PageID == "" {
			return fmt.Errorf("PRESS_KEY requires pageId")
		}
		if action.ElementRef != "" && decision.ObservationID == "" {
			return fmt.Errorf("element PRESS_KEY requires observationId")
		}
		switch action.Key {
		case "Enter", "Escape", "Tab", "ArrowUp", "ArrowDown":
		default:
			return fmt.Errorf("PRESS_KEY key is not allowed")
		}
	case "SELECT":
		if decision.PageID == "" || decision.ObservationID == "" ||
			action.ElementRef == "" {
			return fmt.Errorf("SELECT requires pageId, observationId and elementRef")
		}
	case "SCROLL":
		if decision.PageID == "" {
			return fmt.Errorf("SCROLL requires pageId")
		}
		if action.ElementRef != "" && decision.ObservationID == "" {
			return fmt.Errorf("element SCROLL requires observationId")
		}
	case "WAIT":
		if decision.PageID == "" {
			return fmt.Errorf("WAIT requires pageId")
		}
		if action.DurationMS != nil && (*action.DurationMS < 0 || *action.DurationMS > 30000) {
			return fmt.Errorf("WAIT durationMs must be between 0 and 30000")
		}
	case "OBSERVE":
		if decision.PageID == "" {
			return fmt.Errorf("OBSERVE requires pageId")
		}
		if action.Level != "LIGHT" && action.Level != "STANDARD" && action.Level != "FULL" {
			return fmt.Errorf("OBSERVE level is invalid")
		}
	case "COMPLETE":
	default:
		return fmt.Errorf("unsupported action %q", action.Type)
	}
	if decision.TimeoutMS != 0 && (decision.TimeoutMS < 100 || decision.TimeoutMS > 30000) {
		return fmt.Errorf("timeoutMs must be between 100 and 30000")
	}
	return nil
}

func parseTime(value string) error {
	if value == "" {
		return fmt.Errorf("timestamp is required")
	}
	if _, err := time.Parse(time.RFC3339, value); err != nil {
		return fmt.Errorf("invalid timestamp: %w", err)
	}
	return nil
}
