package controllers

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"stonesuite-backend/tenancy"
)

// singleSessionRequest is the body of PUT /api/platform/tenants/{id}/single-session.
type singleSessionRequest struct {
	Enforced *bool `json:"enforced"`
}

// SetSingleSession turns the one-session-per-user rule on or off for a
// customer. Platform-admin only; the platform-owner tenant is always exempt.
// Path: PUT /api/platform/tenants/{id}/single-session
func (h *TenantOps) SetSingleSession(w http.ResponseWriter, r *http.Request) {
	admin, ok := h.requirePlatformAdmin(r)
	if !ok {
		logSecurityEvent(r, "permission_denied", "context", "tenant_single_session")
		fail(w, http.StatusForbidden, "Platform admin privileges required.")
		return
	}
	var req singleSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Enforced == nil {
		fail(w, http.StatusBadRequest, "Body must be {\"enforced\": true|false}.")
		return
	}
	id := r.PathValue("id")
	err := h.CP.SetTenantSingleSession(r.Context(), id, *req.Enforced)
	switch {
	case errors.Is(err, tenancy.ErrTenantNotFound):
		fail(w, http.StatusNotFound, "Tenant not found.")
		return
	case errors.Is(err, tenancy.ErrPlatformOwnerExempt):
		fail(w, http.StatusBadRequest, "Platform users are always exempt from the single-session rule.")
		return
	case err != nil:
		slog.Error("set single session", "tenant", id, "error", err)
		fail(w, http.StatusInternalServerError, "Failed to update the setting.")
		return
	}
	logSecurityEvent(r, "single_session_setting_changed", "tenant", id,
		"actor", admin.ID, "enforced", *req.Enforced)
	logAuditErr("platform_audit", h.CP.LogPlatformAudit(r.Context(), admin.ID, admin.Email, id,
		"tenant.single_session", fmt.Sprintf(`{"enforced":%t}`, *req.Enforced)))
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "singleSessionEnforced": *req.Enforced})
}
