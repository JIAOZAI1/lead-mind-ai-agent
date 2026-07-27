package plugin

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
)

type Service struct {
	store Store
	agent BrowserAgent
}

const _agentPlanningTimeout = 45 * time.Second

func NewService(store Store, agent BrowserAgent) *Service {
	return &Service{store: store, agent: agent}
}

func (s *Service) Workflows() []Workflow {
	return workflows()
}

func (s *Service) CreateTask(
	tenantCode string,
	userID string,
	workflowID string,
	inputs map[string]any,
) (string, error) {
	workflow, ok := findWorkflow(workflowID)
	if !ok {
		return "", fmt.Errorf("workflow %q: %w", workflowID, ErrTaskNotFound)
	}
	if !workflow.Enabled {
		return "", fmt.Errorf("workflow %q is not available: %w", workflowID, ErrTaskConflict)
	}
	if err := validateInputs(workflow, inputs); err != nil {
		return "", err
	}
	now := time.Now().UTC()
	taskID := "task_" + uuid.NewString()
	if err := s.store.Create(Task{
		ID:           taskID,
		TenantCode:   tenantCode,
		UserID:       userID,
		WorkflowID:   workflowID,
		Inputs:       inputs,
		Status:       TaskRunning,
		NextSequence: 1,
		Results:      make(map[string]CommandResult),
		Observations: make(map[string]Observation),
		CreatedAt:    now,
		UpdatedAt:    now,
	}); err != nil {
		return "", fmt.Errorf("create plugin task: %w", err)
	}
	return taskID, nil
}

func (s *Service) NextCommand(
	ctx context.Context,
	tenantCode string,
	userID string,
	taskID string,
) (*AgentCommand, int, error) {
	poll, err := s.PollCommand(ctx, tenantCode, userID, taskID)
	return poll.Command, poll.RetryAfterMS, err
}

// PollCommand 返回由后端维护的任务状态和下一条执行命令。插件只负责执行
// Command；是否继续领取以及任务是否终止，以 Status/Continue 为准。
func (s *Service) PollCommand(
	ctx context.Context,
	tenantCode string,
	userID string,
	taskID string,
) (CommandPoll, error) {
	poll := CommandPoll{RetryAfterMS: defaultRetryMS}
	var existing *AgentCommand
	var agentInput AgentInput
	err := s.store.Update(tenantCode, userID, taskID, func(task *Task) error {
		fillCommandPoll(&poll, task)
		if task.CurrentCommand != nil {
			command := *task.CurrentCommand
			existing = &command
			return nil
		}
		if task.Status != TaskRunning {
			return nil
		}
		if task.Planning && time.Since(task.PlanningAt) < time.Minute {
			return nil
		}
		workflow, ok := findWorkflow(task.WorkflowID)
		if !ok {
			return ErrTaskConflict
		}
		task.Planning = true
		task.PlanningAt = time.Now().UTC()
		agentInput = AgentInput{
			TaskID:       task.ID,
			Workflow:     workflow,
			Inputs:       task.Inputs,
			Steps:        append([]AgentStep(nil), task.Steps...),
			Observations: copyObservations(task.Observations),
			Events:       append([]AgentEvent(nil), task.Events...),
			Leads:        append([]Lead(nil), task.Leads...),
		}
		if task.Observation != nil {
			agentInput.LatestObservationID = task.Observation.ObservationID
			if len(agentInput.Observations) == 0 {
				agentInput.Observations = map[string]Observation{
					task.Observation.PageID: *task.Observation,
				}
			}
		}
		return nil
	})
	if err != nil || existing != nil {
		poll.Command = existing
		poll.Continue = poll.Status == TaskRunning
		return poll, err
	}
	if agentInput.Workflow.WorkflowID == "" {
		poll.Continue = poll.Status == TaskRunning
		return poll, nil
	}

	planningContext, cancel := context.WithTimeout(ctx, _agentPlanningTimeout)
	defer cancel()
	decision, err := s.agent.NextAction(planningContext, agentInput)
	if err != nil {
		s.clearPlanning(tenantCode, userID, taskID)
		return poll, err
	}
	if err := validateAction(decision); err != nil {
		s.clearPlanning(tenantCode, userID, taskID)
		return poll, err
	}

	var command *AgentCommand
	err = s.store.Update(tenantCode, userID, taskID, func(task *Task) error {
		task.Planning = false
		task.PlanningAt = time.Time{}
		fillCommandPoll(&poll, task)
		if task.Status != TaskRunning || task.CurrentCommand != nil {
			return nil
		}
		newLeads := mergeVerifiedLeads(task, decision.Leads)
		targetCount := targetLeadCount(task.Inputs)
		if targetCount > 0 && len(task.Leads) >= targetCount {
			decision.Action = BrowserAction{
				Type:    "COMPLETE",
				Summary: leadTargetSummary(task.Leads, targetCount),
			}
			decision.PageID = ""
			decision.ObservationID = ""
			decision.TimeoutMS = 0
		}
		value := AgentCommand{
			ProtocolVersion: ProtocolVersion,
			TaskID:          task.ID,
			CommandID:       "cmd_" + uuid.NewString(),
			Sequence:        task.NextSequence,
			PageID:          decision.PageID,
			ObservationID:   decision.ObservationID,
			Action:          decision.Action,
			TimeoutMS:       decision.TimeoutMS,
		}
		task.NextSequence++
		task.CurrentCommand = &value
		task.CurrentMemory = decision.Memory
		task.CurrentLeads = newLeads
		if decision.Action.Type == "COMPLETE" {
			// COMPLETE 是后端 Agent 的终止判定。状态在命令返回给插件前即
			// 进入终态；命令只作为对旧版插件的终止通知保留，等待结果确认
			// 后清除，不再由插件结果驱动状态转换。
			task.Status = TaskCompleted
			task.ResultSummary = decision.Action.Summary
		}
		fillCommandPoll(&poll, task)
		command = &value
		return nil
	})
	poll.Command = command
	poll.Continue = poll.Status == TaskRunning
	return poll, err
}

func (s *Service) SubmitResult(
	tenantCode string,
	userID string,
	taskID string,
	result CommandResult,
) error {
	if result.CommandID == "" || result.Sequence < 1 {
		return fmt.Errorf("commandId and a positive sequence are required")
	}
	if err := parseTime(result.StartedAt); err != nil {
		return err
	}
	if err := parseTime(result.FinishedAt); err != nil {
		return err
	}
	return s.store.Update(tenantCode, userID, taskID, func(task *Task) error {
		if _, duplicate := task.Results[result.CommandID]; duplicate {
			return nil
		}
		if task.CurrentCommand == nil ||
			task.CurrentCommand.CommandID != result.CommandID ||
			task.CurrentCommand.Sequence != result.Sequence {
			return ErrTaskConflict
		}
		task.Results[result.CommandID] = result
		task.LastResult = &result
		actionType := task.CurrentCommand.Action.Type
		if actionChangesPage(actionType) {
			delete(task.Observations, task.CurrentCommand.PageID)
		}
		task.Steps = append(task.Steps, AgentStep{
			Command: *task.CurrentCommand,
			Result:  result,
			Memory:  task.CurrentMemory,
			Leads:   append([]Lead(nil), task.CurrentLeads...),
		})
		if len(task.Steps) > maxStoredAgentSteps {
			task.Steps = append([]AgentStep(nil), task.Steps[len(task.Steps)-maxStoredAgentSteps:]...)
		}
		task.CurrentCommand = nil
		task.CurrentMemory = ""
		task.CurrentLeads = nil
		return nil
	})
}

func (s *Service) SubmitObservation(
	tenantCode string,
	userID string,
	taskID string,
	observation Observation,
) error {
	if observation.TaskID != taskID || observation.TenantCode != tenantCode {
		return ErrTaskConflict
	}
	if observation.PageID == "" || observation.ObservationID == "" {
		return fmt.Errorf("pageId and observationId are required")
	}
	if observation.Level != "LIGHT" && observation.Level != "STANDARD" && observation.Level != "FULL" {
		return fmt.Errorf("invalid observation level")
	}
	if err := parseTime(observation.CollectedAt); err != nil {
		return err
	}
	// 模型只需要一段受控大小的 HTML 上下文；截图暂未接入多模态消息，
	// 不应把可能达到数 MB 的 Data URL 复制进 Redis 任务快照。
	observation.HTML = truncateText(observation.HTML, _maxPromptHTML)
	observation.Screenshot = ""
	return s.store.Update(tenantCode, userID, taskID, func(task *Task) error {
		task.Observation = &observation
		if task.Observations == nil {
			task.Observations = make(map[string]Observation)
		}
		task.Observations[observation.PageID] = observation
		return nil
	})
}

func (s *Service) Control(
	tenantCode string,
	userID string,
	taskID string,
	action string,
	pageID string,
) error {
	return s.store.Update(tenantCode, userID, taskID, func(task *Task) error {
		switch action {
		case "PAUSE":
			if task.Status == TaskRunning {
				task.Status = TaskPaused
				appendAgentEvent(task, action, pageID)
			}
		case "RESUME":
			if task.Status != TaskPaused {
				return ErrTaskConflict
			}
			task.Status = TaskRunning
			appendAgentEvent(task, action, pageID)
		case "SKIP":
			if task.Status != TaskRunning {
				return ErrTaskConflict
			}
			task.Observation = nil
			delete(task.Observations, pageID)
			task.LastResult = &CommandResult{
				CommandID: "user_control",
				OK:        false,
				Error: &CommandError{
					Code:    "SKIPPED_BY_USER",
					Message: "用户要求跳过当前网站，请选择下一个候选页面。",
					PageID:  pageID,
				},
			}
			appendAgentEvent(task, action, pageID)
		case "TAB_CLOSED":
			task.Observation = nil
			delete(task.Observations, pageID)
			task.LastResult = &CommandResult{
				CommandID: "user_control",
				OK:        false,
				Error: &CommandError{
					Code:    "TAB_CLOSED",
					Message: "任务页面已被用户关闭。",
					PageID:  pageID,
				},
			}
			appendAgentEvent(task, action, pageID)
		case "CANCEL":
			task.Status = TaskCancelled
			task.CurrentCommand = nil
		default:
			return fmt.Errorf("unsupported control action %q", action)
		}
		return nil
	})
}

func appendAgentEvent(task *Task, eventType string, pageID string) {
	task.Events = append(task.Events, AgentEvent{
		Type:   eventType,
		PageID: pageID,
		At:     time.Now().UTC(),
	})
	if len(task.Events) > 20 {
		task.Events = append([]AgentEvent(nil), task.Events[len(task.Events)-20:]...)
	}
}

func (s *Service) clearPlanning(tenantCode string, userID string, taskID string) {
	_ = s.store.Update(tenantCode, userID, taskID, func(task *Task) error {
		task.Planning = false
		task.PlanningAt = time.Time{}
		return nil
	})
}

func copyObservations(input map[string]Observation) map[string]Observation {
	if len(input) == 0 {
		return nil
	}
	output := make(map[string]Observation, len(input))
	for pageID, observation := range input {
		output[pageID] = observation
	}
	return output
}

func fillCommandPoll(poll *CommandPoll, task *Task) {
	poll.Status = task.Status
	poll.ResultSummary = task.ResultSummary
	poll.Leads = append([]Lead(nil), task.Leads...)
	poll.CollectedCount = len(task.Leads)
	poll.TargetCount = targetLeadCount(task.Inputs)
}

func targetLeadCount(inputs map[string]any) int {
	switch value := inputs["resultLimit"].(type) {
	case float64:
		return int(value)
	case int:
		return value
	default:
		return 0
	}
}

func mergeVerifiedLeads(task *Task, candidates []Lead) []Lead {
	if len(candidates) == 0 {
		return nil
	}
	observedURLs := make(map[string]struct{}, len(task.Observations))
	for _, observation := range task.Observations {
		if normalized := normalizeSourceURL(observation.URL); normalized != "" {
			observedURLs[normalized] = struct{}{}
		}
	}
	existing := make(map[string]struct{}, len(task.Leads))
	for _, lead := range task.Leads {
		if key := leadWebsiteKey(lead.Website); key != "" {
			existing[key] = struct{}{}
		}
	}
	added := make([]Lead, 0, len(candidates))
	targetCount := targetLeadCount(task.Inputs)
	for _, candidate := range candidates {
		if targetCount > 0 && len(task.Leads) >= targetCount {
			break
		}
		candidate.CompanyName = strings.TrimSpace(candidate.CompanyName)
		candidate.Website = strings.TrimSpace(candidate.Website)
		candidate.SourceURL = strings.TrimSpace(candidate.SourceURL)
		candidate.Evidence = truncateText(strings.TrimSpace(candidate.Evidence), 2000)
		candidate.Contact = truncateText(strings.TrimSpace(candidate.Contact), 1000)
		key := leadWebsiteKey(candidate.Website)
		if candidate.CompanyName == "" || candidate.Evidence == "" ||
			candidate.Contact == "" || key == "" {
			continue
		}
		if _, observed := observedURLs[normalizeSourceURL(candidate.SourceURL)]; !observed {
			continue
		}
		if _, duplicate := existing[key]; duplicate {
			continue
		}
		existing[key] = struct{}{}
		task.Leads = append(task.Leads, candidate)
		added = append(added, candidate)
	}
	return added
}

func normalizeSourceURL(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Hostname() == "" {
		return ""
	}
	parsed.Fragment = ""
	parsed.Host = strings.ToLower(parsed.Host)
	return strings.TrimRight(parsed.String(), "/")
}

func leadWebsiteKey(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		return ""
	}
	host := strings.ToLower(parsed.Hostname())
	host = strings.TrimPrefix(host, "www.")
	return host
}

func leadTargetSummary(leads []Lead, targetCount int) string {
	var summary strings.Builder
	fmt.Fprintf(&summary, "已达到目标：已核实 %d/%d 个客户。", len(leads), targetCount)
	for index, lead := range leads {
		fmt.Fprintf(&summary, "\n%d. %s — %s", index+1, lead.CompanyName, lead.Website)
	}
	return truncateText(summary.String(), _maxAgentMemory)
}

func actionChangesPage(actionType string) bool {
	switch actionType {
	case "NAVIGATE", "GO_BACK", "CLICK", "TYPE", "PRESS_KEY", "SELECT":
		return true
	default:
		return false
	}
}

func statusForError(err error) int {
	switch {
	case errors.Is(err, ErrTaskNotFound):
		return 404
	case errors.Is(err, ErrTaskConflict):
		return 409
	default:
		return 400
	}
}
