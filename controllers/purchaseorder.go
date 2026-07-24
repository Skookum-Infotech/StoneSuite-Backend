// controllers/purchaseorder.go
package controllers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/middleware"
	"stonesuite-backend/purchaseorder"
	"stonesuite-backend/query"
	"stonesuite-backend/tenancy"
)

// PurchaseOrderOps handles the Purchase Order endpoints: a dedicated
// relational module (header + line items + receiving progress), the first
// Purchases document module — a sibling of Estimate/Quote/Invoice, not
// served through the generic /api/tenant/crm/{workflowKey} JSONB router
// (spec AD-1). Mirrors PaymentOps' auth/IDOR/error-mapping conventions.
//
// Routes:
//
//	GET    /api/tenant/purchase-orders                    — unfiltered list (cursor-paginated)
//	POST   /api/tenant/purchase-orders/search             — filter + sort + search + pagination
//	POST   /api/tenant/purchase-orders                    — create
//	GET    /api/tenant/purchase-orders/{uuid}             — get (+ items)
//	PATCH  /api/tenant/purchase-orders/{uuid}             — update (DRFT only)
//	DELETE /api/tenant/purchase-orders/{uuid}             — soft delete (DRFT/CANC only)
//	POST   /api/tenant/purchase-orders/{uuid}/transition  — status change
//	POST   /api/tenant/purchase-orders/{uuid}/approve     — approval sign-off
//	GET    /api/tenant/purchase-orders/{uuid}/audit       — audit trail
type PurchaseOrderOps struct{}

// NewPurchaseOrderOps constructs the handler group.
func NewPurchaseOrderOps() *PurchaseOrderOps { return &PurchaseOrderOps{} }

// authPO resolves JWT + tenant pool + the purchase_order:<action> RBAC grant
// for requests with no specific record yet (list/search/create).
func (h *PurchaseOrderOps) authPO(w http.ResponseWriter, r *http.Request, action authz.Action) (*pgxpool.Pool, string, authz.Scope, bool) {
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
	decision, err := authz.Check(r.Context(), pool, payload.ID, authz.ResourcePurchaseOrder, action)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, "", "", false
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", payload.ID, "resource", string(authz.ResourcePurchaseOrder), "action", string(action))
		fail(w, http.StatusForbidden, "You do not have permission to "+string(action)+" purchase orders.")
		return nil, "", "", false
	}
	return pool, payload.ID, decision.Scope, true
}

// authPOByUUID resolves auth for a single-record action, then enforces the
// row-level IDOR guard. Denial returns 404 (not 403) so callers cannot
// enumerate ids outside their scope.
func (h *PurchaseOrderOps) authPOByUUID(w http.ResponseWriter, r *http.Request, uuid string, action authz.Action) (*pgxpool.Pool, string, *purchaseorder.PurchaseOrder, bool) {
	pool, identityID, scope, ok := h.authPO(w, r, action)
	if !ok {
		return nil, "", nil, false
	}
	po, err := purchaseorder.Get(r.Context(), pool, uuid)
	if errors.Is(err, purchaseorder.ErrNotFound) {
		fail(w, http.StatusNotFound, "Purchase order not found.")
		return nil, "", nil, false
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load purchase order.")
		return nil, "", nil, false
	}
	if scope != authz.ScopeAll {
		allowed, aerr := recordInScope(r.Context(), pool, scope, identityID, po.OwnerUserID)
		if aerr != nil {
			fail(w, http.StatusInternalServerError, "Permission check failed.")
			return nil, "", nil, false
		}
		if !allowed {
			logSecurityEvent(r, "idor_denied",
				"identity", identityID, "record", uuid, "resource", string(authz.ResourcePurchaseOrder),
				"action", string(action), "scope", string(scope))
			fail(w, http.StatusNotFound, "Purchase order not found.")
			return nil, "", nil, false
		}
	}
	return pool, identityID, po, true
}

// poFail maps a store error to an HTTP response.
func poFail(w http.ResponseWriter, err error, serverMsg string) {
	switch {
	case errors.Is(err, purchaseorder.ErrNotFound):
		fail(w, http.StatusNotFound, "Purchase order not found.")
	case errors.Is(err, purchaseorder.ErrInvalidTransition),
		errors.Is(err, purchaseorder.ErrApprovalRequired),
		errors.Is(err, purchaseorder.ErrApprovalNotRequired):
		fail(w, http.StatusConflict, err.Error())
	case errors.Is(err, purchaseorder.ErrNotApprover):
		fail(w, http.StatusForbidden, err.Error())
	case purchaseorder.IsClientError(err):
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

// List GET /api/tenant/purchase-orders
func (h *PurchaseOrderOps) List(w http.ResponseWriter, r *http.Request) {
	pool, identityID, scope, ok := h.authPO(w, r, authz.ActionRead)
	if !ok {
		return
	}
	req := query.Request{Cursor: r.URL.Query().Get("cursor"), Search: r.URL.Query().Get("search")}
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil {
		req.Limit = n
	}
	h.search(w, r, pool, identityID, scope, req)
}

// Search POST /api/tenant/purchase-orders/search
func (h *PurchaseOrderOps) Search(w http.ResponseWriter, r *http.Request) {
	pool, identityID, scope, ok := h.authPO(w, r, authz.ActionRead)
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

func (h *PurchaseOrderOps) search(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, identityID string, scope authz.Scope, req query.Request) {
	page, err := purchaseorder.Search(r.Context(), pool, string(scope), identityID, req)
	if err != nil {
		poFail(w, err, "Failed to search purchase orders.")
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

// Create POST /api/tenant/purchase-orders
func (h *PurchaseOrderOps) Create(w http.ResponseWriter, r *http.Request) {
	pool, identityID, _, ok := h.authPO(w, r, authz.ActionCreate)
	if !ok {
		return
	}
	var in purchaseorder.CreatePurchaseOrderInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	po, err := purchaseorder.Create(r.Context(), pool, in, resolveEmployeeID(r, identityID))
	if err != nil {
		poFail(w, err, "Failed to create purchase order.")
		return
	}
	auditPO(r, pool, identityID, "create", po.ID, nil, po)
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "purchaseOrder": po})
}

// ---- single record ------------------------------------------------------------

// Get GET /api/tenant/purchase-orders/{uuid}
func (h *PurchaseOrderOps) Get(w http.ResponseWriter, r *http.Request) {
	_, _, po, ok := h.authPOByUUID(w, r, r.PathValue("uuid"), authz.ActionRead)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "purchaseOrder": po})
}

// Update PATCH /api/tenant/purchase-orders/{uuid}
func (h *PurchaseOrderOps) Update(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, before, ok := h.authPOByUUID(w, r, uuid, authz.ActionUpdate)
	if !ok {
		return
	}
	var in purchaseorder.UpdatePurchaseOrderInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	after, err := purchaseorder.Update(r.Context(), pool, uuid, in, resolveEmployeeID(r, identityID))
	if err != nil {
		poFail(w, err, "Failed to update purchase order.")
		return
	}
	auditPO(r, pool, identityID, "update", uuid, before, after)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "purchaseOrder": after})
}

// Delete DELETE /api/tenant/purchase-orders/{uuid}
func (h *PurchaseOrderOps) Delete(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, before, ok := h.authPOByUUID(w, r, uuid, authz.ActionDelete)
	if !ok {
		return
	}
	if err := purchaseorder.SoftDelete(r.Context(), pool, uuid, resolveEmployeeID(r, identityID)); err != nil {
		poFail(w, err, "Failed to delete purchase order.")
		return
	}
	auditPODelete(r, pool, identityID, uuid, before)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Purchase order deleted."})
}

// Transition POST /api/tenant/purchase-orders/{uuid}/transition  body {"toStatusCode":"..."}
func (h *PurchaseOrderOps) Transition(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, _, ok := h.authPOByUUID(w, r, uuid, authz.ActionTransition)
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
	po, err := purchaseorder.Transition(r.Context(), pool, uuid, req.ToStatusCode, resolveEmployeeID(r, identityID))
	if err != nil {
		poFail(w, err, "Failed to apply transition.")
		return
	}
	auditPO(r, pool, identityID, "transition", uuid, nil, po)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "purchaseOrder": po})
}

// Approve POST /api/tenant/purchase-orders/{uuid}/approve
func (h *PurchaseOrderOps) Approve(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, _, ok := h.authPOByUUID(w, r, uuid, authz.ActionTransition)
	if !ok {
		return
	}
	po, err := purchaseorder.Approve(r.Context(), pool, uuid, resolveEmployeeID(r, identityID))
	if err != nil {
		if errors.Is(err, purchaseorder.ErrNotApprover) {
			logSecurityEvent(r, "approval_denied", "identity", identityID, "record", uuid)
		}
		poFail(w, err, "Failed to approve purchase order.")
		return
	}
	auditPO(r, pool, identityID, "approve", uuid, nil, po)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "purchaseOrder": po})
}
