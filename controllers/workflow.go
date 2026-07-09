package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/middleware"
	"stonesuite-backend/models"
	"stonesuite-backend/query"
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
	// Best-effort: only CRM workflows (lead/prospect/customer) have configured
	// approvers; leave ApproverUserIds nil for anything else or on lookup error.
	if code, cerr := recordTypeCodeForWorkflow(r.Context(), pool, def.Workflow.ID); cerr == nil {
		if ids, aerr := activeApproverUserIDs(r.Context(), pool, code); aerr == nil {
			def.Workflow.ApproverUserIds = ids
		}
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

// ---- approver config ---------------------------------------------------------

// GetWorkflowApprovers GET /api/tenant/workflows/{id}/approvers
// Returns the user ids currently configured as active approvers for this
// workflow's CRM record type (only lead/prospect/customer support approval;
// today only "customer" Closed-Won records ever reach a pending state).
func (h *WorkflowOps) GetWorkflowApprovers(w http.ResponseWriter, r *http.Request) {
	pool, _, _, ok := h.authorize(w, r, authz.ResourceWorkflowConfig, authz.ActionRead)
	if !ok {
		return
	}
	code, err := recordTypeCodeForWorkflow(r.Context(), pool, r.PathValue("id"))
	switch {
	case errors.Is(err, workflow.ErrWorkflowNotFound):
		fail(w, http.StatusNotFound, "Workflow not found.")
		return
	case errors.Is(err, errNotCRMWorkflow):
		fail(w, http.StatusBadRequest, "This workflow does not support approver configuration.")
		return
	case err != nil:
		fail(w, http.StatusInternalServerError, "Failed to load workflow.")
		return
	}
	ids, err := activeApproverUserIDs(r.Context(), pool, code)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load approvers.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "approverUserIds": ids})
}

// SetWorkflowApprovers PATCH /api/tenant/workflows/{id}/approvers  body {"approverUserIds":["..."]}
// Replaces the full set of active approvers for this workflow's CRM record
// type with exactly the given users. The backend enforces no count cap — the
// 2-approver limit is a UI concern; the backend holds any number.
func (h *WorkflowOps) SetWorkflowApprovers(w http.ResponseWriter, r *http.Request) {
	pool, _, _, ok := h.authorize(w, r, authz.ResourceWorkflowConfig, authz.ActionConfigure)
	if !ok {
		return
	}
	var req struct {
		ApproverUserIDs []string `json:"approverUserIds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	code, err := recordTypeCodeForWorkflow(r.Context(), pool, r.PathValue("id"))
	switch {
	case errors.Is(err, workflow.ErrWorkflowNotFound):
		fail(w, http.StatusNotFound, "Workflow not found.")
		return
	case errors.Is(err, errNotCRMWorkflow):
		fail(w, http.StatusBadRequest, "This workflow does not support approver configuration.")
		return
	case err != nil:
		fail(w, http.StatusInternalServerError, "Failed to load workflow.")
		return
	}
	if err := replaceActiveApprovers(r.Context(), pool, code, req.ApproverUserIDs); err != nil {
		if errors.Is(err, errUnknownApproverUser) {
			fail(w, http.StatusBadRequest, "One or more approverUserIds do not match an active employee.")
			return
		}
		fail(w, http.StatusInternalServerError, "Failed to save approvers.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "approverUserIds": req.ApproverUserIDs})
}

// ---- per-state approver config (generic workflow engine) --------------------

// GetStateApprovers GET /api/tenant/workflows/{id}/states/{stateId}/approvers
// Returns the tenant user ids currently configured as active approvers for the
// given workflow state.
func (h *WorkflowOps) GetStateApprovers(w http.ResponseWriter, r *http.Request) {
	pool, _, _, ok := h.authorize(w, r, authz.ResourceWorkflowConfig, authz.ActionRead)
	if !ok {
		return
	}
	stateID, ok := h.stateInWorkflow(w, r, pool)
	if !ok {
		return
	}
	ids, err := workflow.StateApproverUserIDs(r.Context(), pool, stateID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load approvers.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "approverUserIds": ids})
}

// SetStateApprovers PUT /api/tenant/workflows/{id}/states/{stateId}/approvers
// body {"approverUserIds":["..."]}
// Replaces the state's active approver set with exactly the given users. The
// backend enforces NO count cap — the 2-approver limit is a UI concern; the
// backend holds any number. A state with >=1 approver becomes approval-gated.
func (h *WorkflowOps) SetStateApprovers(w http.ResponseWriter, r *http.Request) {
	pool, _, identityID, ok := h.authorize(w, r, authz.ResourceWorkflowConfig, authz.ActionConfigure)
	if !ok {
		return
	}
	stateID, ok := h.stateInWorkflow(w, r, pool)
	if !ok {
		return
	}
	var req struct {
		ApproverUserIDs []string `json:"approverUserIds"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	ids := dedupeStrings(req.ApproverUserIDs)
	if !allActiveUsers(r.Context(), pool, ids) {
		fail(w, http.StatusBadRequest, "One or more approverUserIds do not match an active user.")
		return
	}
	createdBy, _ := workflow.UserIDByIdentity(r.Context(), pool, identityID)
	if err := workflow.ReplaceStateApprovers(r.Context(), pool, stateID, ids, createdBy); err != nil {
		fail(w, http.StatusInternalServerError, "Failed to save approvers.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "approverUserIds": ids})
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
	approverUserID, _ := workflow.UserIDByIdentity(r.Context(), pool, identityID)
	_ = workflow.AttachApprovalOverlays(r.Context(), pool, records, approverUserID)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "scope": scope, "records": records})
}

// SearchRecords POST /api/tenant/workflows/{id}/records/search — scope-filtered,
// server-side filter + sort + keyset pagination. The caller's RBAC scope is
// composed (ANDed) with the request filter, so a filter can only narrow the
// caller's already-permitted set, never widen it.
func (h *WorkflowOps) SearchRecords(w http.ResponseWriter, r *http.Request) {
	pool, scope, identityID, ok := h.authorize(w, r, authz.ResourceRecord, authz.ActionRead)
	if !ok {
		return
	}
	var req query.Request
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			fail(w, http.StatusBadRequest, "Invalid request body.")
			return
		}
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
	callerUserID, teamIDs := h.callerScope(r, pool, identityID, scope)
	page, err := workflow.ListRecordsFiltered(r.Context(), pool, def.Workflow.ID, string(scope), callerUserID, teamIDs, def.Fields, req)
	if err != nil {
		var ife *query.InvalidFilterError
		if errors.As(err, &ife) {
			fail(w, http.StatusBadRequest, ife.Error())
			return
		}
		fail(w, http.StatusInternalServerError, "Failed to search records.")
		return
	}
	approverUserID, _ := workflow.UserIDByIdentity(r.Context(), pool, identityID)
	_ = workflow.AttachApprovalOverlays(r.Context(), pool, page.Records, approverUserID)
	writeJSON(w, http.StatusOK, map[string]any{
		"success":    true,
		"scope":      scope,
		"records":    page.Records,
		"nextCursor": page.NextCursor,
		"hasMore":    page.HasMore,
	})
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
	pool, scope, identityID, ok := h.authorize(w, r, authz.ResourceRecord, authz.ActionRead)
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
	if !h.enforceRecordScope(w, r, pool, scope, identityID, rec, authz.ActionRead) {
		return
	}
	callerUserID, _ := workflow.UserIDByIdentity(r.Context(), pool, identityID)
	if ov, oerr := workflow.ApprovalOverlay(r.Context(), pool, rec, callerUserID); oerr == nil {
		rec.Approval = ov
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "record": rec})
}

// UpdateRecord PATCH /api/tenant/records/{id} — replaces custom fields (validated).
func (h *WorkflowOps) UpdateRecord(w http.ResponseWriter, r *http.Request) {
	pool, scope, identityID, ok := h.authorize(w, r, authz.ResourceRecord, authz.ActionUpdate)
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
	if !h.enforceRecordScope(w, r, pool, scope, identityID, rec, authz.ActionUpdate) {
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
	pool, scope, identityID, ok := h.authorize(w, r, authz.ResourceRecord, authz.ActionTransition)
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
	if !h.enforceRecordScope(w, r, pool, scope, identityID, rec, authz.ActionTransition) {
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

// ---- record approval --------------------------------------------------------

// ApproveRecord POST /api/tenant/records/{id}/approve
// Records the caller's sign-off on a record that is pending approval in its
// current state. Authorization is the record:approve permission PLUS the domain
// check that the caller is an assigned approver of the current state — the
// owner/team IDOR scope guard is intentionally NOT applied here, because
// approvers legitimately act on records they do not own (mirrors the CRM
// approval posture). A non-approver is denied and the attempt is logged.
func (h *WorkflowOps) ApproveRecord(w http.ResponseWriter, r *http.Request) {
	pool, _, identityID, ok := h.authorize(w, r, authz.ResourceRecord, authz.ActionApprove)
	if !ok {
		return
	}
	id := r.PathValue("id")
	callerUserID, _ := workflow.UserIDByIdentity(r.Context(), pool, identityID)
	updated, err := workflow.Approve(r.Context(), pool, id, callerUserID)
	switch {
	case errors.Is(err, workflow.ErrNotApprover):
		// A non-approver (or a probe for a record the caller can't approve) is
		// answered with 404 — identical to a missing record — so record ids
		// cannot be enumerated. This endpoint intentionally skips the owner/team
		// scope guard, so the approver-membership check is the access boundary.
		logSecurityEvent(r, "approval_denied", "identity", identityID, "record", id)
		fail(w, http.StatusNotFound, "Record not found.")
		return
	case errors.Is(err, workflow.ErrRecordNotFound):
		fail(w, http.StatusNotFound, "Record not found.")
		return
	case errors.Is(err, workflow.ErrAlreadyApproved):
		fail(w, http.StatusConflict, workflow.ErrAlreadyApproved.Error())
		return
	case err != nil:
		fail(w, http.StatusInternalServerError, "Failed to approve record.")
		return
	}
	if ov, oerr := workflow.ApprovalOverlay(r.Context(), pool, updated, callerUserID); oerr == nil {
		updated.Approval = ov
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "record": updated})
}

// PendingApprovalsQueue GET /api/tenant/records/approvals/pending
// Lists records where the caller is an active approver of the record's current
// state and has not yet signed off — the caller's approval queue. Scoped by
// approver assignment, not owner/team.
func (h *WorkflowOps) PendingApprovalsQueue(w http.ResponseWriter, r *http.Request) {
	pool, _, identityID, ok := h.authorize(w, r, authz.ResourceRecord, authz.ActionApprove)
	if !ok {
		return
	}
	callerUserID, _ := workflow.UserIDByIdentity(r.Context(), pool, identityID)
	records, err := workflow.PendingApprovals(r.Context(), pool, callerUserID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load pending approvals.")
		return
	}
	if err := workflow.AttachApprovalOverlays(r.Context(), pool, records, callerUserID); err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load pending approvals.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "records": records})
}

// ---- helpers ----------------------------------------------------------------

// enforceRecordScope is the IDOR guard for WorkflowOps single-record handlers:
// after the resource:action permission passes, it confirms the caller's scope
// (own/team) actually covers THIS record. Returns true to proceed; on denial it
// has already written a 404 (not 403, to avoid id enumeration) and logged the
// attempt, so the caller should just return.
func (h *WorkflowOps) enforceRecordScope(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, scope authz.Scope, identityID string, rec *workflow.Record, action authz.Action) bool {
	allowed, err := recordInScope(r.Context(), pool, scope, identityID, rec.OwnerUserID, rec.TeamID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return false
	}
	if !allowed {
		logSecurityEvent(r, "idor_denied",
			"identity", identityID, "record", rec.ID, "resource", "record",
			"action", string(action), "scope", string(scope))
		fail(w, http.StatusNotFound, "Record not found.")
		return false
	}
	return true
}

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

// stateInWorkflow validates that the {stateId} path param is a state of the
// {id} workflow. On failure it writes the response (404 unknown / 500 error)
// and returns ok=false.
func (h *WorkflowOps) stateInWorkflow(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool) (string, bool) {
	workflowID := r.PathValue("id")
	stateID := r.PathValue("stateId")
	var exists bool
	err := pool.QueryRow(r.Context(),
		`SELECT EXISTS (SELECT 1 FROM workflow_states WHERE id::text = $1 AND workflow_id::text = $2)`,
		stateID, workflowID).Scan(&exists)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load state.")
		return "", false
	}
	if !exists {
		fail(w, http.StatusNotFound, "State not found for this workflow.")
		return "", false
	}
	return stateID, true
}

// allActiveUsers reports whether every id in ids resolves to an active tenant
// user. An empty set is valid (clears the approver set). Malformed ids simply
// fail to match, so the check returns false.
func allActiveUsers(ctx context.Context, pool *pgxpool.Pool, ids []string) bool {
	if len(ids) == 0 {
		return true
	}
	var count int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM users WHERE id::text = ANY($1) AND status = 'active'`, ids).Scan(&count); err != nil {
		return false
	}
	return count == len(ids)
}

// dedupeStrings returns ids with blanks and duplicates removed, order preserved.
func dedupeStrings(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
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
