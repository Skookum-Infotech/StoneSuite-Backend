// controllers/requisition.go
package controllers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/middleware"
	"stonesuite-backend/query"
	"stonesuite-backend/requisition"
	"stonesuite-backend/tenancy"
)

// RequisitionOps handles the Requisition endpoints: a dedicated relational
// module (header + line items + approval + conversion into a Purchase
// Order), the second Purchases document module — a sibling of Purchase
// Order/Estimate/Quote/Invoice, not served through the generic
// /api/tenant/crm/{workflowKey} JSONB router (AD-1). Copied from
// controllers/purchaseorder.go's auth/IDOR/error-mapping skeleton, itself
// mirroring controllers/payment.go.
//
// Routes:
//
//	GET    /api/tenant/requisitions                    — unfiltered list (cursor-paginated)
//	POST   /api/tenant/requisitions/search             — filter + sort + search + pagination
//	POST   /api/tenant/requisitions                    — create
//	GET    /api/tenant/requisitions/{uuid}             — get (+ items)
//	PATCH  /api/tenant/requisitions/{uuid}             — update (DRFT only)
//	DELETE /api/tenant/requisitions/{uuid}             — soft delete (DRFT/CANC only)
//	POST   /api/tenant/requisitions/{uuid}/transition  — status change
//	POST   /api/tenant/requisitions/{uuid}/approve     — approval sign-off
//	POST   /api/tenant/requisitions/{uuid}/convert     — convert to Purchase Order (requisition_convert.go)
//	GET    /api/tenant/requisitions/{uuid}/audit       — audit trail
type RequisitionOps struct{}

// NewRequisitionOps constructs the handler group.
func NewRequisitionOps() *RequisitionOps { return &RequisitionOps{} }

// authReqn resolves JWT + tenant pool + the requisition:<action> RBAC grant
// for requests with no specific record yet (list/search/create).
func (h *RequisitionOps) authReqn(w http.ResponseWriter, r *http.Request, action authz.Action) (*pgxpool.Pool, string, authz.Scope, bool) {
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
	decision, err := authz.Check(r.Context(), pool, payload.ID, authz.ResourceRequisition, action)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, "", "", false
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", payload.ID, "resource", string(authz.ResourceRequisition), "action", string(action))
		fail(w, http.StatusForbidden, "You do not have permission to "+string(action)+" requisitions.")
		return nil, "", "", false
	}
	return pool, payload.ID, decision.Scope, true
}

// authReqnByUUID resolves auth for a single-record action, then enforces the
// row-level IDOR guard. Denial returns 404 (not 403) so callers cannot
// enumerate ids outside their scope.
func (h *RequisitionOps) authReqnByUUID(w http.ResponseWriter, r *http.Request, uuid string, action authz.Action) (*pgxpool.Pool, string, *requisition.Requisition, bool) {
	pool, identityID, scope, ok := h.authReqn(w, r, action)
	if !ok {
		return nil, "", nil, false
	}
	reqn, err := requisition.Get(r.Context(), pool, uuid)
	if errors.Is(err, requisition.ErrNotFound) {
		fail(w, http.StatusNotFound, "Requisition not found.")
		return nil, "", nil, false
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load requisition.")
		return nil, "", nil, false
	}
	if scope != authz.ScopeAll {
		allowed, aerr := recordInScope(r.Context(), pool, scope, identityID, reqn.OwnerUserID)
		if aerr != nil {
			fail(w, http.StatusInternalServerError, "Permission check failed.")
			return nil, "", nil, false
		}
		if !allowed {
			logSecurityEvent(r, "idor_denied",
				"identity", identityID, "record", uuid, "resource", string(authz.ResourceRequisition),
				"action", string(action), "scope", string(scope))
			fail(w, http.StatusNotFound, "Requisition not found.")
			return nil, "", nil, false
		}
	}
	return pool, identityID, reqn, true
}

// reqnFail maps a store error to an HTTP response.
func reqnFail(w http.ResponseWriter, err error, serverMsg string) {
	switch {
	case errors.Is(err, requisition.ErrNotFound):
		fail(w, http.StatusNotFound, "Requisition not found.")
	case errors.Is(err, requisition.ErrInvalidTransition),
		errors.Is(err, requisition.ErrApprovalRequired),
		errors.Is(err, requisition.ErrApprovalNotRequired):
		fail(w, http.StatusConflict, err.Error())
	case errors.Is(err, requisition.ErrNotApprover):
		fail(w, http.StatusForbidden, err.Error())
	case requisition.IsClientError(err):
		fail(w, http.StatusBadRequest, err.Error())
	default:
		var ife *query.InvalidFilterError
		if errors.As(err, &ife) {
			fail(w, http.StatusBadRequest, ife.Error())
			return
		}
		fail(w, http.StatusInternalServerError, serverMsg)
	}
}

// ---- list / search / create --------------------------------------------------

// List GET /api/tenant/requisitions
func (h *RequisitionOps) List(w http.ResponseWriter, r *http.Request) {
	pool, identityID, scope, ok := h.authReqn(w, r, authz.ActionRead)
	if !ok {
		return
	}
	req := query.Request{Cursor: r.URL.Query().Get("cursor"), Search: r.URL.Query().Get("search")}
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil {
		req.Limit = n
	}
	h.search(w, r, pool, identityID, scope, req)
}

// Search POST /api/tenant/requisitions/search
func (h *RequisitionOps) Search(w http.ResponseWriter, r *http.Request) {
	pool, identityID, scope, ok := h.authReqn(w, r, authz.ActionRead)
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
	h.search(w, r, pool, identityID, scope, req)
}

func (h *RequisitionOps) search(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, identityID string, scope authz.Scope, req query.Request) {
	page, err := requisition.Search(r.Context(), pool, string(scope), identityID, req)
	if err != nil {
		reqnFail(w, err, "Failed to search requisitions.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":    true,
		"scope":      scope,
		"records":    page.Records,
		"nextCursor": page.NextCursor,
		"hasMore":    page.HasMore,
	})
}

// Create POST /api/tenant/requisitions
func (h *RequisitionOps) Create(w http.ResponseWriter, r *http.Request) {
	pool, identityID, _, ok := h.authReqn(w, r, authz.ActionCreate)
	if !ok {
		return
	}
	var in requisition.CreateRequisitionInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	reqn, err := requisition.Create(r.Context(), pool, in, resolveEmployeeID(r, identityID))
	if err != nil {
		reqnFail(w, err, "Failed to create requisition.")
		return
	}
	auditReqn(r, pool, identityID, "create", reqn.ID, nil, reqn)
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "requisition": reqn})
}

// ---- single record ------------------------------------------------------------

// Get GET /api/tenant/requisitions/{uuid}
func (h *RequisitionOps) Get(w http.ResponseWriter, r *http.Request) {
	_, _, reqn, ok := h.authReqnByUUID(w, r, r.PathValue("uuid"), authz.ActionRead)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "requisition": reqn})
}

// Update PATCH /api/tenant/requisitions/{uuid}
func (h *RequisitionOps) Update(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, before, ok := h.authReqnByUUID(w, r, uuid, authz.ActionUpdate)
	if !ok {
		return
	}
	var in requisition.UpdateRequisitionInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	after, err := requisition.Update(r.Context(), pool, uuid, in, resolveEmployeeID(r, identityID))
	if err != nil {
		reqnFail(w, err, "Failed to update requisition.")
		return
	}
	auditReqn(r, pool, identityID, "update", uuid, before, after)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "requisition": after})
}

// Delete DELETE /api/tenant/requisitions/{uuid}
func (h *RequisitionOps) Delete(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, before, ok := h.authReqnByUUID(w, r, uuid, authz.ActionDelete)
	if !ok {
		return
	}
	if err := requisition.SoftDelete(r.Context(), pool, uuid, resolveEmployeeID(r, identityID)); err != nil {
		reqnFail(w, err, "Failed to delete requisition.")
		return
	}
	auditReqnDelete(r, pool, identityID, uuid, before)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Requisition deleted."})
}

// Transition POST /api/tenant/requisitions/{uuid}/transition  body {"toStatusCode":"..."}
func (h *RequisitionOps) Transition(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, _, ok := h.authReqnByUUID(w, r, uuid, authz.ActionTransition)
	if !ok {
		return
	}
	var req struct {
		ToStatusCode string `json:"toStatusCode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ToStatusCode == "" {
		fail(w, http.StatusBadRequest, "toStatusCode is required.")
		return
	}
	reqn, err := requisition.Transition(r.Context(), pool, uuid, req.ToStatusCode, resolveEmployeeID(r, identityID))
	if err != nil {
		reqnFail(w, err, "Failed to apply transition.")
		return
	}
	auditReqn(r, pool, identityID, "transition", uuid, nil, reqn)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "requisition": reqn})
}

// Approve POST /api/tenant/requisitions/{uuid}/approve
func (h *RequisitionOps) Approve(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, _, ok := h.authReqnByUUID(w, r, uuid, authz.ActionTransition)
	if !ok {
		return
	}
	reqn, err := requisition.Approve(r.Context(), pool, uuid, resolveEmployeeID(r, identityID))
	if err != nil {
		if errors.Is(err, requisition.ErrNotApprover) {
			logSecurityEvent(r, "approval_denied", "identity", identityID, "record", uuid)
		}
		reqnFail(w, err, "Failed to approve requisition.")
		return
	}
	auditReqn(r, pool, identityID, "approve", uuid, nil, reqn)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "requisition": reqn})
}
