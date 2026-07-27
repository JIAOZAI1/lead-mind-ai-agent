package plugin

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/identity"
)

type Handler struct {
	service *Service
}

const _maxPluginRequestBytes = 12 << 20

func NewHandler(service *Service) *Handler {
	return &Handler{service: service}
}

func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /ai-agent/api/plugin/workflows", h.listWorkflows)
	mux.HandleFunc("POST /ai-agent/api/plugin/tasks", h.createTask)
	mux.HandleFunc("GET /ai-agent/api/plugin/tasks/{taskId}/commands", h.pollCommand)
	mux.HandleFunc("POST /ai-agent/api/plugin/tasks/{taskId}/command-results", h.submitCommandResult)
	mux.HandleFunc("POST /ai-agent/api/plugin/tasks/{taskId}/observations", h.submitObservation)
	mux.HandleFunc("POST /ai-agent/api/plugin/tasks/{taskId}/control", h.controlTask)
}

func (h *Handler) listWorkflows(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, h.service.Workflows())
}

func (h *Handler) createTask(w http.ResponseWriter, r *http.Request) {
	var request struct {
		WorkflowID string         `json:"workflowId"`
		Inputs     map[string]any `json:"inputs"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(r, w, err, http.StatusBadRequest)
		return
	}
	id, _ := identity.FromContext(r.Context())
	taskID, err := h.service.CreateTask(id.TenantCode, id.UserID, request.WorkflowID, request.Inputs)
	if err != nil {
		writeError(r, w, err, statusForError(err))
		return
	}
	writeJSON(w, http.StatusCreated, map[string]string{"taskId": taskID})
}

func (h *Handler) pollCommand(w http.ResponseWriter, r *http.Request) {
	id, _ := identity.FromContext(r.Context())
	command, retryAfterMS, err := h.service.NextCommand(
		r.Context(), id.TenantCode, id.UserID, r.PathValue("taskId"),
	)
	if err != nil {
		status := statusForError(err)
		if status == http.StatusBadRequest {
			status = http.StatusBadGateway
		}
		writeError(r, w, err, status)
		return
	}
	response := struct {
		Command      *AgentCommand `json:"command,omitempty"`
		RetryAfterMS int           `json:"retryAfterMs"`
	}{Command: command, RetryAfterMS: retryAfterMS}
	writeJSON(w, http.StatusOK, response)
}

func (h *Handler) submitCommandResult(w http.ResponseWriter, r *http.Request) {
	var result CommandResult
	if err := decodeJSON(w, r, &result); err != nil {
		writeError(r, w, err, http.StatusBadRequest)
		return
	}
	id, _ := identity.FromContext(r.Context())
	err := h.service.SubmitResult(id.TenantCode, id.UserID, r.PathValue("taskId"), result)
	if err != nil {
		writeError(r, w, err, statusForError(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) submitObservation(w http.ResponseWriter, r *http.Request) {
	var observation Observation
	if err := decodeJSON(w, r, &observation); err != nil {
		writeError(r, w, err, http.StatusBadRequest)
		return
	}
	id, _ := identity.FromContext(r.Context())
	err := h.service.SubmitObservation(
		id.TenantCode, id.UserID, r.PathValue("taskId"), observation,
	)
	if err != nil {
		writeError(r, w, err, statusForError(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) controlTask(w http.ResponseWriter, r *http.Request) {
	var request struct {
		Action string `json:"action"`
		PageID string `json:"pageId"`
	}
	if err := decodeJSON(w, r, &request); err != nil {
		writeError(r, w, err, http.StatusBadRequest)
		return
	}
	id, _ := identity.FromContext(r.Context())
	err := h.service.Control(
		id.TenantCode, id.UserID, r.PathValue("taskId"), request.Action, request.PageID,
	)
	if err != nil {
		writeError(r, w, err, statusForError(err))
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func decodeJSON(w http.ResponseWriter, r *http.Request, target any) error {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, _maxPluginRequestBytes))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(r *http.Request, w http.ResponseWriter, err error, status int) {
	id, _ := identity.FromContext(r.Context())
	level := slog.LevelWarn
	if status >= http.StatusInternalServerError {
		level = slog.LevelError
	}
	slog.Log(r.Context(), level, "plugin API error",
		"method", r.Method,
		"path", r.URL.Path,
		"tenant_code", id.TenantCode,
		"user_id", id.UserID,
		"status", status,
		"error", err,
	)
	message := "invalid request"
	switch {
	case errors.Is(err, ErrTaskNotFound):
		message = "task not found"
	case errors.Is(err, ErrTaskConflict):
		message = "task state conflict"
	case status >= http.StatusInternalServerError:
		message = "agent unavailable"
	}
	writeJSON(w, status, map[string]string{"error": message})
}
