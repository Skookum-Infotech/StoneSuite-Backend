package controllers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/middleware"
	"stonesuite-backend/teamstore"
	"stonesuite-backend/tenancy"
)

// TeamOps groups the tenant-scoped team + membership handlers. Teams live in the
// tenant database, so the resolved pool is the tenant scope. Teams are an admin
// configuration resource (team:read / team:configure), not owner-scoped, so no
// per-row scope filter applies.
type TeamOps struct{}

// NewTeamOps constructs the team handler group.
func NewTeamOps() *TeamOps { return &TeamOps{} }

// authorize checks team:action for the caller and returns the tenant pool. On
// failure it writes a response and returns ok=false.
func (h *TeamOps) authorize(w http.ResponseWriter, r *http.Request, action authz.Action) (*pgxpool.Pool, bool) {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return nil, false
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return nil, false
	}
	decision, err := authz.Check(r.Context(), pool, payload.ID, authz.ResourceTeam, action)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, false
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", payload.ID, "resource", string(authz.ResourceTeam), "action", string(action))
		fail(w, http.StatusForbidden, "You do not have permission to "+string(action)+" teams.")
		return nil, false
	}
	return pool, true
}

type teamRequest struct {
	Name string `json:"name"`
}

type memberRequest struct {
	UserID string `json:"user_id"`
}

// ListTeams GET /api/tenant/teams
func (h *TeamOps) ListTeams(w http.ResponseWriter, r *http.Request) {
	pool, ok := h.authorize(w, r, authz.ActionRead)
	if !ok {
		return
	}
	teams, err := teamstore.ListTeams(r.Context(), pool)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to list teams.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "teams": teams})
}

// GetTeam GET /api/tenant/teams/{id}
func (h *TeamOps) GetTeam(w http.ResponseWriter, r *http.Request) {
	pool, ok := h.authorize(w, r, authz.ActionRead)
	if !ok {
		return
	}
	team, err := teamstore.GetTeam(r.Context(), pool, r.PathValue("id"))
	if errors.Is(err, teamstore.ErrTeamNotFound) {
		fail(w, http.StatusNotFound, "Team not found.")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load team.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "team": team})
}

// CreateTeam POST /api/tenant/teams
func (h *TeamOps) CreateTeam(w http.ResponseWriter, r *http.Request) {
	pool, ok := h.authorize(w, r, authz.ActionConfigure)
	if !ok {
		return
	}
	var req teamRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		fail(w, http.StatusBadRequest, "Team name is required.")
		return
	}
	team, err := teamstore.CreateTeam(r.Context(), pool, name)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to create team.")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "team": team})
}

// UpdateTeam PUT /api/tenant/teams/{id}
func (h *TeamOps) UpdateTeam(w http.ResponseWriter, r *http.Request) {
	pool, ok := h.authorize(w, r, authz.ActionConfigure)
	if !ok {
		return
	}
	var req teamRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		fail(w, http.StatusBadRequest, "Team name is required.")
		return
	}
	team, err := teamstore.UpdateTeam(r.Context(), pool, r.PathValue("id"), name)
	if errors.Is(err, teamstore.ErrTeamNotFound) {
		fail(w, http.StatusNotFound, "Team not found.")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to update team.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "team": team})
}

// DeleteTeam DELETE /api/tenant/teams/{id}
func (h *TeamOps) DeleteTeam(w http.ResponseWriter, r *http.Request) {
	pool, ok := h.authorize(w, r, authz.ActionConfigure)
	if !ok {
		return
	}
	err := teamstore.DeleteTeam(r.Context(), pool, r.PathValue("id"))
	if errors.Is(err, teamstore.ErrTeamNotFound) {
		fail(w, http.StatusNotFound, "Team not found.")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to delete team.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

// AddMember POST /api/tenant/teams/{id}/members
func (h *TeamOps) AddMember(w http.ResponseWriter, r *http.Request) {
	pool, ok := h.authorize(w, r, authz.ActionConfigure)
	if !ok {
		return
	}
	var req memberRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	if strings.TrimSpace(req.UserID) == "" {
		fail(w, http.StatusBadRequest, "user_id is required.")
		return
	}
	err := teamstore.AddMember(r.Context(), pool, r.PathValue("id"), req.UserID)
	switch {
	case errors.Is(err, teamstore.ErrTeamNotFound):
		fail(w, http.StatusNotFound, "Team not found.")
	case errors.Is(err, teamstore.ErrUserNotFound):
		fail(w, http.StatusBadRequest, "User not found.")
	case err != nil:
		fail(w, http.StatusInternalServerError, "Failed to add team member.")
	default:
		writeJSON(w, http.StatusOK, map[string]any{"success": true})
	}
}

// RemoveMember DELETE /api/tenant/teams/{id}/members/{userId}
func (h *TeamOps) RemoveMember(w http.ResponseWriter, r *http.Request) {
	pool, ok := h.authorize(w, r, authz.ActionConfigure)
	if !ok {
		return
	}
	err := teamstore.RemoveMember(r.Context(), pool, r.PathValue("id"), r.PathValue("userId"))
	if errors.Is(err, teamstore.ErrTeamNotFound) {
		fail(w, http.StatusNotFound, "Team not found.")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to remove team member.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}
