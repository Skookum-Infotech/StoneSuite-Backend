package controllers

import (
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/vendorbill"
)

// writeVBDuplicate writes the 409 for a *vendorbill.DuplicateDocumentError and
// reports whether err was one. Same shape and disclosure rule as
// writeSODuplicate: the existing bill's uuid/number are only named when the
// caller could read that bill themselves (read grant + recordInScope), so the
// guard can't be used to enumerate bills outside the caller's scope.
func writeVBDuplicate(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, identityID string, err error) bool {
	var dup *vendorbill.DuplicateDocumentError
	if !errors.As(err, &dup) {
		return false
	}
	writeJSON(w, http.StatusConflict, vbDuplicateBody(dup, canReadVB(r, pool, identityID, dup.ExistingUUID)))
	return true
}

// vbDuplicateBody builds the 409 JSON body; the existing bill is only named
// when the caller could read it.
func vbDuplicateBody(dup *vendorbill.DuplicateDocumentError, disclose bool) map[string]any {
	body := map[string]any{
		"success": false,
		"message": "A vendor bill with this invoice number already exists for this vendor.",
		"code":    codeDuplicateDocument,
	}
	if disclose {
		body["message"] = fmt.Sprintf("A vendor bill with this invoice number already exists for this vendor (%s).", dup.ExistingNumber)
		body["existingUuid"] = dup.ExistingUUID
		body["existingNumber"] = dup.ExistingNumber
	}
	return body
}

// canReadVB reports whether the caller may read the given vendor bill: a read
// grant and, below `all` scope, ownership. Any lookup error fails closed.
func canReadVB(r *http.Request, pool *pgxpool.Pool, identityID, uuid string) bool {
	d, err := authz.Check(r.Context(), pool, identityID, authz.ResourceVendorBill, authz.ActionRead)
	if err != nil || !d.Allowed {
		return false
	}
	if d.Scope == authz.ScopeAll {
		return true
	}
	bill, err := vendorbill.Get(r.Context(), pool, uuid)
	if err != nil {
		return false
	}
	ok, err := recordInScope(r.Context(), pool, d.Scope, identityID, bill.OwnerUserID)
	return err == nil && ok
}

// vbCreateExtraMeta returns the audit extraMeta for a create that overrode the
// duplicate guard, or nil otherwise.
func vbCreateExtraMeta(b *vendorbill.VendorBill) map[string]any {
	if b == nil || !b.DuplicateOverride {
		return nil
	}
	return map[string]any{"duplicate_override": true, "existing_uuid": b.DuplicateOfUUID}
}
