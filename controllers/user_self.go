package controllers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"stonesuite-backend/middleware"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/userstore"
)

// maxProfileNameLen caps a self-edited display name. It matches the limit the
// admin "Edit display name" dialog already enforces, and sits well under the
// users.full_name VARCHAR(255) column.
const maxProfileNameLen = 120

// userStatusActive is the only users.status a caller may edit their own
// profile under; a suspended or disabled member's still-unexpired token must
// not be able to keep writing.
const userStatusActive = "active"

// normalizeProfileName trims a submitted display name and validates it. It
// returns the cleaned name and, when invalid, the message to send back with a
// 400 (empty otherwise). Length counts characters, not bytes, because the
// column limit is in characters.
func normalizeProfileName(raw string) (name, invalidMsg string) {
	name = strings.TrimSpace(raw)
	if name == "" {
		return "", "Name is required."
	}
	if utf8.RuneCountInString(name) > maxProfileNameLen {
		return "", "Name is too long."
	}
	return name, ""
}

// UpdateMyProfile PATCH /api/tenant/users/me
// Lets any active workspace member change their own display name -- unlike
// PATCH /api/tenant/users/{id} (UpdateUser), which needs user:update because
// it can also change status and target other people.
//
// There is deliberately no resource permission check here, and no IDOR guard:
// the row edited is resolved from the caller's own JWT identity, never from a
// client-supplied id, so a caller can only ever reach their own row. The body
// is a fixed shape holding fullName alone, so status, email and roles cannot be
// touched through this route.
func (h *UserOps) UpdateMyProfile(w http.ResponseWriter, r *http.Request) {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return
	}

	var req struct {
		FullName string `json:"fullName"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	fullName, invalidMsg := normalizeProfileName(req.FullName)
	if invalidMsg != "" {
		fail(w, http.StatusBadRequest, invalidMsg)
		return
	}

	me, err := userstore.GetUserByIdentityID(r.Context(), pool, payload.ID)
	if errors.Is(err, userstore.ErrUserNotFound) {
		fail(w, http.StatusNotFound, "User not found.")
		return
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load user.")
		return
	}
	if me.Status != userStatusActive {
		logSecurityEvent(r, "profile_update_denied_inactive", "user_id", me.ID, "status", me.Status)
		fail(w, http.StatusForbidden, "Your account is not active.")
		return
	}

	// Status is passed empty so UpdateUser leaves it untouched.
	u, err := userstore.UpdateUser(r.Context(), pool, me.ID, fullName, "")
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to update profile.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "user": u})
}
