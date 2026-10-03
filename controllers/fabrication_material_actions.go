package controllers

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"stonesuite-backend/authz"
	"stonesuite-backend/fabrication"
)

// AllocateMaterial reserves material with a reviewed layout on the approved template.
func (h *FabricationOps) AllocateMaterial(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, identity, _, ok := h.authFJByUUID(w, r, id, authz.ActionUpdate)
	if !ok {
		return
	}
	var in fabrication.AllocateMaterialInput
	if !decodeMaterialAction(w, r, &in) {
		return
	}
	result, err := fabrication.AllocateMaterial(r.Context(), pool, id, fabrication.ActionActor{IdentityID: identity, EmployeeID: resolveEmployeeID(r, identity)}, in)
	writeMaterialAction(w, r, identity, result, err)
}

// ReleaseMaterial releases a reserved slab without discarding layout evidence.
func (h *FabricationOps) ReleaseMaterial(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, identity, _, ok := h.authFJByUUID(w, r, id, authz.ActionUpdate)
	if !ok {
		return
	}
	var in fabrication.ReleaseMaterialInput
	if !decodeMaterialAction(w, r, &in) {
		return
	}
	result, err := fabrication.ReleaseMaterial(r.Context(), pool, id, fabrication.ActionActor{IdentityID: identity, EmployeeID: resolveEmployeeID(r, identity)}, in)
	writeMaterialAction(w, r, identity, result, err)
}

func decodeMaterialAction[T fabrication.AllocateMaterialInput | fabrication.ReleaseMaterialInput | fabrication.ShortagePurchaseInput](w http.ResponseWriter, r *http.Request, in *T) bool {
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, templateBodyLimit))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid material action.")
		return false
	}
	var trailing json.RawMessage
	if err := decoder.Decode(&trailing); err != io.EOF {
		fail(w, http.StatusBadRequest, "A single material action is required.")
		return false
	}
	return true
}

func writeMaterialAction(w http.ResponseWriter, r *http.Request, identity string, result fabrication.ActionResult, err error) {
	if errors.Is(err, fabrication.ErrMovementPermission) {
		logSecurityEvent(r, "permission_denied", "identity", identity, "resource", string(authz.ResourceInventoryUnit), "action", string(authz.ActionUpdate))
		fail(w, http.StatusForbidden, "Inventory update permission required.")
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

// CreateShortagePurchase creates a draft linked to approved fabrication requirements.
func (h *FabricationOps) CreateShortagePurchase(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, identity, _, ok := h.authFJByUUID(w, r, id, authz.ActionUpdate)
	if !ok {
		return
	}
	var in fabrication.ShortagePurchaseInput
	if !decodeMaterialAction(w, r, &in) {
		return
	}
	result, err := fabrication.CreateShortagePurchase(r.Context(), pool, id, fabrication.ActionActor{IdentityID: identity, EmployeeID: resolveEmployeeID(r, identity)}, in)
	if errors.Is(err, fabrication.ErrPurchasePermission) {
		logSecurityEvent(r, "permission_denied", "identity", identity, "resource", string(authz.ResourcePurchaseOrder), "action", string(authz.ActionCreate))
		fail(w, http.StatusForbidden, "Purchase order create permission required.")
		return
	}
	writeMaterialAction(w, r, identity, result, err)
}
