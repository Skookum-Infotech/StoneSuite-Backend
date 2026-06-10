package controllers

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/middleware"
	"stonesuite-backend/models"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/workflow"
)

// WorkflowOps groups the tenant-scoped workflow + record handlers. All routes
// run behind RequireAuth + the tenancy resolver, then enforce a catalog
// permission per action.
type WorkflowOps struct {
	engine *workflow.Engine
}

// NewWorkflowOps constructs the handler group.
func NewWorkflowOps() *WorkflowOps { return &WorkflowOps{engine: workflow.NewEngine()} }

// authorize checks resource:action for the caller and returns the tenant pool,
// the granted scope, the caller identity, and ok. On failure it writes a
// response and returns ok=false.
func (h *WorkflowOps) authorize(w http.ResponseWriter, r *http.Request, resource authz.Resource, action authz.Action) (*pgxpool.Pool, authz.Scope, string, bool) {
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
	decision, err := authz.Check(r.Context(), pool, payload.ID, resource, action)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, "", "", false
	}
	if !decision.Allowed {
		fail(w, http.StatusForbidden, "You do not have permission to "+string(action)+" "+string(resource)+".")
		return nil, "", "", false
	}
	return pool, decision.Scope, payload.ID, true
}

// ---- workflow config --------------------------------------------------------

// ListWorkflows GET /api/tenant/workflows
func (h *WorkflowOps) ListWorkflows(w http.ResponseWriter, r *http.Request) {
	pool, _, _, ok := h.authorize(w, r, authz.ResourceWorkflow, authz.ActionRead)
	if !ok {
		return
	}
	wfs, err := workflow.ListWorkflows(r.Context(), pool)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to list workflows.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "workflows": wfs})
}

// GetWorkflow GET /api/tenant/workflows/{id} — full definition.
func (h *WorkflowOps) GetWorkflow(w http.ResponseWriter, r *http.Request) {
	pool, _, _, ok := h.authorize(w, r, authz.ResourceWorkflow, authz.ActionRead)
	if !ok {
		return
	}
	def, err := workflow.LoadDefinition(r.Context(), pool, r.PathValue("id"))
	if errors.Is(err, workflow.ErrWorkflowNotFound) {
		fail(w, http.StatusNotFound, "Workflow not found.")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load workflow.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "definition": def})
}

// SetWorkflowEnabled POST /api/tenant/workflows/{id}/enabled  body {"enabled":bool}
func (h *WorkflowOps) SetWorkflowEnabled(w http.ResponseWriter, r *http.Request) {
	pool, _, _, ok := h.authorize(w, r, authz.ResourceWorkflowConfig, authz.ActionConfigure)
	if !ok {
		return
	}
	var req struct {
		Enabled bool `json:"enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	if err := workflow.SetWorkflowEnabled(r.Context(), pool, r.PathValue("id"), req.Enabled); err != nil {
		if errors.Is(err, workflow.ErrWorkflowNotFound) {
			fail(w, http.StatusNotFound, "Workflow not found.")
			return
		}
		if errors.Is(err, workflow.ErrDisableDependency) {
			fail(w, http.StatusConflict, err.Error())
			return
		}
		fail(w, http.StatusInternalServerError, "Failed to update workflow.")
		return
	}
	writeJSON(w, http.StatusOK, models.APIResponse{Success: true, Message: "Workflow updated."})
}

// ---- record numbering --------------------------------------------------------

// GetNumberingConfig GET /api/tenant/workflows/{id}/numbering
func (h *WorkflowOps) GetNumberingConfig(w http.ResponseWriter, r *http.Request) {
	pool, _, _, ok := h.authorize(w, r, authz.ResourceWorkflowConfig, authz.ActionRead)
	if !ok {
		return
	}
	cfg, err := workflow.GetNumberingConfig(r.Context(), pool, r.PathValue("id"))
	if errors.Is(err, workflow.ErrWorkflowNotFound) {
		fail(w, http.StatusNotFound, "Workflow not found.")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load numbering config.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "numbering": cfg})
}

// SetNumberingConfig PUT /api/tenant/workflows/{id}/numbering
func (h *WorkflowOps) SetNumberingConfig(w http.ResponseWriter, r *http.Request) {
	pool, _, _, ok := h.authorize(w, r, authz.ResourceWorkflowConfig, authz.ActionConfigure)
	if !ok {
		return
	}
	var req struct {
		Enabled    bool   `json:"enabled"`
		Prefix     string `json:"prefix"`
		Suffix     string `json:"suffix"`
		MinDigits  int    `json:"minDigits"`
		NextNumber int64  `json:"nextNumber"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	cfg := workflow.NumberingConfig{
		WorkflowID: r.PathValue("id"),
		Enabled:    req.Enabled,
		Prefix:     req.Prefix,
		Suffix:     req.Suffix,
		MinDigits:  req.MinDigits,
		NextNumber: req.NextNumber,
	}
	if err := workflow.UpsertNumberingConfig(r.Context(), pool, cfg); err != nil {
		if errors.Is(err, workflow.ErrWorkflowNotFound) {
			fail(w, http.StatusNotFound, "Workflow not found.")
			return
		}
		var ve workflow.ValidationErrors
		if errors.As(err, &ve) {
			fail(w, http.StatusBadRequest, ve.Error())
			return
		}
		fail(w, http.StatusInternalServerError, "Failed to save numbering config.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "numbering": cfg})
}

// ---- custom field definitions ----------------------------------------------

// CreateField POST /api/tenant/workflows/{id}/fields
func (h *WorkflowOps) CreateField(w http.ResponseWriter, r *http.Request) {
	pool, _, _, ok := h.authorize(w, r, authz.ResourceWorkflowConfig, authz.ActionConfigure)
	if !ok {
		return
	}
	var f workflow.FieldDefinition
	if err := json.NewDecoder(r.Body).Decode(&f); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	f.WorkflowID = r.PathValue("id")
	id, err := workflow.CreateField(r.Context(), pool, f)
	if err != nil {
		if errors.Is(err, workflow.ErrFieldCap) {
			fail(w, http.StatusConflict, err.Error())
			return
		}
		// Validation errors are caller errors.
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "id": id})
}

// DeleteField DELETE /api/tenant/workflows/{id}/fields/{fieldId}
func (h *WorkflowOps) DeleteField(w http.ResponseWriter, r *http.Request) {
	pool, _, _, ok := h.authorize(w, r, authz.ResourceWorkflowConfig, authz.ActionConfigure)
	if !ok {
		return
	}
	if err := workflow.DeleteField(r.Context(), pool, r.PathValue("id"), r.PathValue("fieldId")); err != nil {
		fail(w, http.StatusNotFound, "Field not found.")
		return
	}
	writeJSON(w, http.StatusOK, models.APIResponse{Success: true, Message: "Field deleted."})
}

// ---- records ----------------------------------------------------------------

// ListRecords GET /api/tenant/workflows/{id}/records — scope-filtered.
func (h *WorkflowOps) ListRecords(w http.ResponseWriter, r *http.Request) {
	pool, scope, identityID, ok := h.authorize(w, r, authz.ResourceRecord, authz.ActionRead)
	if !ok {
		return
	}
	callerUserID, teamIDs := h.callerScope(r, pool, identityID, scope)
	records, err := workflow.ListRecords(r.Context(), pool, r.PathValue("id"), string(scope), callerUserID, teamIDs)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to list records.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "scope": scope, "records": records})
}

type createRecordRequest struct {
	OwnerUserID  string         `json:"ownerUserId"`
	TeamID       string         `json:"teamId"`
	CoreFields   map[string]any `json:"coreFields"`
	CustomFields map[string]any `json:"customFields"`
}

// CreateRecord POST /api/tenant/workflows/{id}/records
func (h *WorkflowOps) CreateRecord(w http.ResponseWriter, r *http.Request) {
	pool, _, identityID, ok := h.authorize(w, r, authz.ResourceRecord, authz.ActionCreate)
	if !ok {
		return
	}
	var req createRecordRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	def, err := workflow.LoadDefinition(r.Context(), pool, r.PathValue("id"))
	if errors.Is(err, workflow.ErrWorkflowNotFound) {
		fail(w, http.StatusNotFound, "Workflow not found.")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load workflow.")
		return
	}
	// Default the owner to the caller when not specified.
	owner := req.OwnerUserID
	if owner == "" {
		if uid, err := workflow.UserIDByIdentity(r.Context(), pool, identityID); err == nil {
			owner = uid
		}
	}
	rec, err := h.engine.CreateRecord(r.Context(), pool, def, owner, req.TeamID, req.CoreFields, req.CustomFields)
	if err != nil {
		if isWorkflowClientErr(err) {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
		fail(w, http.StatusInternalServerError, "Failed to create record.")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "record": rec})
}

// GetRecord GET /api/tenant/records/{id}
func (h *WorkflowOps) GetRecord(w http.ResponseWriter, r *http.Request) {
	pool, _, _, ok := h.authorize(w, r, authz.ResourceRecord, authz.ActionRead)
	if !ok {
		return
	}
	rec, err := workflow.GetRecord(r.Context(), pool, r.PathValue("id"))
	if errors.Is(err, workflow.ErrRecordNotFound) {
		fail(w, http.StatusNotFound, "Record not found.")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load record.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "record": rec})
}

// UpdateRecord PATCH /api/tenant/records/{id} — replaces custom fields (validated).
func (h *WorkflowOps) UpdateRecord(w http.ResponseWriter, r *http.Request) {
	pool, _, _, ok := h.authorize(w, r, authz.ResourceRecord, authz.ActionUpdate)
	if !ok {
		return
	}
	var req struct {
		CustomFields map[string]any `json:"customFields"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	rec, err := workflow.GetRecord(r.Context(), pool, r.PathValue("id"))
	if errors.Is(err, workflow.ErrRecordNotFound) {
		fail(w, http.StatusNotFound, "Record not found.")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load record.")
		return
	}
	def, err := workflow.LoadDefinition(r.Context(), pool, rec.WorkflowID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load workflow.")
		return
	}
	if err := workflow.ValidateCustomFields(def.Fields, req.CustomFields); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := workflow.UpdateRecordFields(r.Context(), pool, rec.ID, req.CustomFields); err != nil {
		fail(w, http.StatusInternalServerError, "Failed to update record.")
		return
	}
	writeJSON(w, http.StatusOK, models.APIResponse{Success: true, Message: "Record updated."})
}

// TransitionRecord POST /api/tenant/records/{id}/transition  body {"toStateId":"..."}
func (h *WorkflowOps) TransitionRecord(w http.ResponseWriter, r *http.Request) {
	pool, _, identityID, ok := h.authorize(w, r, authz.ResourceRecord, authz.ActionTransition)
	if !ok {
		return
	}
	var req struct {
		ToStateID string `json:"toStateId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ToStateID == "" {
		fail(w, http.StatusBadRequest, "toStateId is required.")
		return
	}
	rec, err := workflow.GetRecord(r.Context(), pool, r.PathValue("id"))
	if errors.Is(err, workflow.ErrRecordNotFound) {
		fail(w, http.StatusNotFound, "Record not found.")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load record.")
		return
	}
	def, err := workflow.LoadDefinition(r.Context(), pool, rec.WorkflowID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load workflow.")
		return
	}

	// Optional per-transition permission refinement: if the matching transition
	// declares a required_permission, enforce it on top of record:transition.
	if t, ferr := h.engine.ValidateTransition(def, rec, req.ToStateID); ferr == nil && t.RequiredPermission != "" {
		if res, act, okp := splitPermission(t.RequiredPermission); okp {
			d, cerr := authz.Check(r.Context(), pool, identityID, res, act)
			if cerr != nil || !d.Allowed {
				fail(w, http.StatusForbidden, "This transition requires "+t.RequiredPermission+".")
				return
			}
		}
	}

	actorUserID, _ := workflow.UserIDByIdentity(r.Context(), pool, identityID)
	updated, err := h.engine.Apply(r.Context(), pool, def, rec, req.ToStateID, actorUserID)
	if err != nil {
		if isWorkflowClientErr(err) {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
		fail(w, http.StatusInternalServerError, "Failed to apply transition.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "record": updated})
}

// ---- helpers ----------------------------------------------------------------

// callerScope resolves the caller's tenant user id and team ids when needed for
// team/own scope filtering. For "all" scope it returns empties (no filtering).
func (h *WorkflowOps) callerScope(r *http.Request, pool *pgxpool.Pool, identityID string, scope authz.Scope) (string, []string) {
	if scope == authz.ScopeAll {
		return "", nil
	}
	userID, err := workflow.UserIDByIdentity(r.Context(), pool, identityID)
	if err != nil {
		return "", nil
	}
	if scope == authz.ScopeTeam {
		teams, _ := workflow.TeamIDsForUser(r.Context(), pool, userID)
		return userID, teams
	}
	return userID, nil
}

// isWorkflowClientErr reports whether err is a caller-facing workflow error
// (validation / illegal transition) versus an infrastructure failure.
func isWorkflowClientErr(err error) bool {
	var te workflow.TransitionError
	if errors.As(err, &te) {
		return true
	}
	var ve workflow.ValidationErrors
	return errors.As(err, &ve)
}

// splitPermission parses "resource:action" into typed values.
func splitPermission(p string) (authz.Resource, authz.Action, bool) {
	for i := 0; i < len(p); i++ {
		if p[i] == ':' {
			return authz.Resource(p[:i]), authz.Action(p[i+1:]), true
		}
	}
	return "", "", false
}
