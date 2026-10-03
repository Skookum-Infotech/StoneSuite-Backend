package controllers

import (
	"encoding/json"
	"errors"
	"net/http"
	"stonesuite-backend/authz"
	"stonesuite-backend/fabrication"
)

// TransferMaterialToWIP moves inspected, allocated material into a WIP bin.
func (h *FabricationOps) TransferMaterialToWIP(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, identity, _, ok := h.authFJByUUID(w, r, id, authz.ActionUpdate)
	if !ok {
		return
	}
	var in fabrication.WIPTransferInput
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, templateBodyLimit))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid WIP transfer.")
		return
	}
	result, err := fabrication.TransferToWIP(r.Context(), pool, id, fabrication.ActionActor{IdentityID: identity, EmployeeID: resolveEmployeeID(r, identity)}, in)
	if errors.Is(err, fabrication.ErrMovementPermission) {
		logSecurityEvent(r, "permission_denied", "identity", identity, "resource", string(authz.ResourceInventoryUnit), "action", string(authz.ActionUpdate))
		fail(w, http.StatusForbidden, "You do not have permission to move inventory.")
		return
	}
	if err != nil {
		fabricationActionFail(w, r, err, identity, authz.ActionUpdate)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Success bool                     `json:"success"`
		Result  fabrication.ActionResult `json:"result"`
	}{true, result})
}

// WIPOptions returns allocated materials and permitted movement actions.
func (h *FabricationOps) WIPOptions(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, identity, _, ok := h.authFJByUUID(w, r, id, authz.ActionRead)
	if !ok {
		return
	}
	decision, err := authz.Check(r.Context(), pool, identity, authz.ResourceInventoryUnit, authz.ActionRead)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to check inventory access.")
		return
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied", "identity", identity, "resource", string(authz.ResourceInventoryUnit), "action", string(authz.ActionRead))
		fail(w, http.StatusForbidden, "Inventory read permission required.")
		return
	}
	options, err := fabrication.WIPOptions(r.Context(), pool, id, identity)
	if err != nil {
		fabricationActionFail(w, r, err, identity, authz.ActionRead)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Success bool                    `json:"success"`
		Options []fabrication.WIPOption `json:"options"`
	}{true, options})
}
