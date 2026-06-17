package controllers

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/crmstore"
	"stonesuite-backend/middleware"
	"stonesuite-backend/models"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/workflow"
)

// CRMOps handles the unified CRM endpoints for Lead, Prospect, and Customer.
// All three are one logical entity distinguished by the workflowKey path
// segment. Persistence is delegated to a crmstore.Store chosen per request from
// the tenant's design_version, so the same endpoints serve either database
// design (v1 JSONB workflow_records or v2 relational crm_record) identically.
//
// Routes:
//
//	GET    /api/tenant/crm/statuses                         — all CRM statuses
//	GET    /api/tenant/crm/{workflowKey}/statuses           — create-form statuses
//	GET    /api/tenant/crm/{workflowKey}/records            — list records
//	POST   /api/tenant/crm/{workflowKey}/records            — create record
//	GET    /api/tenant/crm/records/{id}                     — get record
//	PATCH  /api/tenant/crm/records/{id}                     — update record
//	DELETE /api/tenant/crm/records/{id}                     — delete record
//	GET    /api/tenant/crm/records/{id}/transitions         — edit-form statuses
//	POST   /api/tenant/crm/records/{id}/transition          — apply a transition
//	POST   /api/tenant/crm/records/{id}/convert             — convert to next stage
//	POST   /api/tenant/crm/records/{id}/approve             — approve a Closed-Won customer
type CRMOps struct{}

// NewCRMOps constructs the handler group.
func NewCRMOps() *CRMOps { return &CRMOps{} }

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

// storeFromContext returns the CRM store for the request's tenant design version.
func storeFromContext(r *http.Request) (crmstore.Store, *pgxpool.Pool, error) {
	t, err := tenancy.TenantFromContext(r.Context())
	if err != nil {
		return nil, nil, err
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		return nil, nil, err
	}
	return crmstore.For(t.DesignVersion), pool, nil
}

// authCRM resolves JWT + tenant store/pool + RBAC for a CRM request, deriving
// the resource from the workflowKey. Returns store, pool, identityID, scope, ok.
func (h *CRMOps) authCRM(w http.ResponseWriter, r *http.Request,
	workflowKey string, action authz.Action) (crmstore.Store, *pgxpool.Pool, string, authz.Scope, bool) {

	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return nil, nil, "", "", false
	}
	st, pool, err := storeFromContext(r)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return nil, nil, "", "", false
	}
	resource := resourceForKey(workflowKey)
	decision, err := authz.Check(r.Context(), pool, payload.ID, resource, action)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, nil, "", "", false
	}
	if !decision.Allowed {
		fail(w, http.StatusForbidden,
			"You do not have permission to "+string(action)+" "+workflowKey+".")
		return nil, nil, "", "", false
	}
	return st, pool, payload.ID, decision.Scope, true
}

// authCRMByRecordID resolves auth for record-level actions where the workflow
// key is not in the path. It derives the key from the record, then checks RBAC.
func (h *CRMOps) authCRMByRecordID(w http.ResponseWriter, r *http.Request,
	recordID string, action authz.Action) (crmstore.Store, *pgxpool.Pool, string, string, bool) {

	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return nil, nil, "", "", false
	}
	st, pool, err := storeFromContext(r)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return nil, nil, "", "", false
	}
	key, err := st.KeyForRecord(r.Context(), pool, recordID)
	if errors.Is(err, crmstore.ErrRecordNotFound) {
		fail(w, http.StatusNotFound, "Record not found.")
		return nil, nil, "", "", false
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load record.")
		return nil, nil, "", "", false
	}
	resource := resourceForKey(key)
	decision, err := authz.Check(r.Context(), pool, payload.ID, resource, action)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, nil, "", "", false
	}
	if !decision.Allowed {
		fail(w, http.StatusForbidden,
			"You do not have permission to "+string(action)+" "+key+".")
		return nil, nil, "", "", false
	}
	return st, pool, key, payload.ID, true
}

// crmFail maps a store error to an HTTP response (400 for client errors).
func crmFail(w http.ResponseWriter, err error, serverMsg string) {
	switch {
	case errors.Is(err, crmstore.ErrRecordNotFound):
		fail(w, http.StatusNotFound, "Record not found.")
	case errors.Is(err, crmstore.ErrNotApprover):
		fail(w, http.StatusForbidden, err.Error())
	case crmstore.IsClientError(err):
		fail(w, http.StatusBadRequest, err.Error())
	default:
		fail(w, http.StatusInternalServerError, serverMsg)
	}
}

// ---- status endpoints -------------------------------------------------------

// AllStatuses GET /api/tenant/crm/statuses
func (h *CRMOps) AllStatuses(w http.ResponseWriter, r *http.Request) {
	st, pool, _, _, ok := h.authCRM(w, r, "lead", authz.ActionRead)
	if !ok {
		return
	}
	statuses, err := st.AllStatuses(r.Context(), pool)
	if err != nil {
		crmFail(w, err, "Failed to load CRM statuses.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "statuses": statuses})
}

// WorkflowStatuses GET /api/tenant/crm/{workflowKey}/statuses
// Returns the statuses shown on the CREATE form for the workflow (its own stage).
func (h *CRMOps) WorkflowStatuses(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("workflowKey")
	st, pool, _, _, ok := h.authCRM(w, r, key, authz.ActionRead)
	if !ok {
		return
	}
	statuses, err := st.Statuses(r.Context(), pool, key)
	if err != nil {
		crmFail(w, err, "Failed to load statuses.")
		return
	}
	resp := map[string]any{"success": true, "statuses": statuses}
	// Include workflow metadata for the UI envelope when available (both designs
	// keep the workflows table seeded).
	if wf, werr := workflow.GetWorkflowByKey(r.Context(), pool, key); werr == nil {
		resp["workflow"] = wf
	} else {
		resp["workflow"] = map[string]any{"key": key}
	}
	writeJSON(w, http.StatusOK, resp)
}

// ---- record list / create ---------------------------------------------------

// ListRecords GET /api/tenant/crm/{workflowKey}/records
func (h *CRMOps) ListRecords(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("workflowKey")
	st, pool, identityID, scope, ok := h.authCRM(w, r, key, authz.ActionRead)
	if !ok {
		return
	}
	records, err := st.ListRecords(r.Context(), pool, key, string(scope), identityID)
	if err != nil {
		log.Printf("crm: ListRecords(%s, scope=%s): %v", key, scope, err)
		crmFail(w, err, "Failed to list records.")
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
	CrmStatusID  string         `json:"crmStatusId"` // optional chosen status (v2)
	CoreFields   map[string]any `json:"coreFields"`
	CustomFields map[string]any `json:"customFields"`
}

// CreateRecord POST /api/tenant/crm/{workflowKey}/records
// A prospect or customer may be created directly without a prior lead/prospect.
func (h *CRMOps) CreateRecord(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("workflowKey")
	st, pool, identityID, _, ok := h.authCRM(w, r, key, authz.ActionCreate)
	if !ok {
		return
	}
	var req crmCreateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	rec, err := st.CreateRecord(r.Context(), pool, key, crmstore.CreateInput{
		ActorIdentityID: identityID,
		OwnerUserID:     req.OwnerUserID,
		TeamID:          req.TeamID,
		CrmStatusID:     req.CrmStatusID,
		CoreFields:      req.CoreFields,
		CustomFields:    req.CustomFields,
	})
	if err != nil {
		crmFail(w, err, "Failed to create record.")
		return
	}
	auditCRM(r, pool, identityID, "create", key, rec.ID, nil, rec)
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "record": rec})
}

// ---- single record CRUD -----------------------------------------------------

// GetRecord GET /api/tenant/crm/records/{id}
func (h *CRMOps) GetRecord(w http.ResponseWriter, r *http.Request) {
	st, pool, _, _, ok := h.authCRMByRecordID(w, r, r.PathValue("id"), authz.ActionRead)
	if !ok {
		return
	}
	rec, err := st.GetRecord(r.Context(), pool, r.PathValue("id"))
	if err != nil {
		crmFail(w, err, "Failed to load record.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "record": rec})
}

type crmUpdateRequest struct {
	CoreFields   map[string]any `json:"coreFields"`
	CustomFields map[string]any `json:"customFields"`
}

// UpdateRecord PATCH /api/tenant/crm/records/{id}
func (h *CRMOps) UpdateRecord(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	st, pool, key, identityID, ok := h.authCRMByRecordID(w, r, id, authz.ActionUpdate)
	if !ok {
		return
	}
	var req crmUpdateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	before, err := st.GetRecord(r.Context(), pool, id)
	if err != nil {
		log.Printf("warn: could not fetch record for audit pre-image: %v", err)
	}
	if err := st.UpdateRecord(r.Context(), pool, id, req.CoreFields, req.CustomFields); err != nil {
		crmFail(w, err, "Failed to update record.")
		return
	}
	after, _ := st.GetRecord(r.Context(), pool, id)
	auditCRM(r, pool, identityID, "update", key, id, before, after)
	writeJSON(w, http.StatusOK, models.APIResponse{Success: true, Message: "Record updated."})
}

// DeleteRecord DELETE /api/tenant/crm/records/{id}
func (h *CRMOps) DeleteRecord(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	st, pool, key, identityID, ok := h.authCRMByRecordID(w, r, id, authz.ActionDelete)
	if !ok {
		return
	}
	before, _ := st.GetRecord(r.Context(), pool, id)
	if err := st.DeleteRecord(r.Context(), pool, id); err != nil {
		crmFail(w, err, "Failed to delete record.")
		return
	}
	auditCRM(r, pool, identityID, "delete", key, id, before, nil)
	writeJSON(w, http.StatusOK, models.APIResponse{Success: true, Message: "Record deleted."})
}

// ---- transitions ------------------------------------------------------------

// AvailableTransitions GET /api/tenant/crm/records/{id}/transitions
// Returns the statuses shown on the EDIT form (forward-stage statuses).
func (h *CRMOps) AvailableTransitions(w http.ResponseWriter, r *http.Request) {
	st, pool, _, _, ok := h.authCRMByRecordID(w, r, r.PathValue("id"), authz.ActionRead)
	if !ok {
		return
	}
	transitions, err := st.AvailableTransitions(r.Context(), pool, r.PathValue("id"))
	if err != nil {
		crmFail(w, err, "Failed to load transitions.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":     true,
		"recordId":    r.PathValue("id"),
		"transitions": transitions,
	})
}

// TransitionRecord POST /api/tenant/crm/records/{id}/transition  body {"toStateId":"..."}
func (h *CRMOps) TransitionRecord(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	st, pool, key, identityID, ok := h.authCRMByRecordID(w, r, id, authz.ActionTransition)
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
	updated, err := st.TransitionRecord(r.Context(), pool, id, req.ToStateID, identityID)
	if err != nil {
		crmFail(w, err, "Failed to apply transition.")
		return
	}
	auditCRM(r, pool, identityID, "transition", key, id, nil, updated)
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
func (h *CRMOps) ConvertRecord(w http.ResponseWriter, r *http.Request) {
	st, pool, _, identityID, ok := h.authCRMByRecordID(w, r, r.PathValue("id"), authz.ActionCreate)
	if !ok {
		return
	}
	var req convertRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.TargetWorkflowKey == "" {
		fail(w, http.StatusBadRequest, "targetWorkflowKey is required.")
		return
	}
	newRec, sourceID, err := st.ConvertRecord(r.Context(), pool, r.PathValue("id"),
		req.TargetWorkflowKey, req.CoreFields, req.CustomFields, identityID)
	if err != nil {
		crmFail(w, err, "Failed to convert record.")
		return
	}
	auditCRM(r, pool, identityID, "convert", req.TargetWorkflowKey, newRec.ID, nil, newRec)
	writeJSON(w, http.StatusCreated, map[string]any{
		"success":        true,
		"record":         newRec,
		"sourceRecordId": sourceID,
	})
}

// ---- approval ---------------------------------------------------------------

// ApproveRecord POST /api/tenant/crm/records/{id}/approve
// Approves a Closed-Won customer if the caller is a configured approver. Only
// supported on the v2 design; v1 returns 400 (not supported).
func (h *CRMOps) ApproveRecord(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	st, pool, key, identityID, ok := h.authCRMByRecordID(w, r, id, authz.ActionUpdate)
	if !ok {
		return
	}
	rec, err := st.Approve(r.Context(), pool, id, identityID)
	if errors.Is(err, crmstore.ErrNotSupported) {
		fail(w, http.StatusBadRequest, "Approval is not available for this workspace's design.")
		return
	}
	if err != nil {
		crmFail(w, err, "Failed to approve record.")
		return
	}
	auditCRM(r, pool, identityID, "approve", key, id, nil, rec)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "record": rec})
}
