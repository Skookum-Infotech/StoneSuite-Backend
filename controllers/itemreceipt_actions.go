// controllers/itemreceipt_actions.go — the handlers that do more than CRUD:
// posting a receipt (which moves stock and rolls the order forward), reversing
// it, and the order-scoped listing. Split from itemreceipt.go for the 300-line
// file cap.
package controllers

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/itemreceipt"
)

// Post POST /api/tenant/item-receipts/{uuid}/post
//
// The over-receipt override is resolved here rather than in the store: the
// store decides whether an override is *needed*, the enforcer decides whether
// this caller *has* it. A denial is not fatal — a within-tolerance posting
// proceeds without the grant — so the check is advisory and its error is
// treated as "not granted".
func (h *ItemReceiptOps) Post(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, _, ok := h.authIRByUUID(w, r, uuid, authz.ActionTransition)
	if !ok {
		return
	}
	var in itemreceipt.PostInput
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			fail(w, http.StatusBadRequest, "Invalid request body.")
			return
		}
	}

	canApprove := false
	if d, err := authz.Check(r.Context(), pool, identityID, authz.ResourceItemReceipt, authz.ActionApprove); err == nil {
		canApprove = d.Allowed
	}

	ir, err := itemreceipt.Post(r.Context(), pool, uuid, in, canApprove, resolveEmployeeID(r, identityID))
	if err != nil {
		if errors.Is(err, itemreceipt.ErrOverReceipt) {
			logSecurityEvent(r, "over_receipt_denied",
				"identity", identityID, "record", uuid, "resource", string(authz.ResourceItemReceipt))
		}
		irFail(w, err, "Failed to post item receipt.")
		return
	}
	auditIR(r, pool, identityID, "post", uuid, nil, ir)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "itemReceipt": ir})
}

// createAndPost is Create's "post": true path: it creates the receipt and posts
// it in one transaction, so a refused post (over-receipt, order no longer
// receivable) leaves nothing behind -- no Pending receipt to clean up.
//
// Posting moves stock, so on top of the item_receipt:create Create already
// required, the caller needs item_receipt:transition -- the grant /post
// demands. It is checked before the store is touched, so a caller who may only
// create is refused without a receipt being written. The over-receipt override
// is resolved exactly as in Post.
func (h *ItemReceiptOps) createAndPost(
	w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, identityID string, in itemreceipt.CreateItemReceiptInput,
) {
	d, err := authz.Check(r.Context(), pool, identityID, authz.ResourceItemReceipt, authz.ActionTransition)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return
	}
	if !d.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", identityID, "resource", string(authz.ResourceItemReceipt),
			"action", string(authz.ActionTransition), "context", "item_receipt_create_and_post")
		fail(w, http.StatusForbidden, "You do not have permission to post item receipts.")
		return
	}

	canApprove := false
	if a, err := authz.Check(r.Context(), pool, identityID, authz.ResourceItemReceipt, authz.ActionApprove); err == nil {
		canApprove = a.Allowed
	}

	ir, err := itemreceipt.CreateAndPost(r.Context(), pool, in, canApprove, resolveEmployeeID(r, identityID))
	if err != nil {
		if errors.Is(err, itemreceipt.ErrOverReceipt) {
			logSecurityEvent(r, "over_receipt_denied",
				"identity", identityID, "record", in.PurchaseOrderUUID, "resource", string(authz.ResourceItemReceipt))
		}
		irFail(w, err, "Failed to create and post item receipt.")
		return
	}
	auditIR(r, pool, identityID, "create", ir.ID, nil, ir)
	auditIR(r, pool, identityID, "post", ir.ID, nil, ir)
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "itemReceipt": ir})
}

// Void POST /api/tenant/item-receipts/{uuid}/void  body {"voidReason":"..."}
func (h *ItemReceiptOps) Void(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, _, ok := h.authIRByUUID(w, r, uuid, authz.ActionTransition)
	if !ok {
		return
	}
	var in itemreceipt.VoidInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	ir, err := itemreceipt.Void(r.Context(), pool, uuid, in, resolveEmployeeID(r, identityID))
	if err != nil {
		irFail(w, err, "Failed to void item receipt.")
		return
	}
	auditIR(r, pool, identityID, "void", uuid, nil, ir)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "itemReceipt": ir})
}

// Transition POST /api/tenant/item-receipts/{uuid}/transition  body {"toStatusCode":"..."}
func (h *ItemReceiptOps) Transition(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, _, ok := h.authIRByUUID(w, r, uuid, authz.ActionTransition)
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
	ir, err := itemreceipt.Transition(r.Context(), pool, uuid, req.ToStatusCode, resolveEmployeeID(r, identityID))
	if err != nil {
		irFail(w, err, "Failed to apply transition.")
		return
	}
	auditIR(r, pool, identityID, "transition", uuid, nil, ir)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "itemReceipt": ir})
}

// ForPurchaseOrder GET /api/tenant/purchase-orders/{uuid}/receipts
//
// This route hangs off the purchase order, so it is gated by the purchase
// order's own permission and IDOR guard (PurchaseOrderOps.authPOByUUID) — a
// caller who can see the order can see what arrived against it.
func (h *ItemReceiptOps) ForPurchaseOrder(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	poOps := NewPurchaseOrderOps()
	pool, _, _, ok := poOps.authPOByUUID(w, r, uuid, authz.ActionRead)
	if !ok {
		return
	}
	records, err := itemreceipt.ForPurchaseOrder(r.Context(), pool, uuid)
	if err != nil {
		irFail(w, err, "Failed to load item receipts for this purchase order.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "records": records})
}
