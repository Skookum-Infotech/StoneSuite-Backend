package controllers

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/salesorder"
)

// codeDuplicateDocument is the machine-readable code on a duplicate-PO 409.
const codeDuplicateDocument = "duplicate_document"

// writeSODuplicate writes the 409 for a *salesorder.DuplicateDocumentError and
// reports whether err was one. The existing order's uuid/number are only
// disclosed when the caller could read that order themselves (read grant +
// recordInScope); otherwise the 409 says a duplicate exists without naming it,
// so the guard can't be used to enumerate orders outside the caller's scope.
func writeSODuplicate(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, identityID string, err error) bool {
	var dup *salesorder.DuplicateDocumentError
	if !errors.As(err, &dup) {
		return false
	}
	body := map[string]any{
		"success": false,
		"message": "A sales order with this PO number already exists for this customer.",
		"code":    codeDuplicateDocument,
	}
	if canReadSO(r, pool, identityID, dup.ExistingUUID) {
		body["message"] = fmt.Sprintf("A sales order with this PO number already exists for this customer (%s).", dup.ExistingNumber)
		body["existingUuid"] = dup.ExistingUUID
		body["existingNumber"] = dup.ExistingNumber
	}
	writeJSON(w, http.StatusConflict, body)
	return true
}

// canReadSO reports whether the caller may read the given order: a read grant
// and, below `all` scope, ownership. Any lookup error fails closed.
func canReadSO(r *http.Request, pool *pgxpool.Pool, identityID, uuid string) bool {
	d, err := authz.Check(r.Context(), pool, identityID, authz.ResourceSalesOrder, authz.ActionRead)
	if err != nil || !d.Allowed {
		return false
	}
	if d.Scope == authz.ScopeAll {
		return true
	}
	order, err := salesorder.Get(r.Context(), pool, uuid)
	if err != nil {
		return false
	}
	ok, err := recordInScope(r.Context(), pool, d.Scope, identityID, order.OwnerUserID)
	return err == nil && ok
}

// soCreateExtraMeta returns the audit extraMeta for a create that overrode the
// duplicate guard, or nil otherwise.
func soCreateExtraMeta(o *salesorder.Order) map[string]any {
	if o == nil || !o.DuplicateOverride {
		return nil
	}
	return map[string]any{"duplicate_override": true, "existing_uuid": o.DuplicateOfUUID}
}
