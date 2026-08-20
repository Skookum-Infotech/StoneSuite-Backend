// controllers/customer_portal.go
package controllers

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strconv"

	"stonesuite-backend/customernote"
	"stonesuite-backend/middleware"
	"stonesuite-backend/services"
	"stonesuite-backend/tenancy"
)

// CustomerPortalOps handles the customer-facing side of the customer note
// feature: an authenticated external customer (middleware.RequireCustomerAuth)
// submitting and viewing their own notes. Every handler is scoped to exactly
// the customer identified by the caller's verified JWT — there is no RBAC
// here, and no request input is ever trusted for scoping (see
// middleware.CustomerContextPayload).
//
// Routes:
//
//	POST /api/customer/notes — submit a note
//	GET  /api/customer/notes — list the caller's own notes
type CustomerPortalOps struct{}

// NewCustomerPortalOps constructs the handler group.
func NewCustomerPortalOps() *CustomerPortalOps { return &CustomerPortalOps{} }

// customerNoteFail maps a customernote store error to an HTTP response.
func customerNoteFail(w http.ResponseWriter, err error, serverMsg string) {
	switch {
	case errors.Is(err, customernote.ErrNotFound):
		fail(w, http.StatusNotFound, "Note not found.")
	case customernote.IsClientError(err):
		fail(w, http.StatusBadRequest, err.Error())
	default:
		fail(w, http.StatusInternalServerError, serverMsg)
	}
}

// selfCustomerID resolves the caller's own internal customer_id from their
// verified JWT claims — never from request input.
func selfCustomerID(w http.ResponseWriter, r *http.Request) (int, string, bool) {
	payload, err := middleware.GetCustomerFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return 0, "", false
	}
	customerID, err := strconv.Atoi(payload.CustomerID)
	if err != nil {
		fail(w, http.StatusUnauthorized, "Invalid session.")
		return 0, "", false
	}
	return customerID, payload.CustomerIdentityID, true
}

// CreateNote POST /api/customer/notes
func (h *CustomerPortalOps) CreateNote(w http.ResponseWriter, r *http.Request) {
	customerID, customerIdentityID, ok := selfCustomerID(w, r)
	if !ok {
		return
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return
	}

	var in customernote.CreateNoteInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}

	note, err := customernote.Create(r.Context(), pool, customerID, customerIdentityID, in)
	if err != nil {
		customerNoteFail(w, err, "Failed to submit note.")
		return
	}

	// Confirmation email is a non-fatal side effect — the note is already
	// saved regardless of whether it succeeds, matching the existing
	// invite-email precedent (controllers/user.go InviteUser).
	if tenant, tErr := tenancy.TenantFromContext(r.Context()); tErr == nil {
		if mErr := services.SendCustomerNoteConfirmationEmail(note.Submitter.Email, note.Submitter.Name, tenant.DisplayName); mErr != nil {
			log.Printf("customer note confirmation email to %s failed (note still saved): %v", note.Submitter.Email, mErr)
		}
	}

	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "note": note})
}

// ListMyNotes GET /api/customer/notes
func (h *CustomerPortalOps) ListMyNotes(w http.ResponseWriter, r *http.Request) {
	customerID, _, ok := selfCustomerID(w, r)
	if !ok {
		return
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return
	}

	notes, err := customernote.ListByCustomerID(r.Context(), pool, customerID)
	if err != nil {
		customerNoteFail(w, err, "Failed to load notes.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "notes": notes})
}
