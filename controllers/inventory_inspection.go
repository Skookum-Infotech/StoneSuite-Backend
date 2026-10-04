package controllers

import (
	"encoding/json"
	"net/http"
	"stonesuite-backend/authz"
	"stonesuite-backend/inventory"
)

// Inspect records whole-slab acceptance or rejection after receipt.
func (h *InventoryUnitOps) Inspect(w http.ResponseWriter, r *http.Request) {
	pool, identity, ok := h.authByUUID(w, r, authz.ActionUpdate)
	if !ok {
		return
	}
	var in inventory.InspectionInput
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, templateBodyLimit))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid inspection input.")
		return
	}
	if err := inventory.InspectUnit(r.Context(), pool, r.PathValue("uuid"), in, resolveEmployeeID(r, identity)); err != nil {
		inventoryFail(w, err, "Failed to record inspection.")
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Success bool `json:"success"`
	}{true})
}
