// controllers/itemreceipt.go
package controllers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/itemreceipt"
	"stonesuite-backend/middleware"
	"stonesuite-backend/query"
	"stonesuite-backend/tenancy"
)

// ItemReceiptOps handles the Item Receipt endpoints: the document recording
// goods physically arriving against a finalized purchase order. It is the only
// writer of purchase_order_item.qty_received and the trigger for the purchase
// order's SENT → PART → RCVD rollup. Mirrors PurchaseOrderOps' auth/IDOR/
// error-mapping conventions.
//
// Routes:
//
//	GET    /api/tenant/item-receipts                    — unfiltered list (cursor-paginated)
//	POST   /api/tenant/item-receipts/search             — filter + sort + search + pagination
//	POST   /api/tenant/item-receipts                    — create (against a purchase order)
//	GET    /api/tenant/item-receipts/{uuid}             — get (+ lines)
//	PATCH  /api/tenant/item-receipts/{uuid}             — update (PEND only)
//	DELETE /api/tenant/item-receipts/{uuid}             — soft delete (PEND/VOID only)
//	POST   /api/tenant/item-receipts/{uuid}/post        — post: qty_received + stock + rollup
//	POST   /api/tenant/item-receipts/{uuid}/void        — void: reverse the posting
//	POST   /api/tenant/item-receipts/{uuid}/transition  — status change
//	GET    /api/tenant/item-receipts/{uuid}/audit       — audit trail
type ItemReceiptOps struct{}

// NewItemReceiptOps constructs the handler group.
func NewItemReceiptOps() *ItemReceiptOps { return &ItemReceiptOps{} }

// authIR resolves JWT + tenant pool + the item_receipt:<action> RBAC grant for
// requests with no specific record yet (list/search/create).
func (h *ItemReceiptOps) authIR(w http.ResponseWriter, r *http.Request, action authz.Action) (*pgxpool.Pool, string, authz.Scope, bool) {
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
	decision, err := authz.Check(r.Context(), pool, payload.ID, authz.ResourceItemReceipt, action)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, "", "", false
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", payload.ID, "resource", string(authz.ResourceItemReceipt), "action", string(action))
		fail(w, http.StatusForbidden, "You do not have permission to "+string(action)+" item receipts.")
		return nil, "", "", false
	}
	return pool, payload.ID, decision.Scope, true
}

// authIRByUUID resolves auth for a single-record action, then enforces the
// row-level IDOR guard. Denial returns 404 (not 403) so callers cannot
// enumerate ids outside their scope.
func (h *ItemReceiptOps) authIRByUUID(w http.ResponseWriter, r *http.Request, uuid string, action authz.Action) (*pgxpool.Pool, string, *itemreceipt.ItemReceipt, bool) {
	pool, identityID, scope, ok := h.authIR(w, r, action)
	if !ok {
		return nil, "", nil, false
	}
	ir, err := itemreceipt.Get(r.Context(), pool, uuid)
	if errors.Is(err, itemreceipt.ErrNotFound) {
		fail(w, http.StatusNotFound, "Item receipt not found.")
		return nil, "", nil, false
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load item receipt.")
		return nil, "", nil, false
	}
	if scope != authz.ScopeAll {
		allowed, aerr := recordInScope(r.Context(), pool, scope, identityID, ir.OwnerUserID)
		if aerr != nil {
			fail(w, http.StatusInternalServerError, "Permission check failed.")
			return nil, "", nil, false
		}
		if !allowed {
			logSecurityEvent(r, "idor_denied",
				"identity", identityID, "record", uuid, "resource", string(authz.ResourceItemReceipt),
				"action", string(action), "scope", string(scope))
			fail(w, http.StatusNotFound, "Item receipt not found.")
			return nil, "", nil, false
		}
	}
	return pool, identityID, ir, true
}

// irFail maps a store error to an HTTP response.
func irFail(w http.ResponseWriter, err error, serverMsg string) {
	switch {
	case errors.Is(err, itemreceipt.ErrNotFound):
		fail(w, http.StatusNotFound, "Item receipt not found.")
	case errors.Is(err, itemreceipt.ErrInvalidTransition),
		errors.Is(err, itemreceipt.ErrAlreadyPosted),
		errors.Is(err, itemreceipt.ErrPONotReceivable),
		errors.Is(err, itemreceipt.ErrMovementAlreadyApplied):
		fail(w, http.StatusConflict, err.Error())
	case errors.Is(err, itemreceipt.ErrOverReceipt):
		// 403, not 400: the request is well-formed and the quantities are real.
		// The caller simply is not permitted to accept an over-delivery.
		fail(w, http.StatusForbidden, err.Error())
	case itemreceipt.IsClientError(err):
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

// List GET /api/tenant/item-receipts
func (h *ItemReceiptOps) List(w http.ResponseWriter, r *http.Request) {
	pool, identityID, scope, ok := h.authIR(w, r, authz.ActionRead)
	if !ok {
		return
	}
	req := query.Request{Cursor: r.URL.Query().Get("cursor"), Search: r.URL.Query().Get("search")}
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil {
		req.Limit = n
	}
	h.search(w, r, pool, identityID, scope, req)
}

// Search POST /api/tenant/item-receipts/search
func (h *ItemReceiptOps) Search(w http.ResponseWriter, r *http.Request) {
	pool, identityID, scope, ok := h.authIR(w, r, authz.ActionRead)
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

func (h *ItemReceiptOps) search(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, identityID string, scope authz.Scope, req query.Request) {
	page, err := itemreceipt.Search(r.Context(), pool, string(scope), identityID, req)
	if err != nil {
		irFail(w, err, "Failed to search item receipts.")
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

// Create POST /api/tenant/item-receipts
func (h *ItemReceiptOps) Create(w http.ResponseWriter, r *http.Request) {
	pool, identityID, _, ok := h.authIR(w, r, authz.ActionCreate)
	if !ok {
		return
	}
	var in itemreceipt.CreateItemReceiptInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	ir, err := itemreceipt.Create(r.Context(), pool, in, resolveEmployeeID(r, identityID))
	if err != nil {
		irFail(w, err, "Failed to create item receipt.")
		return
	}
	auditIR(r, pool, identityID, "create", ir.ID, nil, ir)
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "itemReceipt": ir})
}

// ---- single record ------------------------------------------------------------

// Get GET /api/tenant/item-receipts/{uuid}
func (h *ItemReceiptOps) Get(w http.ResponseWriter, r *http.Request) {
	_, _, ir, ok := h.authIRByUUID(w, r, r.PathValue("uuid"), authz.ActionRead)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "itemReceipt": ir})
}

// Update PATCH /api/tenant/item-receipts/{uuid}
func (h *ItemReceiptOps) Update(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, before, ok := h.authIRByUUID(w, r, uuid, authz.ActionUpdate)
	if !ok {
		return
	}
	var in itemreceipt.UpdateItemReceiptInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	after, err := itemreceipt.Update(r.Context(), pool, uuid, in, resolveEmployeeID(r, identityID))
	if err != nil {
		irFail(w, err, "Failed to update item receipt.")
		return
	}
	auditIR(r, pool, identityID, "update", uuid, before, after)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "itemReceipt": after})
}

// Delete DELETE /api/tenant/item-receipts/{uuid}
func (h *ItemReceiptOps) Delete(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, before, ok := h.authIRByUUID(w, r, uuid, authz.ActionDelete)
	if !ok {
		return
	}
	if err := itemreceipt.SoftDelete(r.Context(), pool, uuid, resolveEmployeeID(r, identityID)); err != nil {
		irFail(w, err, "Failed to delete item receipt.")
		return
	}
	auditIRDelete(r, pool, identityID, uuid, before)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Item receipt deleted."})
}
