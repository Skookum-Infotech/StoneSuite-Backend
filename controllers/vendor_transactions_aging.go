// controllers/vendor_transactions_aging.go
package controllers

import (
	"net/http"

	"stonesuite-backend/authz"
	"stonesuite-backend/vendortransactions"
)

// Aging GET /api/tenant/vendors/{uuid}/transactions/aging
//
// The vendor's open bills bucketed by days past due. buckets is null when the
// caller cannot read vendor bills.
func (h *VendorTransactionsOps) Aging(w http.ResponseWriter, r *http.Request) {
	vendorUUID := r.PathValue("uuid")
	pool, identityID, _, ok := h.vendor.authVendorByUUID(w, r, vendorUUID, authz.ActionRead)
	if !ok {
		return
	}
	ag, err := vendortransactions.GetAging(r.Context(), pool, identityID, vendorUUID, tenantToday(r.Context(), pool, h.fallbackTZ))
	if writeVendorTxError(w, err) {
		return
	}
	if ag.Denied {
		logSecurityEvent(r, "permission_denied",
			"identity", identityID, "record", vendorUUID, "resource", vendorTxResource,
			"action", string(authz.ActionRead), "type", "vendor_bill")
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":      true,
		"scopeLimited": ag.ScopeLimited,
		"buckets":      ag.Buckets,
		"total":        ag.Total,
	})
}
