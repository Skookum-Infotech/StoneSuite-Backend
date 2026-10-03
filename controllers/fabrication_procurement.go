package controllers

import (
	"errors"
	"net/http"
	"stonesuite-backend/authz"
	"stonesuite-backend/fabrication"
)

// ProcurementOrders lists visible linked orders and posted receipt quantities.
func (h *FabricationOps) ProcurementOrders(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, identity, _, ok := h.authFJByUUID(w, r, id, authz.ActionRead)
	if !ok {
		return
	}
	orders, err := fabrication.ProcurementOrders(r.Context(), pool, id, identity)
	if errors.Is(err, fabrication.ErrPurchaseReadPermission) {
		logSecurityEvent(r, "permission_denied", "identity", identity, "resource", string(authz.ResourcePurchaseOrder), "action", string(authz.ActionRead))
		fail(w, http.StatusForbidden, "Purchase order read permission required.")
		return
	}
	if err != nil {
		fabricationActionFail(w, r, err, identity, authz.ActionRead)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Success bool                           `json:"success"`
		Orders  []fabrication.ProcurementOrder `json:"orders"`
	}{true, orders})
}

// ProcurementAction returns current shortage purchase availability independently of PO read access.
func (h *FabricationOps) ProcurementAction(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, identity, _, ok := h.authFJByUUID(w, r, id, authz.ActionRead)
	if !ok {
		return
	}
	action, err := fabrication.ProcurementAction(r.Context(), pool, id, identity, resolveEmployeeID(r, identity))
	if err != nil {
		fabricationActionFail(w, r, err, identity, authz.ActionRead)
		return
	}
	writeJSON(w, http.StatusOK, struct {
		Success bool                        `json:"success"`
		Action  fabrication.AvailableAction `json:"action"`
	}{true, action})
}
