package controllers

import (
	"log/slog"
	"net/http"
	"strings"

	"stonesuite-backend/authz"
	"stonesuite-backend/middleware"
	"stonesuite-backend/userstore"
)

// Super admins can be seen by anyone who may read users, but only a super
// admin may change one: rename, suspend, deactivate, or change their roles.
// Otherwise a holder of plain user:update / role:update could lock out or
// demote the people who hold the keys to the workspace.

const msgSuperAdminOnly = "Only a super admin can change another super admin."

// blockNonSuperAdmin writes a 403 and returns true when the member targetUserID
// is a super admin and the signed-in caller is not. A target id that isn't a
// valid id is left for the handler's own lookup to reject.
func blockNonSuperAdmin(w http.ResponseWriter, r *http.Request, q userstore.Querier, targetUserID string) bool {
	if !userIDPattern.MatchString(strings.ToLower(targetUserID)) {
		return false
	}
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return true
	}
	targetIsSA, err := userstore.IsSuperAdmin(r.Context(), q, targetUserID)
	if err != nil {
		slog.ErrorContext(r.Context(), "check super admin target", "user", targetUserID, "error", err)
		fail(w, http.StatusInternalServerError, "Failed to check admin status.")
		return true
	}
	if !targetIsSA {
		return false
	}
	callerIsSA, err := authz.IsSuperAdmin(r.Context(), q, payload.ID)
	if err != nil {
		slog.ErrorContext(r.Context(), "check super admin caller", "identity", payload.ID, "error", err)
		fail(w, http.StatusInternalServerError, "Failed to check admin status.")
		return true
	}
	if callerIsSA {
		return false
	}
	logSecurityEvent(r, "super_admin_change_denied", "identity", payload.ID, "target_user", targetUserID)
	fail(w, http.StatusForbidden, msgSuperAdminOnly)
	return true
}
