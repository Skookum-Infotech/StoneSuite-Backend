// controllers/vendor_transactions_scheduled.go
package controllers

import (
	"net/http"
	"strconv"

	"stonesuite-backend/authz"
	"stonesuite-backend/vendortransactions"
)

// Scheduled GET /api/tenant/vendors/{uuid}/transactions/scheduled
//
// The vendor's scheduled payments, soonest first. Query param: limit (default
// 5, max 100). rows is empty when the caller cannot read vendor payments.
func (h *VendorTransactionsOps) Scheduled(w http.ResponseWriter, r *http.Request) {
	vendorUUID := r.PathValue("uuid")
	pool, identityID, _, ok := h.vendor.authVendorByUUID(w, r, vendorUUID, authz.ActionRead)
	if !ok {
		return
	}
	limit := 0
	if v := r.URL.Query().Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			fail(w, http.StatusBadRequest, "Invalid limit: must be an integer.")
			return
		}
		limit = n
	}
	sched, err := vendortransactions.ListScheduled(r.Context(), pool, identityID, vendorUUID, limit)
	if writeVendorTxError(w, err) {
		return
	}
	if sched.Denied {
		logSecurityEvent(r, "permission_denied",
			"identity", identityID, "record", vendorUUID, "resource", vendorTxResource,
			"action", string(authz.ActionRead), "type", "vendor_payment")
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true,
		"rows":    sched.Rows,
	})
}
