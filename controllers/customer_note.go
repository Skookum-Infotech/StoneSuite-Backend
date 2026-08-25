// controllers/customer_note.go
package controllers

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/customernote"
)

// CustomerNoteOps handles the staff-facing side of customer-submitted notes:
// a sub-resource of a CRM customer record. Every handler first resolves +
// IDOR-guards the parent record via CRMOps.authCRMByRecordID (requiring
// customer:read), then separately checks customer_note:<action>, mirroring
// CRMActivityOps's two-permission (source + target) pattern.
//
// Routes:
//
//	GET    /api/tenant/crm/customer/records/{id}/notes             — list
//	PATCH  /api/tenant/crm/customer/records/{id}/notes/{noteId}     — update status
//	DELETE /api/tenant/crm/customer/records/{id}/notes/{noteId}     — soft delete
type CustomerNoteOps struct {
	crm *CRMOps
}

// NewCustomerNoteOps constructs the handler group.
func NewCustomerNoteOps() *CustomerNoteOps { return &CustomerNoteOps{crm: NewCRMOps()} }

// authNote resolves the parent CRM record (IDOR-guarded, read access
// required) then checks customer_note:<action>.
func (h *CustomerNoteOps) authNote(w http.ResponseWriter, r *http.Request, recordID string, action authz.Action) (*pgxpool.Pool, string, bool) {
	_, pool, _, identityID, ok := h.crm.authCRMByRecordID(w, r, recordID, authz.ActionRead)
	if !ok {
		return nil, "", false
	}
	decision, err := authz.Check(r.Context(), pool, identityID, authz.ResourceCustomerNote, action)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, "", false
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", identityID, "resource", string(authz.ResourceCustomerNote), "action", string(action),
			"parent_record", recordID)
		fail(w, http.StatusForbidden, "You do not have permission to "+string(action)+" customer notes.")
		return nil, "", false
	}
	return pool, identityID, true
}

// customerNoteStaffFail maps a store error to an HTTP response, logging an
// IDOR probe when a noteId simply doesn't belong to recordID.
func customerNoteStaffFail(w http.ResponseWriter, r *http.Request, err error, recordID, noteID string, action authz.Action, identityID string) {
	if errors.Is(err, customernote.ErrNotFound) {
		logSecurityEvent(r, "idor_denied",
			"identity", identityID, "record", recordID, "resource", string(authz.ResourceCustomerNote),
			"action", string(action), "note", noteID)
	}
	customerNoteFail(w, err, "Failed to process customer note.")
}

// List GET /api/tenant/crm/customer/records/{id}/notes
func (h *CustomerNoteOps) List(w http.ResponseWriter, r *http.Request) {
	recordID := r.PathValue("id")
	pool, _, ok := h.authNote(w, r, recordID, authz.ActionRead)
	if !ok {
		return
	}
	notes, err := customernote.ListByCustomerRecord(r.Context(), pool, recordID)
	if err != nil {
		customerNoteFail(w, err, "Failed to load notes.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "recordId": recordID, "notes": notes})
}

// UpdateStatus PATCH /api/tenant/crm/customer/records/{id}/notes/{noteId}
func (h *CustomerNoteOps) UpdateStatus(w http.ResponseWriter, r *http.Request) {
	recordID := r.PathValue("id")
	noteID := r.PathValue("noteId")
	pool, identityID, ok := h.authNote(w, r, recordID, authz.ActionUpdate)
	if !ok {
		return
	}
	var in customernote.UpdateStatusInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	note, err := customernote.UpdateStatus(r.Context(), pool, recordID, noteID, in, resolveEmployeeID(r, identityID))
	if err != nil {
		customerNoteStaffFail(w, r, err, recordID, noteID, authz.ActionUpdate, identityID)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "note": note})
}

// Delete DELETE /api/tenant/crm/customer/records/{id}/notes/{noteId}
func (h *CustomerNoteOps) Delete(w http.ResponseWriter, r *http.Request) {
	recordID := r.PathValue("id")
	noteID := r.PathValue("noteId")
	pool, identityID, ok := h.authNote(w, r, recordID, authz.ActionDelete)
	if !ok {
		return
	}
	if err := customernote.SoftDelete(r.Context(), pool, recordID, noteID, resolveEmployeeID(r, identityID)); err != nil {
		customerNoteStaffFail(w, r, err, recordID, noteID, authz.ActionDelete, identityID)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Note deleted."})
}
