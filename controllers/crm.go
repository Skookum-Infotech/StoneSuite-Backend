package controllers

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/middleware"
	"stonesuite-backend/models"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/workflow"
)

// CRMOps handles the unified CRM endpoints for Lead, Prospect, and Customer
// entities. All three entity types are stored in workflow_records; the
// workflowKey path segment selects which CRM workflow the request targets.
//
// Routes:
//
//	GET  /api/tenant/crm/statuses                         — all CRM statuses (dropdown)
//	GET  /api/tenant/crm/{workflowKey}/statuses           — statuses for one workflow
//	GET  /api/tenant/crm/{workflowKey}/records            — list records
//	POST /api/tenant/crm/{workflowKey}/records            — create record
//	GET  /api/tenant/crm/records/{id}                     — get record
//	GET  /api/tenant/crm/records/{id}/transitions         — available transitions
//	PATCH /api/tenant/crm/records/{id}                    — update record
//	DELETE /api/tenant/crm/records/{id}                   — delete record
//	POST /api/tenant/crm/records/{id}/transition          — apply a transition
//	POST /api/tenant/crm/records/{id}/convert             — convert to next CRM stage
type CRMOps struct {
	engine *workflow.Engine
}

// NewCRMOps constructs the handler group.
func NewCRMOps() *CRMOps { return &CRMOps{engine: workflow.NewEngine()} }

// resourceForKey maps a workflow key to the RBAC resource used for auth.
func resourceForKey(key string) authz.Resource {
	switch key {
	case "lead":
		return authz.ResourceLead
	case "prospect":
		return authz.ResourceProspect
	case "customer":
		return authz.ResourceCustomer
	case "estimate":
		return authz.ResourceEstimate
	case "quote":
		return authz.ResourceQuote
	case "sales_order":
		return authz.ResourceSalesOrder
	case "installation":
		return authz.ResourceInstallation
	case "invoice":
		return authz.ResourceInvoice
	case "payment":
		return authz.ResourcePayment
	case "credit_memo":
		return authz.ResourceCreditMemo
	case "refund":
		return authz.ResourceRefund
	case "vendor":
		return authz.ResourceVendor
	case "requisition":
		return authz.ResourceRequisition
	case "purchase_order":
		return authz.ResourcePurchaseOrder
	case "item_receipt":
		return authz.ResourceItemReceipt
	case "vendor_bill":
		return authz.ResourceVendorBill
	case "vendor_payment":
		return authz.ResourceVendorPayment
	case "vendor_credit":
		return authz.ResourceVendorCredit
	case "expense":
		return authz.ResourceExpense
	default:
		return authz.ResourceRecord
	}
}

// authCRM resolves JWT + tenant pool + RBAC for a CRM request, deriving the
// resource from the workflowKey. Returns pool, identityID, scope, ok.
func (h *CRMOps) authCRM(w http.ResponseWriter, r *http.Request,
	workflowKey string, action authz.Action) (*pgxpool.Pool, string, authz.Scope, bool) {

	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return nil, "", "", false
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		log.Printf("crm: authCRM(%s): pool error: %v", workflowKey, err)
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return nil, "", "", false
	}
	resource := resourceForKey(workflowKey)
	decision, err := authz.Check(r.Context(), pool, payload.ID, resource, action)
	if err != nil {
		log.Printf("crm: authCRM(%s): permission check error: %v", workflowKey, err)
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, "", "", false
	}
	if !decision.Allowed {
		fail(w, http.StatusForbidden,
			"You do not have permission to "+string(action)+" "+workflowKey+".")
		return nil, "", "", false
	}
	return pool, payload.ID, decision.Scope, true
}

// authCRMByRecordID resolves auth for record-level actions where the workflow
// key is not in the path. It loads the record first, derives the workflow key
// from the workflow definition, then checks RBAC.
func (h *CRMOps) authCRMByRecordID(w http.ResponseWriter, r *http.Request,
	recordID string, action authz.Action) (*pgxpool.Pool, *workflow.Record, string, bool) {

	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return nil, nil, "", false
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return nil, nil, "", false
	}
	rec, err := workflow.GetRecord(r.Context(), pool, recordID)
	if errors.Is(err, workflow.ErrRecordNotFound) {
		fail(w, http.StatusNotFound, "Record not found.")
		return nil, nil, "", false
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load record.")
		return nil, nil, "", false
	}
	wf, err := workflow.LoadDefinition(r.Context(), pool, rec.WorkflowID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load workflow.")
		return nil, nil, "", false
	}
	resource := resourceForKey(wf.Workflow.Key)
	decision, err := authz.Check(r.Context(), pool, payload.ID, resource, action)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, nil, "", false
	}
	if !decision.Allowed {
		fail(w, http.StatusForbidden,
			"You do not have permission to "+string(action)+" "+wf.Workflow.Key+".")
		return nil, nil, "", false
	}
	return pool, rec, payload.ID, true
}

// ---- status endpoints -------------------------------------------------------

// AllStatuses GET /api/tenant/crm/statuses
// Returns all states across the CRM pipeline (lead→prospect→customer),
// suitable for a combined filter/status dropdown in the UI.
func (h *CRMOps) AllStatuses(w http.ResponseWriter, r *http.Request) {
	pool, _, _, ok := h.authCRM(w, r, "lead", authz.ActionRead)
	if !ok {
		return
	}
	statuses, err := workflow.ListCRMStatuses(r.Context(), pool)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load CRM statuses.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "statuses": statuses})
}

// WorkflowStatuses GET /api/tenant/crm/{workflowKey}/statuses
// Returns only the states for a specific CRM workflow. On creation, the
// client can use this to know the initial state; on edit, use /transitions.
func (h *CRMOps) WorkflowStatuses(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("workflowKey")
	pool, _, _, ok := h.authCRM(w, r, key, authz.ActionRead)
	if !ok {
		return
	}
	wf, err := workflow.GetWorkflowByKey(r.Context(), pool, key)
	if errors.Is(err, workflow.ErrWorkflowNotFound) {
		fail(w, http.StatusNotFound, "Workflow not found.")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load workflow.")
		return
	}
	def, err := workflow.LoadDefinition(r.Context(), pool, wf.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load workflow definition.")
		return
	}
	statuses := make([]workflow.StatusInfo, 0, len(def.States))
	for _, s := range def.States {
		statuses = append(statuses, workflow.StatusInfo{
			StateID:      s.ID,
			StateKey:     s.Key,
			StatusLabel:  s.Name,
			WorkflowKey:  def.Workflow.Key,
			WorkflowName: def.Workflow.Name,
			IsInitial:    s.IsInitial,
			IsTerminal:   s.IsTerminal,
			SortOrder:    s.SortOrder,
			Color:        s.Color,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":  true,
		"workflow": def.Workflow,
		"statuses": statuses,
	})
}

// ---- record list / create ---------------------------------------------------

// ListRecords GET /api/tenant/crm/{workflowKey}/records
func (h *CRMOps) ListRecords(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("workflowKey")
	pool, identityID, scope, ok := h.authCRM(w, r, key, authz.ActionRead)
	if !ok {
		return
	}
	wf, err := workflow.GetWorkflowByKey(r.Context(), pool, key)
	if errors.Is(err, workflow.ErrWorkflowNotFound) {
		fail(w, http.StatusNotFound, "Workflow not found.")
		return
	}
	if err != nil {
		log.Printf("crm: ListRecords: GetWorkflowByKey(%s): %v", key, err)
		fail(w, http.StatusInternalServerError, "Failed to load workflow.")
		return
	}
	callerUserID, teamIDs := crmCallerScope(r, pool, identityID, scope)
	records, err := workflow.ListRecords(r.Context(), pool, wf.ID, string(scope), callerUserID, teamIDs)
	if err != nil {
		log.Printf("crm: ListRecords(%s, workflowId=%s, scope=%s): %v", key, wf.ID, scope, err)
		fail(w, http.StatusInternalServerError, "Failed to list records.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"scope":   scope,
		"records": records,
	})
}

type crmCreateRequest struct {
	OwnerUserID  string         `json:"ownerUserId"`
	TeamID       string         `json:"teamId"`
	CoreFields   map[string]any `json:"coreFields"`
	CustomFields map[string]any `json:"customFields"`
}

// CreateRecord POST /api/tenant/crm/{workflowKey}/records
// Creates a new record in the target workflow's initial state. A prospect or
// customer can be created directly without a prior lead or prospect record.
func (h *CRMOps) CreateRecord(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("workflowKey")
	pool, identityID, _, ok := h.authCRM(w, r, key, authz.ActionCreate)
	if !ok {
		return
	}
	var req crmCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	wf, err := workflow.GetWorkflowByKey(r.Context(), pool, key)
	if errors.Is(err, workflow.ErrWorkflowNotFound) {
		fail(w, http.StatusNotFound, "Workflow not found.")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load workflow.")
		return
	}
	def, err := workflow.LoadDefinition(r.Context(), pool, wf.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load workflow definition.")
		return
	}
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

// ---- single record CRUD -----------------------------------------------------

// GetRecord GET /api/tenant/crm/records/{id}
func (h *CRMOps) GetRecord(w http.ResponseWriter, r *http.Request) {
	pool, rec, _, ok := h.authCRMByRecordID(w, r, r.PathValue("id"), authz.ActionRead)
	if !ok {
		return
	}
	_ = pool
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "record": rec})
}

type crmUpdateRequest struct {
	CoreFields   map[string]any `json:"coreFields"`
	CustomFields map[string]any `json:"customFields"`
}

// UpdateRecord PATCH /api/tenant/crm/records/{id}
// Replaces core_fields and/or custom_fields. Validates custom fields against
// the workflow's field definitions before saving.
func (h *CRMOps) UpdateRecord(w http.ResponseWriter, r *http.Request) {
	pool, rec, _, ok := h.authCRMByRecordID(w, r, r.PathValue("id"), authz.ActionUpdate)
	if !ok {
		return
	}
	var req crmUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	def, err := workflow.LoadDefinition(r.Context(), pool, rec.WorkflowID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load workflow.")
		return
	}
	// Merge incoming custom fields onto existing ones then validate.
	merged := rec.CustomFields
	if merged == nil {
		merged = map[string]any{}
	}
	for k, v := range req.CustomFields {
		merged[k] = v
	}
	if err := workflow.ValidateCustomFields(def.Fields, merged); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	// Merge incoming core fields onto existing ones.
	mergedCore := rec.CoreFields
	if mergedCore == nil {
		mergedCore = map[string]any{}
	}
	for k, v := range req.CoreFields {
		mergedCore[k] = v
	}
	if err := workflow.UpdateRecordAllFields(r.Context(), pool, rec.ID, mergedCore, merged); err != nil {
		fail(w, http.StatusInternalServerError, "Failed to update record.")
		return
	}
	writeJSON(w, http.StatusOK, models.APIResponse{Success: true, Message: "Record updated."})
}

// DeleteRecord DELETE /api/tenant/crm/records/{id}
func (h *CRMOps) DeleteRecord(w http.ResponseWriter, r *http.Request) {
	pool, rec, _, ok := h.authCRMByRecordID(w, r, r.PathValue("id"), authz.ActionDelete)
	if !ok {
		return
	}
	if err := workflow.DeleteRecord(r.Context(), pool, rec.ID); err != nil {
		fail(w, http.StatusInternalServerError, "Failed to delete record.")
		return
	}
	writeJSON(w, http.StatusOK, models.APIResponse{Success: true, Message: "Record deleted."})
}

// ---- transitions ------------------------------------------------------------

// AvailableTransitions GET /api/tenant/crm/records/{id}/transitions
// Returns the states reachable from the record's current state. Clients use
// this to build the transition dropdown on the edit/detail view.
func (h *CRMOps) AvailableTransitions(w http.ResponseWriter, r *http.Request) {
	pool, rec, _, ok := h.authCRMByRecordID(w, r, r.PathValue("id"), authz.ActionRead)
	if !ok {
		return
	}
	transitions, err := workflow.AvailableTransitions(r.Context(), pool, rec)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load transitions.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":     true,
		"recordId":    rec.ID,
		"transitions": transitions,
	})
}

// TransitionRecord POST /api/tenant/crm/records/{id}/transition  body {"toStateId":"..."}
func (h *CRMOps) TransitionRecord(w http.ResponseWriter, r *http.Request) {
	pool, rec, identityID, ok := h.authCRMByRecordID(w, r, r.PathValue("id"), authz.ActionTransition)
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
	def, err := workflow.LoadDefinition(r.Context(), pool, rec.WorkflowID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load workflow.")
		return
	}
	// Enforce per-transition permission refinement if declared.
	if t, ferr := h.engine.ValidateTransition(def, rec, req.ToStateID); ferr == nil && t.RequiredPermission != "" {
		if res, act, ok := splitPermission(t.RequiredPermission); ok {
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

// ---- conversion (lead → prospect → customer) --------------------------------

type convertRequest struct {
	TargetWorkflowKey string         `json:"targetWorkflowKey"` // "prospect" or "customer"
	CoreFields        map[string]any `json:"coreFields"`
	CustomFields      map[string]any `json:"customFields"`
	TeamID            string         `json:"teamId"`
}

// ConvertRecord POST /api/tenant/crm/records/{id}/convert
// Creates a new record in the next CRM workflow stage (e.g. lead → prospect)
// linked via parent_record_id. The source record is NOT deleted or transitioned;
// callers should explicitly transition or delete it as needed.
func (h *CRMOps) ConvertRecord(w http.ResponseWriter, r *http.Request) {
	pool, sourceRec, identityID, ok := h.authCRMByRecordID(w, r, r.PathValue("id"), authz.ActionCreate)
	if !ok {
		return
	}
	var req convertRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.TargetWorkflowKey == "" {
		fail(w, http.StatusBadRequest, "targetWorkflowKey is required.")
		return
	}
	targetWF, err := workflow.GetWorkflowByKey(r.Context(), pool, req.TargetWorkflowKey)
	if errors.Is(err, workflow.ErrWorkflowNotFound) {
		fail(w, http.StatusNotFound, "Target workflow not found.")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load target workflow.")
		return
	}
	targetDef, err := workflow.LoadDefinition(r.Context(), pool, targetWF.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load target workflow definition.")
		return
	}
	// Seed new record core fields from source if caller didn't provide them.
	core := req.CoreFields
	if core == nil {
		core = map[string]any{}
	}
	for k, v := range sourceRec.CoreFields {
		if _, exists := core[k]; !exists {
			core[k] = v
		}
	}

	// Seed target custom fields from the source record's core and custom fields,
	// but only for keys that are defined in the target workflow. This allows
	// values like company_name (stored in core_fields on the source) to satisfy
	// required field_definitions on the target without the UI having to re-send them.
	custom := req.CustomFields
	if custom == nil {
		custom = map[string]any{}
	}
	targetFieldSet := make(map[string]struct{}, len(targetDef.Fields))
	for _, f := range targetDef.Fields {
		targetFieldSet[f.Key] = struct{}{}
	}
	for k, v := range sourceRec.CustomFields {
		if _, inTarget := targetFieldSet[k]; inTarget {
			if _, exists := custom[k]; !exists {
				custom[k] = v
			}
		}
	}
	for k, v := range sourceRec.CoreFields {
		if _, inTarget := targetFieldSet[k]; inTarget {
			if _, exists := custom[k]; !exists {
				custom[k] = v
			}
		}
	}

	owner, _ := workflow.UserIDByIdentity(r.Context(), pool, identityID)
	newRec, err := h.engine.ConvertRecord(r.Context(), pool, targetDef,
		owner, req.TeamID, sourceRec.ID, core, custom)
	if err != nil {
		if isWorkflowClientErr(err) {
			fail(w, http.StatusBadRequest, err.Error())
			return
		}
		fail(w, http.StatusInternalServerError, "Failed to convert record.")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"success":        true,
		"record":         newRec,
		"sourceRecordId": sourceRec.ID,
	})
}

// ---- helpers ----------------------------------------------------------------

func crmCallerScope(r *http.Request, pool *pgxpool.Pool, identityID string, scope authz.Scope) (string, []string) {
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
