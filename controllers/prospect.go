package controllers

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/middleware"
	"stonesuite-backend/prospect"
	"stonesuite-backend/tenancy"
)

// ProspectOps handles all /api/tenant/prospects routes. Authorization uses the
// prospect resource so it can be granted independently of leads.
type ProspectOps struct{}

// NewProspectOps constructs the handler group.
func NewProspectOps() *ProspectOps { return &ProspectOps{} }

// authProspect resolves JWT identity + tenant pool + RBAC in one call. Returns
// pool, identityID, the broadest granted scope (all|team|own), and ok. On
// failure it writes a response and ok=false.
func authProspect(w http.ResponseWriter, r *http.Request, action authz.Action) (*pgxpool.Pool, string, authz.Scope, bool) {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return nil, "", "", false
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return nil, "", "", false
	}
	decision, err := authz.Check(r.Context(), pool, payload.ID, authz.ResourceProspect, action)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, "", "", false
	}
	if !decision.Allowed {
		fail(w, http.StatusForbidden, "You do not have permission to "+string(action)+" prospects.")
		return nil, "", "", false
	}
	return pool, payload.ID, decision.Scope, true
}

// prospectCallerScope resolves the caller's tenant user id and team ids for
// team/own scope filtering. For "all" scope it returns empties (no filtering).
func prospectCallerScope(r *http.Request, pool *pgxpool.Pool, identityID string, scope authz.Scope) (string, []string) {
	if scope == authz.ScopeAll {
		return "", nil
	}
	userID, err := prospect.UserIDByIdentity(r.Context(), pool, identityID)
	if err != nil {
		return "", nil
	}
	if scope == authz.ScopeTeam {
		teams, _ := prospect.TeamIDsForUser(r.Context(), pool, userID)
		return userID, teams
	}
	return userID, nil
}

// ListProspects GET /api/tenant/prospects — scope-filtered by the caller's grant.
func (h *ProspectOps) ListProspects(w http.ResponseWriter, r *http.Request) {
	pool, identityID, scope, ok := authProspect(w, r, authz.ActionRead)
	if !ok {
		return
	}
	callerUserID, teamIDs := prospectCallerScope(r, pool, identityID, scope)
	prospects, err := prospect.List(r.Context(), pool, string(scope), callerUserID, teamIDs)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to list prospects.")
		return
	}
	if prospects == nil {
		prospects = []prospect.Prospect{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "scope": scope, "prospects": prospects})
}

// CreateProspect POST /api/tenant/prospects
func (h *ProspectOps) CreateProspect(w http.ResponseWriter, r *http.Request) {
	pool, identityID, _, ok := authProspect(w, r, authz.ActionCreate)
	if !ok {
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	name, _ := body["company_name"].(string)
	if name == "" {
		fail(w, http.StatusBadRequest, "company_name is required.")
		return
	}

	ownerUserID, _ := prospect.UserIDByIdentity(r.Context(), pool, identityID)
	p := prospect.FromMap(ownerUserID, body)

	created, err := prospect.Create(r.Context(), pool, p)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to create prospect.")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "prospect": created})
}

// GetProspect GET /api/tenant/prospects/{id}
func (h *ProspectOps) GetProspect(w http.ResponseWriter, r *http.Request) {
	pool, _, _, ok := authProspect(w, r, authz.ActionRead)
	if !ok {
		return
	}
	p, err := prospect.Get(r.Context(), pool, r.PathValue("id"))
	if errors.Is(err, prospect.ErrNotFound) {
		fail(w, http.StatusNotFound, "Prospect not found.")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load prospect.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "prospect": p})
}

// UpdateProspect PATCH /api/tenant/prospects/{id}
func (h *ProspectOps) UpdateProspect(w http.ResponseWriter, r *http.Request) {
	pool, identityID, _, ok := authProspect(w, r, authz.ActionUpdate)
	if !ok {
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	ownerUserID, _ := prospect.UserIDByIdentity(r.Context(), pool, identityID)
	p := prospect.FromMap(ownerUserID, body)

	updated, err := prospect.Update(r.Context(), pool, r.PathValue("id"), p)
	if errors.Is(err, prospect.ErrNotFound) {
		fail(w, http.StatusNotFound, "Prospect not found.")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to update prospect.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "prospect": updated})
}

// DeleteProspect DELETE /api/tenant/prospects/{id}
func (h *ProspectOps) DeleteProspect(w http.ResponseWriter, r *http.Request) {
	pool, _, _, ok := authProspect(w, r, authz.ActionDelete)
	if !ok {
		return
	}
	if err := prospect.Delete(r.Context(), pool, r.PathValue("id")); err != nil {
		if errors.Is(err, prospect.ErrNotFound) {
			fail(w, http.StatusNotFound, "Prospect not found.")
			return
		}
		fail(w, http.StatusInternalServerError, "Failed to delete prospect.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Prospect deleted."})
}
