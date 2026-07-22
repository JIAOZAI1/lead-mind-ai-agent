package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/identity"
	"github.com/JIAOZAI1/lead-mind-ai-agent/internal/session"
)

type sessionResponse struct {
	ID           string `json:"id"`
	Title        string `json:"title"`
	Pinned       bool   `json:"pinned"`
	Archived     bool   `json:"archived"`
	CreatedAt    string `json:"created_at"`
	LastActiveAt string `json:"last_active_at"`
}

func toSessionResponse(s session.Session) sessionResponse {
	return sessionResponse{
		ID:           s.ID,
		Title:        s.Title,
		Pinned:       s.Pinned,
		Archived:     s.Archived,
		CreatedAt:    s.CreatedAt.Format(time.RFC3339),
		LastActiveAt: s.LastActiveAt.Format(time.RFC3339),
	}
}

type sessionPatchRequest struct {
	Title    *string `json:"title"`
	Pinned   *bool   `json:"pinned"`
	Archived *bool   `json:"archived"`
}

// ListSessions handles GET /ai-agent/v1/sessions?include_archived=false,
// returning the caller's own session list — metadata only (title,
// pinned/archived, timestamps), never conversation content (see
// internal/memory/shortterm for that, which is TTL-bounded independent of
// this list).
func (d AgentDeps) ListSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()
	id, _ := identity.FromContext(ctx)

	includeArchived := r.URL.Query().Get("include_archived") == "true"

	sessions, err := d.Sessions.List(ctx, id.TenantCode, id.UserID, includeArchived)
	if err != nil {
		httpError(ctx, w, r, err, "failed to list sessions", http.StatusInternalServerError)
		return
	}

	out := make([]sessionResponse, len(sessions))
	for i, s := range sessions {
		out[i] = toSessionResponse(s)
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// PatchSession handles PATCH /ai-agent/v1/sessions/{id}, supporting
// partial updates to title/pinned/archived.
func (d AgentDeps) PatchSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPatch {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()
	id, _ := identity.FromContext(ctx)
	sessionID := r.PathValue("id")

	owned, err := d.ownsSession(ctx, id.TenantCode, id.UserID, sessionID)
	if err != nil {
		httpError(ctx, w, r, err, "failed to look up session", http.StatusInternalServerError)
		return
	}
	if !owned {
		http.Error(w, `{"error":"session not found"}`, http.StatusNotFound)
		return
	}

	var req sessionPatchRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		httpError(ctx, w, r, err, "invalid request body", http.StatusBadRequest)
		return
	}

	if req.Title != nil {
		if err := d.Sessions.Rename(ctx, id.TenantCode, sessionID, *req.Title); err != nil {
			httpError(ctx, w, r, err, "failed to rename session", http.StatusInternalServerError)
			return
		}
	}
	if req.Pinned != nil {
		if err := d.Sessions.SetPinned(ctx, id.TenantCode, sessionID, *req.Pinned); err != nil {
			httpError(ctx, w, r, err, "failed to update session", http.StatusInternalServerError)
			return
		}
	}
	if req.Archived != nil {
		if err := d.Sessions.SetArchived(ctx, id.TenantCode, sessionID, *req.Archived); err != nil {
			httpError(ctx, w, r, err, "failed to update session", http.StatusInternalServerError)
			return
		}
	}

	sess, ok, err := d.Sessions.Get(ctx, id.TenantCode, sessionID)
	if err != nil {
		httpError(ctx, w, r, err, "failed to reload session", http.StatusInternalServerError)
		return
	}
	if !ok {
		httpError(ctx, w, r, fmt.Errorf("session %s vanished after patch", sessionID), "failed to reload session", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(toSessionResponse(sess))
}

// DeleteSession handles DELETE /ai-agent/v1/sessions/{id}: it removes the
// session's metadata and clears any not-yet-expired short-term history.
func (d AgentDeps) DeleteSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete {
		http.Error(w, `{"error":"method not allowed"}`, http.StatusMethodNotAllowed)
		return
	}

	ctx := r.Context()
	id, _ := identity.FromContext(ctx)
	sessionID := r.PathValue("id")

	owned, err := d.ownsSession(ctx, id.TenantCode, id.UserID, sessionID)
	if err != nil {
		httpError(ctx, w, r, err, "failed to look up session", http.StatusInternalServerError)
		return
	}
	if !owned {
		http.Error(w, `{"error":"session not found"}`, http.StatusNotFound)
		return
	}

	if err := d.Sessions.Delete(ctx, id.TenantCode, sessionID); err != nil {
		httpError(ctx, w, r, err, "failed to delete session", http.StatusInternalServerError)
		return
	}
	if err := d.ShortTerm.Reset(ctx, id.TenantCode, sessionID); err != nil {
		httpError(ctx, w, r, err, "failed to clear session history", http.StatusInternalServerError)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

// ownsSession reports whether sessionID exists and belongs to userID.
// Callers should treat "not found" and "not yours" identically (404, not
// 403) so a caller can't distinguish "doesn't exist" from "exists but
// isn't yours".
func (d AgentDeps) ownsSession(ctx context.Context, tenantCode, userID, sessionID string) (bool, error) {
	sess, ok, err := d.Sessions.Get(ctx, tenantCode, sessionID)
	if err != nil {
		return false, err
	}
	if !ok || sess.UserID != userID {
		return false, nil
	}
	return true, nil
}
