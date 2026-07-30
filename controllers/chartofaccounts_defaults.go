package controllers

import (
	"encoding/json"
	"net/http"

	"stonesuite-backend/authz"
	"stonesuite-backend/chartofaccounts"
)

// repointRequest is the PATCH body for a default slot. An empty accountId
// clears the slot.
type repointRequest struct {
	AccountID string `json:"accountId"`
}

// Defaults GET /api/tenant/finance/account-defaults
func (h *ChartOfAccountsOps) Defaults(w http.ResponseWriter, r *http.Request) {
	pool, _, ok := h.authCOA(w, r, authz.ActionRead)
	if !ok {
		return
	}
	slots, err := chartofaccounts.Slots(r.Context(), pool)
	if err != nil {
		coaFail(w, err, "Failed to load default accounts.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "slots": slots})
}

// RepointDefault PATCH /api/tenant/finance/account-defaults/{slotKey}
//
// Guarded by chart_of_account:configure rather than :update — repointing where
// every future transaction posts is a higher-trust act than renaming an account.
func (h *ChartOfAccountsOps) RepointDefault(w http.ResponseWriter, r *http.Request) {
	pool, identityID, ok := h.authCOA(w, r, authz.ActionConfigure)
	if !ok {
		return
	}
	var req repointRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	slot, err := chartofaccounts.RepointSlot(r.Context(), pool,
		r.PathValue("slotKey"), req.AccountID, resolveEmployeeID(r, identityID))
	if err != nil {
		coaFail(w, err, "Failed to update the default account.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "slot": slot})
}
