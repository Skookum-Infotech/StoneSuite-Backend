package controllers

import (
	"encoding/json"
	"net/http"

	"stonesuite-backend/authz"
	"stonesuite-backend/fabrication"
)

// AddPiece POST /api/tenant/fabrication-jobs/{uuid}/pieces
// Legal only before the job reaches Cutting and not while on hold
// (fabrication.ErrPiecesLocked, 409). Seeds the new piece's checklist steps.
func (h *FabricationOps) AddPiece(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, _, ok := h.authFJByUUID(w, r, uuid, authz.ActionUpdate)
	if !ok {
		return
	}
	var in fabrication.PieceInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	piece, err := fabrication.AddPiece(r.Context(), pool, uuid, in, resolveEmployeeID(r, identityID))
	if err != nil {
		fjFail(w, err, "Failed to add piece.")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "piece": piece})
}

// UpdatePiece PATCH /api/tenant/fabrication-jobs/{uuid}/pieces/{pieceUuid}
func (h *FabricationOps) UpdatePiece(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pieceUUID := r.PathValue("pieceUuid")
	pool, identityID, _, ok := h.authFJByUUID(w, r, uuid, authz.ActionUpdate)
	if !ok {
		return
	}
	var in fabrication.PieceInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	piece, err := fabrication.UpdatePiece(r.Context(), pool, uuid, pieceUUID, in, resolveEmployeeID(r, identityID))
	if err != nil {
		fjFail(w, err, "Failed to update piece.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "piece": piece})
}

// RemovePiece DELETE /api/tenant/fabrication-jobs/{uuid}/pieces/{pieceUuid}
// Blocked while the piece still has a live slab allocation
// (fabrication.ErrPieceHasSlabs, 409).
func (h *FabricationOps) RemovePiece(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pieceUUID := r.PathValue("pieceUuid")
	pool, identityID, _, ok := h.authFJByUUID(w, r, uuid, authz.ActionUpdate)
	if !ok {
		return
	}
	if err := fabrication.RemovePiece(r.Context(), pool, uuid, pieceUUID, resolveEmployeeID(r, identityID)); err != nil {
		fjFail(w, err, "Failed to remove piece.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Piece removed."})
}
