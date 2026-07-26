package controllers

import (
	"net/http"
	"strconv"

	"stonesuite-backend/authz"
	"stonesuite-backend/chartofaccounts"
)

// History GET /api/tenant/finance/accounts/{uuid}/history — the audit trail.
// Bank account numbers never appear here: appendHistory redacts that field, so
// only the fact of a change is recorded (AD-10).
func (h *ChartOfAccountsOps) History(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.authCOA(w, r, authz.ActionRead)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	entries, err := chartofaccounts.History(r.Context(), pool, r.PathValue("uuid"), limit)
	if err != nil {
		coaFail(w, err, "Failed to load account history.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "history": entries})
}
