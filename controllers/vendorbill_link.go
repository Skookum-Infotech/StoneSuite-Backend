// controllers/vendorbill_link.go
package controllers

import (
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/purchaseorder"
	"stonesuite-backend/vendorbill"
)

// authLinkedPurchaseOrder guards the optional purchaseOrderUuid on a new vendor
// bill. The linked order's number is echoed back on the bill, so linking must
// clear the same bar ConvertToBill applies to its source: purchase_order:read,
// with the row-level scope check. An out-of-scope order gets the very same 400
// as a missing one, so the link field cannot be used to probe which orders
// exist. A blank uuid means no link and always passes. Returns false after
// writing the response.
func authLinkedPurchaseOrder(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, identityID, poUUID string) bool {
	poUUID = strings.TrimSpace(poUUID)
	if poUUID == "" {
		return true
	}
	decision, err := authz.Check(r.Context(), pool, identityID, authz.ResourcePurchaseOrder, authz.ActionRead)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return false
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", identityID, "resource", string(authz.ResourcePurchaseOrder), "action", string(authz.ActionRead),
			"context", "vendor_bill_link_purchase_order", "source_record", poUUID)
		fail(w, http.StatusForbidden, "You do not have permission to link purchase orders.")
		return false
	}
	unknown := vendorbill.UnknownPurchaseOrderError().Error()
	if !vendorbill.WellFormedUUID(poUUID) {
		fail(w, http.StatusBadRequest, unknown)
		return false
	}
	po, err := purchaseorder.Get(r.Context(), pool, poUUID)
	if errors.Is(err, purchaseorder.ErrNotFound) {
		fail(w, http.StatusBadRequest, unknown)
		return false
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load purchase order.")
		return false
	}
	if decision.Scope != authz.ScopeAll {
		allowed, aerr := recordInScope(r.Context(), pool, decision.Scope, identityID, po.OwnerUserID)
		if aerr != nil {
			fail(w, http.StatusInternalServerError, "Permission check failed.")
			return false
		}
		if !allowed {
			logSecurityEvent(r, "idor_denied",
				"identity", identityID, "record", poUUID, "resource", string(authz.ResourcePurchaseOrder),
				"action", string(authz.ActionRead), "scope", string(decision.Scope),
				"context", "vendor_bill_link_purchase_order")
			fail(w, http.StatusBadRequest, unknown)
			return false
		}
	}
	return true
}
