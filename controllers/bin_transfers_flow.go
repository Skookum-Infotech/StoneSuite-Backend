// controllers/bin_transfers_flow.go — the status-moving endpoints of a bin
// transfer, split out of bin_transfers.go to keep both under the file-size cap.
package controllers

import (
	"encoding/json"
	"errors"
	"net/http"

	"stonesuite-backend/authz"
	"stonesuite-backend/bintransfer"
)

// btTransitionRequest is the body of POST .../bin-transfers/{uuid}/transition.
// Note is kept as the cancel reason when cancelling.
type btTransitionRequest struct {
	ToStatusCode string `json:"toStatusCode"`
	Note         string `json:"note"`
}

// Transition POST /api/tenant/inventory/bin-transfers/{uuid}/transition
//
// Submit, revise or cancel. Completed is refused here -- it moves stone, so it
// goes through Complete. A move onto Approved needs bin_transfer:approve and
// every other move bin_transfer:transition (actionForTransition).
func (h *BinTransferOps) Transition(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	// Decoded before the permission check because the target status selects which
	// permission applies; it touches no database and leaks nothing.
	var req btTransitionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ToStatusCode == "" {
		fail(w, http.StatusBadRequest, "toStatusCode is required.")
		return
	}
	pool, identityID, _, ok := h.authBinTransferByUUID(w, r, uuid, actionForTransition(req.ToStatusCode))
	if !ok {
		return
	}
	before, _ := bintransfer.Get(r.Context(), pool, uuid)
	bt, err := bintransfer.Transition(r.Context(), pool, uuid, req.ToStatusCode, req.Note, resolveEmployeeID(r, identityID))
	if err != nil {
		binTransferFail(w, err, "Failed to change the bin transfer's status.")
		return
	}
	auditBT(r, pool, identityID, "transition", uuid, before, bt)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "binTransfer": bt})
}

// Approve POST /api/tenant/inventory/bin-transfers/{uuid}/approve
//
// One configured approver's sign-off (or a super admin's override). Once the
// chain is satisfied the transfer advances to Approved; no slab moves until
// Complete.
func (h *BinTransferOps) Approve(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, _, ok := h.authBinTransferByUUID(w, r, uuid, authz.ActionApprove)
	if !ok {
		return
	}
	isSuperAdmin, err := authz.IsSuperAdmin(r.Context(), pool, identityID)
	if err != nil {
		binTransferFail(w, err, "Failed to approve bin transfer.")
		return
	}
	bt, err := bintransfer.Approve(r.Context(), pool, uuid, resolveEmployeeID(r, identityID), isSuperAdmin)
	if err != nil {
		if errors.Is(err, bintransfer.ErrNotApprover) {
			logSecurityEvent(r, "approval_denied", "identity", identityID, "record", uuid)
		}
		binTransferFail(w, err, "Failed to approve bin transfer.")
		return
	}
	auditBT(r, pool, identityID, "approve", uuid, nil, bt)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "binTransfer": bt})
}

// Reject POST /api/tenant/inventory/bin-transfers/{uuid}/reject  body {"reason":"..."}
//
// An approver's veto: the transfer returns to Draft with the reason kept.
// Authorized like Approve -- the caller must be a configured approver of the
// current status, or a super admin.
func (h *BinTransferOps) Reject(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, _, ok := h.authBinTransferByUUID(w, r, uuid, authz.ActionApprove)
	if !ok {
		return
	}
	var req struct {
		Reason string `json:"reason"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	isSuperAdmin, err := authz.IsSuperAdmin(r.Context(), pool, identityID)
	if err != nil {
		binTransferFail(w, err, "Failed to reject bin transfer.")
		return
	}
	bt, err := bintransfer.Reject(r.Context(), pool, uuid, resolveEmployeeID(r, identityID), isSuperAdmin, req.Reason)
	if err != nil {
		if errors.Is(err, bintransfer.ErrNotApprover) {
			logSecurityEvent(r, "approval_denied", "identity", identityID, "record", uuid)
		}
		binTransferFail(w, err, "Failed to reject bin transfer.")
		return
	}
	auditBT(r, pool, identityID, "reject", uuid, nil, bt)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "binTransfer": bt})
}

// Complete POST /api/tenant/inventory/bin-transfers/{uuid}/complete
//
// Applies the transfer: every slab moves to its destination bin in one
// transaction, or none does.
func (h *BinTransferOps) Complete(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, _, ok := h.authBinTransferByUUID(w, r, uuid, authz.ActionTransition)
	if !ok {
		return
	}
	before, _ := bintransfer.Get(r.Context(), pool, uuid)
	bt, err := bintransfer.Complete(r.Context(), pool, uuid, resolveEmployeeID(r, identityID))
	if err != nil {
		binTransferFail(w, err, "Failed to complete the bin transfer.")
		return
	}
	auditBT(r, pool, identityID, "complete", uuid, before, bt)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "binTransfer": bt})
}

// History GET /api/tenant/inventory/bin-transfers/{uuid}/history
func (h *BinTransferOps) History(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, _, _, ok := h.authBinTransferByUUID(w, r, uuid, authz.ActionRead)
	if !ok {
		return
	}
	entries, err := bintransfer.History(r.Context(), pool, uuid)
	if err != nil {
		binTransferFail(w, err, "Failed to load the bin transfer's history.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "records": entries})
}
