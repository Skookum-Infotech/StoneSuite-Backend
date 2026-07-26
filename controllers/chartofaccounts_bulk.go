package controllers

import (
	"encoding/json"
	"net/http"

	"stonesuite-backend/authz"
	"stonesuite-backend/chartofaccounts"
)

// BulkUpdate PATCH /api/tenant/finance/accounts/bulk — transactional
// activate/hide across many accounts. All-or-nothing: a visibility change
// applied to half a chart of accounts is worse than one applied to none.
func (h *ChartOfAccountsOps) BulkUpdate(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := h.authCOA(w, r, authz.ActionUpdate)
	if !ok {
		return
	}
	var in chartofaccounts.BulkInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	results, err := chartofaccounts.BulkUpdate(r.Context(), pool, in, resolveEmployeeID(r, identityID))
	if err != nil {
		coaFail(w, err, "Failed to update accounts.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "results": results})
}
