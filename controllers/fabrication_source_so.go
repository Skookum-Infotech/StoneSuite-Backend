package controllers

import (
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/salesorder"
)

// authSourceSalesOrder gates creating a fabrication job from a sales order. The
// installation:create grant checked by authFJ says nothing about the sales
// order, so without this an installation-only caller could point a job at any
// order in the tenant by uuid. It requires sales_order:read and the same
// row-level scope guard authSOByUUID applies; an out-of-scope or unknown order
// is a 404 so ids can't be enumerated. A blank uuid is left for the store to
// reject with its own validation message. Writes the response and returns false
// on denial.
func authSourceSalesOrder(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, identityID, soUUID string) bool {
	if soUUID == "" {
		return true
	}
	decision, err := authz.Check(r.Context(), pool, identityID, authz.ResourceSalesOrder, authz.ActionRead)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return false
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", identityID, "resource", string(authz.ResourceSalesOrder), "action", string(authz.ActionRead),
			"context", "fabrication_create", "source_record", soUUID)
		fail(w, http.StatusForbidden, "You do not have permission to read sales orders.")
		return false
	}
	order, err := salesorder.Get(r.Context(), pool, soUUID)
	if errors.Is(err, salesorder.ErrNotFound) {
		fail(w, http.StatusNotFound, "Sales order not found.")
		return false
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load sales order.")
		return false
	}
	if decision.Scope != authz.ScopeAll {
		allowed, aerr := recordInScope(r.Context(), pool, decision.Scope, identityID, order.OwnerUserID)
		if aerr != nil {
			fail(w, http.StatusInternalServerError, "Permission check failed.")
			return false
		}
		if !allowed {
			logSecurityEvent(r, "idor_denied",
				"identity", identityID, "record", soUUID, "resource", "sales_order",
				"action", string(authz.ActionRead), "scope", string(decision.Scope),
				"context", "fabrication_create")
			fail(w, http.StatusNotFound, "Sales order not found.")
			return false
		}
	}
	return true
}
