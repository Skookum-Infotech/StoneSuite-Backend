package controllers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"stonesuite-backend/portal"
)

// maxPortalNameLen / maxPortalPhoneLen match the customer_portal_user columns.
const (
	maxPortalNameLen  = 150
	maxPortalPhoneLen = 20
)

// Me serves GET and PATCH on /api/portal/me.
//
// GET returns who the caller is and which customer they represent. PATCH edits
// the two fields a customer owns about themselves.
//
// Email is absent from both the editable set and any update path: it is the
// login and is globally unique on the control-plane identities table, so
// changing it is an identity operation with cross-tenant consequences, not a
// profile edit.
func (h *PortalDocumentOps) Me(w http.ResponseWriter, r *http.Request) {
	pool, sess, ok := portalSession(w, r)
	if !ok {
		return
	}

	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"profile": map[string]any{
				"email": sess.Email, "fullName": sess.FullName, "phone": sess.Phone,
			},
			"customer": map[string]any{
				"id": sess.CustomerUUID, "name": sess.CustomerName,
			},
		})

	case http.MethodPatch:
		var req struct {
			FullName *string `json:"fullName"`
			Phone    *string `json:"phone"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			fail(w, http.StatusBadRequest, "Invalid request body.")
			return
		}
		// Absent fields keep their current value; the store does a full update,
		// so unspecified fields are carried forward from the session.
		fullName, phone := sess.FullName, sess.Phone
		if req.FullName != nil {
			fullName = strings.TrimSpace(*req.FullName)
		}
		if req.Phone != nil {
			phone = strings.TrimSpace(*req.Phone)
		}
		if len(fullName) > maxPortalNameLen {
			fail(w, http.StatusBadRequest, "Name is too long.")
			return
		}
		if len(phone) > maxPortalPhoneLen {
			fail(w, http.StatusBadRequest, "Phone number is too long.")
			return
		}

		payloadIdentity, err := portalIdentityID(r)
		if err != nil {
			fail(w, http.StatusUnauthorized, "Authentication required.")
			return
		}
		if err := portal.UpdateProfile(r.Context(), pool, payloadIdentity, fullName, phone); err != nil {
			if errors.Is(err, portal.ErrPortalUserNotFound) {
				fail(w, http.StatusUnauthorized, "Your portal access is not available.")
				return
			}
			fail(w, http.StatusInternalServerError, "Failed to update profile.")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"success": true,
			"profile": map[string]any{
				"email": sess.Email, "fullName": fullName, "phone": phone,
			},
		})

	default:
		fail(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}
