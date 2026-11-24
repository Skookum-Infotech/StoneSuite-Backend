// controllers/expense_bill.go — raising a vendor bill for an approved expense claim.
package controllers

import (
	"net/http"

	"stonesuite-backend/authz"
	"stonesuite-backend/expense"
)

// CreateBill POST /api/tenant/expenses/{uuid}/create-bill
//
// Raises the Draft vendor bill for an approved claim that has none (a claim
// approved before bills were raised automatically, or whose bill was voided). It
// moves no status: the claim stays Approved until the bill is paid. The caller
// needs expense:transition, as for any move on a claim, and vendor_bill:create,
// since it makes a vendor bill.
func (h *ExpenseOps) CreateBill(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, before, ok := h.authExpByUUID(w, r, uuid, authz.ActionTransition)
	if !ok {
		return
	}
	decision, err := authz.Check(r.Context(), pool, identityID, authz.ResourceVendorBill, authz.ActionCreate)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", identityID, "resource", string(authz.ResourceVendorBill), "action", string(authz.ActionCreate))
		fail(w, http.StatusForbidden, "You do not have permission to create vendor bills.")
		return
	}
	exp, err := expense.CreateBill(r.Context(), pool, uuid, resolveEmployeeID(r, identityID))
	if err != nil {
		expFail(w, err, "Failed to create the vendor bill.")
		return
	}
	auditExp(r, pool, identityID, "create_bill", uuid, before, exp)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "expense": exp})
}

// Billable GET /api/tenant/expenses/billable?vendorUuid=...&search=...
//
// Lists the approved claims waiting for a vendor bill whose claimant's Employee
// vendor is vendorUuid -- the Vendor Bill form's expense picker. Gated by
// expense:read and narrowed by the caller's expense scope like the claims list.
func (h *ExpenseOps) Billable(w http.ResponseWriter, r *http.Request) {
	pool, identityID, scope, ok := h.authExp(w, r, authz.ActionRead)
	if !ok {
		return
	}
	vendorUUID := r.URL.Query().Get("vendorUuid")
	if vendorUUID == "" {
		fail(w, http.StatusBadRequest, "vendorUuid is required.")
		return
	}
	claims, err := expense.Billable(r.Context(), pool, string(scope), identityID, vendorUUID, r.URL.Query().Get("search"))
	if err != nil {
		expFail(w, err, "Failed to load expense claims.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "claims": claims})
}
