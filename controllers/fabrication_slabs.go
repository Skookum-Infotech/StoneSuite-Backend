package controllers

import (
	"encoding/json"
	"net/http"

	"stonesuite-backend/authz"
	"stonesuite-backend/fabrication"
)

// JobSlabs GET /api/tenant/fabrication-jobs/{uuid}/slabs — allocated slabs.
// Needs installation:read AND inventory_item:read (§3.1).
func (h *FabricationOps) JobSlabs(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, _, ok := h.authFJByUUID(w, r, uuid, authz.ActionRead)
	if !ok {
		return
	}
	if !h.requireInventory(w, r, pool, identityID, authz.ActionRead) {
		return
	}
	slabs, err := fabrication.InventoryForJob(r.Context(), pool, uuid)
	if err != nil {
		fjFail(w, err, "Failed to load job slabs.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "slabs": slabs})
}

// AllocateSlab POST /api/tenant/fabrication-jobs/{uuid}/slabs
// body {"slabUuid":"...","pieceUuid":"..."}. Needs installation:update AND
// inventory_item:update.
func (h *FabricationOps) AllocateSlab(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, _, ok := h.authFJByUUID(w, r, uuid, authz.ActionUpdate)
	if !ok {
		return
	}
	if !h.requireInventory(w, r, pool, identityID, authz.ActionUpdate) {
		return
	}
	var req struct {
		SlabUUID  string `json:"slabUuid"`
		PieceUUID string `json:"pieceUuid"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.SlabUUID == "" {
		fail(w, http.StatusBadRequest, "slabUuid is required.")
		return
	}
	if err := fabrication.AllocateSlab(r.Context(), pool, uuid, req.SlabUUID, req.PieceUUID, resolveEmployeeID(r, identityID)); err != nil {
		fjFail(w, err, "Failed to allocate slab.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Slab allocated."})
}

// DeallocateSlab DELETE /api/tenant/fabrication-jobs/{uuid}/slabs/{slabUuid}
func (h *FabricationOps) DeallocateSlab(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	slabUUID := r.PathValue("slabUuid")
	pool, identityID, _, ok := h.authFJByUUID(w, r, uuid, authz.ActionUpdate)
	if !ok {
		return
	}
	if !h.requireInventory(w, r, pool, identityID, authz.ActionUpdate) {
		return
	}
	if err := fabrication.DeallocateSlab(r.Context(), pool, uuid, slabUUID, resolveEmployeeID(r, identityID)); err != nil {
		fjFail(w, err, "Failed to deallocate slab.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Slab released."})
}

// Disposition POST /api/tenant/fabrication-jobs/{uuid}/slabs/{slabUuid}/disposition
// Declares the fate of a consumed slab on a cancelling job (§4.4).
func (h *FabricationOps) Disposition(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	slabUUID := r.PathValue("slabUuid")
	pool, identityID, _, ok := h.authFJByUUID(w, r, uuid, authz.ActionUpdate)
	if !ok {
		return
	}
	if !h.requireInventory(w, r, pool, identityID, authz.ActionUpdate) {
		return
	}
	var in fabrication.DispositionInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	in.SlabUUID = slabUUID
	if err := fabrication.RecordDisposition(r.Context(), pool, uuid, in, resolveEmployeeID(r, identityID)); err != nil {
		fjFail(w, err, "Failed to record disposition.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Disposition recorded."})
}
