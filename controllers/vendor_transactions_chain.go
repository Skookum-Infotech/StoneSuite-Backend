// controllers/vendor_transactions_chain.go
package controllers

import (
	"errors"
	"net/http"

	"stonesuite-backend/authz"
	"stonesuite-backend/vendortransactions"
)

// Chain GET /api/tenant/vendors/{uuid}/transactions/chain?po=<purchase order uuid>
//
// The purchase order's receipt, bill and payment trail with a derived status.
// Each type lists only what the caller may read plus a hidden count. A purchase
// order that is missing, deleted, another vendor's or out of scope is 404
// (idor_denied); a caller without purchase_order read gets the same 404 but is
// logged as permission_denied.
func (h *VendorTransactionsOps) Chain(w http.ResponseWriter, r *http.Request) {
	vendorUUID := r.PathValue("uuid")
	pool, identityID, _, ok := h.vendor.authVendorByUUID(w, r, vendorUUID, authz.ActionRead)
	if !ok {
		return
	}
	poUUID := r.URL.Query().Get("po")
	if poUUID == "" {
		fail(w, http.StatusBadRequest, "Invalid po: purchase order id is required.")
		return
	}
	chain, err := vendortransactions.GetChain(r.Context(), pool, identityID, vendorUUID, poUUID)
	if errors.Is(err, vendortransactions.ErrPurchaseOrderForbidden) {
		logSecurityEvent(r, "permission_denied",
			"identity", identityID, "record", vendorUUID, "resource", "purchase_order",
			"action", string(authz.ActionRead))
		fail(w, http.StatusNotFound, "Purchase order not found.")
		return
	}
	if errors.Is(err, vendortransactions.ErrPurchaseOrderNotFound) {
		logSecurityEvent(r, "idor_denied", "identity", identityID, "resource", vendorTxResource, "id", poUUID)
		fail(w, http.StatusNotFound, "Purchase order not found.")
		return
	}
	if writeVendorTxError(w, err) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":     true,
		"status":      chain.Status,
		"po":          chain.PO,
		"receipts":    chain.Receipts,
		"bills":       chain.Bills,
		"payments":    chain.Payments,
		"billsHidden": chain.BillsHidden,
	})
}
