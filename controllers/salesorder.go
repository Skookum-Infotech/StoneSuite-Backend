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
	"stonesuite-backend/salesorder"
	"stonesuite-backend/tenancy"
)

// SalesOrderOps handles the Sales Order endpoints: a dedicated relational
// module (header + line items), a sibling of the CRM customer table — not
// served through the generic /api/tenant/crm/{workflowKey} JSONB router
// (spec AD-1). Mirrors CRMOps' auth/IDOR/error-mapping conventions.
//
// Routes:
//
//	GET    /api/tenant/sales-orders                    — unfiltered list (cursor-paginated)
//	POST   /api/tenant/sales-orders/search              — filter + sort + search + pagination
//	POST   /api/tenant/sales-orders                     — create
//	GET    /api/tenant/sales-orders/{uuid}              — get (+ items)
//	PATCH  /api/tenant/sales-orders/{uuid}               — update
//	DELETE /api/tenant/sales-orders/{uuid}               — soft delete
//	POST   /api/tenant/sales-orders/{uuid}/transition    — status change
//	POST   /api/tenant/sales-orders/{uuid}/convert       — convert to an Invoice
//	GET    /api/tenant/sales-orders/{uuid}/inventory     — inventory tab
//	GET    /api/tenant/sales-orders/{uuid}/audit         — audit trail
type SalesOrderOps struct{}

// NewSalesOrderOps constructs the handler group.
func NewSalesOrderOps() *SalesOrderOps { return &SalesOrderOps{} }

// authSO resolves JWT + tenant pool + the sales_order:<action> RBAC grant for
// requests with no specific record yet (list/search/create).
func (h *SalesOrderOps) authSO(w http.ResponseWriter, r *http.Request, action authz.Action) (*pgxpool.Pool, string, authz.Scope, bool) {
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
	decision, err := authz.Check(r.Context(), pool, payload.ID, authz.ResourceSalesOrder, action)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, "", "", false
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", payload.ID, "resource", string(authz.ResourceSalesOrder), "action", string(action))
		fail(w, http.StatusForbidden, "You do not have permission to "+string(action)+" sales orders.")
		return nil, "", "", false
	}
	return pool, payload.ID, decision.Scope, true
}

// authSOByUUID resolves auth for a single-record action, then enforces the
// row-level IDOR guard: an own/team-scoped caller may only act on orders they
// own. Denial returns 404 (not 403) so callers cannot enumerate ids outside
// their scope — mirrors authCRMByRecordID. Sales Order has no team column
// (like the v2 customer table), so team scope behaves like own.
func (h *SalesOrderOps) authSOByUUID(w http.ResponseWriter, r *http.Request, uuid string, action authz.Action) (*pgxpool.Pool, string, *salesorder.Order, bool) {
	pool, identityID, scope, ok := h.authSO(w, r, action)
	if !ok {
		return nil, "", nil, false
	}
	order, err := salesorder.Get(r.Context(), pool, uuid)
	if errors.Is(err, salesorder.ErrNotFound) {
		fail(w, http.StatusNotFound, "Sales order not found.")
		return nil, "", nil, false
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load sales order.")
		return nil, "", nil, false
	}
	if scope != authz.ScopeAll {
		allowed, aerr := recordInScope(r.Context(), pool, scope, identityID, order.OwnerUserID)
		if aerr != nil {
			fail(w, http.StatusInternalServerError, "Permission check failed.")
			return nil, "", nil, false
		}
		if !allowed {
			logSecurityEvent(r, "idor_denied",
				"identity", identityID, "record", uuid, "resource", "sales_order",
				"action", string(action), "scope", string(scope))
			fail(w, http.StatusNotFound, "Sales order not found.")
			return nil, "", nil, false
		}
	}
	return pool, identityID, order, true
}

// soFail maps a store error to an HTTP response.
func soFail(w http.ResponseWriter, err error, serverMsg string) {
	switch {
	case errors.Is(err, salesorder.ErrNotFound):
		fail(w, http.StatusNotFound, "Sales order not found.")
	case errors.Is(err, salesorder.ErrInvalidTransition),
		errors.Is(err, salesorder.ErrApprovalRequired),
		errors.Is(err, salesorder.ErrApprovalNotRequired):
		fail(w, http.StatusConflict, err.Error())
	case errors.Is(err, salesorder.ErrNotApprover):
		fail(w, http.StatusForbidden, err.Error())
	case salesorder.IsClientError(err):
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

// List GET /api/tenant/sales-orders — the unfiltered default list, built from
// query params (?limit=&cursor=&search=) rather than a JSON body.
func (h *SalesOrderOps) List(w http.ResponseWriter, r *http.Request) {
	pool, identityID, scope, ok := h.authSO(w, r, authz.ActionRead)
	if !ok {
		return
	}
	req := query.Request{Cursor: r.URL.Query().Get("cursor"), Search: r.URL.Query().Get("search")}
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil {
		req.Limit = n
	}
	h.search(w, r, pool, identityID, scope, req)
}

// Search POST /api/tenant/sales-orders/search — full filter + sort + global
// search + keyset pagination, composed onto the caller's RBAC scope.
func (h *SalesOrderOps) Search(w http.ResponseWriter, r *http.Request) {
	pool, identityID, scope, ok := h.authSO(w, r, authz.ActionRead)
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

func (h *SalesOrderOps) search(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, identityID string, scope authz.Scope, req query.Request) {
	page, err := salesorder.Search(r.Context(), pool, string(scope), identityID, req)
	if err != nil {
		soFail(w, err, "Failed to search sales orders.")
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

// Create POST /api/tenant/sales-orders
func (h *SalesOrderOps) Create(w http.ResponseWriter, r *http.Request) {
	pool, identityID, _, ok := h.authSO(w, r, authz.ActionCreate)
	if !ok {
		return
	}
	var in salesorder.CreateOrderInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	order, err := salesorder.Create(r.Context(), pool, in, resolveEmployeeID(r, identityID))
	if err != nil {
		soFail(w, err, "Failed to create sales order.")
		return
	}
	auditSO(r, pool, identityID, "create", order.ID, nil, order)
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "salesOrder": order})
}

// ---- single record ------------------------------------------------------------

// Get GET /api/tenant/sales-orders/{uuid}
func (h *SalesOrderOps) Get(w http.ResponseWriter, r *http.Request) {
	_, _, order, ok := h.authSOByUUID(w, r, r.PathValue("uuid"), authz.ActionRead)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "salesOrder": order})
}

// Update PATCH /api/tenant/sales-orders/{uuid}
func (h *SalesOrderOps) Update(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, before, ok := h.authSOByUUID(w, r, uuid, authz.ActionUpdate)
	if !ok {
		return
	}
	var in salesorder.UpdateOrderInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	after, err := salesorder.Update(r.Context(), pool, uuid, in, resolveEmployeeID(r, identityID))
	if err != nil {
		soFail(w, err, "Failed to update sales order.")
		return
	}
	auditSO(r, pool, identityID, "update", uuid, before, after)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "salesOrder": after})
}

// Delete DELETE /api/tenant/sales-orders/{uuid}
func (h *SalesOrderOps) Delete(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, before, ok := h.authSOByUUID(w, r, uuid, authz.ActionDelete)
	if !ok {
		return
	}
	if err := salesorder.SoftDelete(r.Context(), pool, uuid, resolveEmployeeID(r, identityID)); err != nil {
		soFail(w, err, "Failed to delete sales order.")
		return
	}
	auditSODelete(r, pool, identityID, uuid, before)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Sales order deleted."})
}

// Transition POST /api/tenant/sales-orders/{uuid}/transition  body {"toStatusCode":"..."}
func (h *SalesOrderOps) Transition(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, _, ok := h.authSOByUUID(w, r, uuid, authz.ActionTransition)
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
	order, err := salesorder.Transition(r.Context(), pool, uuid, req.ToStatusCode, resolveEmployeeID(r, identityID))
	if err != nil {
		soFail(w, err, "Failed to apply transition.")
		return
	}
	auditSO(r, pool, identityID, "transition", uuid, nil, order)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "salesOrder": order})
}

// Approve POST /api/tenant/sales-orders/{uuid}/approve
// Records the calling employee's approval sign-off on the order at its current
// status (AD-10). RBAC gate is the transition action; being an eligible
// approver is governed additionally by the sales_order_approver configuration
// (a non-approver gets 403; a status with no approvers gets 409).
func (h *SalesOrderOps) Approve(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, _, ok := h.authSOByUUID(w, r, uuid, authz.ActionTransition)
	if !ok {
		return
	}
	order, err := salesorder.Approve(r.Context(), pool, uuid, resolveEmployeeID(r, identityID))
	if err != nil {
		if errors.Is(err, salesorder.ErrNotApprover) {
			logSecurityEvent(r, "approval_denied", "identity", identityID, "record", uuid)
		}
		soFail(w, err, "Failed to approve sales order.")
		return
	}
	auditSO(r, pool, identityID, "approve", uuid, nil, order)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "salesOrder": order})
}

// Inventory GET /api/tenant/sales-orders/{uuid}/inventory
func (h *SalesOrderOps) Inventory(w http.ResponseWriter, r *http.Request) {
	pool, identityID, _, ok := h.authSOByUUID(w, r, r.PathValue("uuid"), authz.ActionRead)
	if !ok {
		return
	}
	// The inventory tab reads inventory_item-resource tables (stock and
	// allocation aggregated tenant-wide), so require inventory_item:read in
	// addition to sales_order:read — a caller with only sales_order:read must
	// not see warehouse stock they'd be denied at GET /inventory/items.
	invDecision, err := authz.Check(r.Context(), pool, identityID, authz.ResourceInventoryItem, authz.ActionRead)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return
	}
	if !invDecision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", identityID, "resource", string(authz.ResourceInventoryItem),
			"action", string(authz.ActionRead), "sales_order", r.PathValue("uuid"))
		fail(w, http.StatusForbidden, "You do not have permission to read inventory.")
		return
	}
	items, err := salesorder.InventoryForOrder(r.Context(), pool, r.PathValue("uuid"))
	if err != nil {
		soFail(w, err, "Failed to load inventory tab.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "items": items})
}
