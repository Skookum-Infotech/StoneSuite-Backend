package controllers

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"stonesuite-backend/tenancy"
)

// maxSessionPolicyBodyBytes caps the settings request body.
const maxSessionPolicyBodyBytes = 1 << 10

// sessionPolicyRequest is the body of PUT /api/platform/tenants/{id}/single-session.
// Every field is optional; fields left out keep their current value.
type sessionPolicyRequest struct {
	Enforced                *bool `json:"enforced"`
	TakeoverCooldownMinutes *int  `json:"takeoverCooldownMinutes"`
	TakeoverDailyCap        *int  `json:"takeoverDailyCap"`
}

// validate returns the user-facing problem with the request, or "".
func (req sessionPolicyRequest) validate() string {
	if req.Enforced == nil && req.TakeoverCooldownMinutes == nil && req.TakeoverDailyCap == nil {
		return "Nothing to update."
	}
	if v := req.TakeoverCooldownMinutes; v != nil && (*v < 0 || *v > tenancy.MaxTakeoverCooldownMinutes) {
		return fmt.Sprintf("Wait between device switches must be 0 to %d minutes.", tenancy.MaxTakeoverCooldownMinutes)
	}
	if v := req.TakeoverDailyCap; v != nil && (*v < 0 || *v > tenancy.MaxTakeoverDailyCap) {
		return fmt.Sprintf("Device switches allowed per day must be 0 to %d.", tenancy.MaxTakeoverDailyCap)
	}
	return ""
}

// auditDetails is the JSON recorded in the platform audit log: the fields changed.
func (req sessionPolicyRequest) auditDetails() string {
	b, err := json.Marshal(req)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// SetSingleSession updates a customer's session settings: the
// one-session-per-user rule and its takeover cooldown and daily cap.
// Platform-admin only; the platform-owner tenant is always exempt.
// Path: PUT /api/platform/tenants/{id}/single-session
func (h *TenantOps) SetSingleSession(w http.ResponseWriter, r *http.Request) {
	admin, ok := h.requirePlatformAdmin(r)
	if !ok {
		logSecurityEvent(r, "permission_denied", "context", "tenant_single_session")
		fail(w, http.StatusForbidden, "Platform admin privileges required.")
		return
	}
	id := r.PathValue("id")
	if !validUUID(id) {
		fail(w, http.StatusNotFound, "Tenant not found.")
		return
	}
	var req sessionPolicyRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSessionPolicyBodyBytes)).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	if msg := req.validate(); msg != "" {
		fail(w, http.StatusBadRequest, msg)
		return
	}
	ended, err := h.CP.UpdateTenantSessionPolicy(r.Context(), id, tenancy.SessionPolicyPatch{
		Enforced: req.Enforced, CooldownMinutes: req.TakeoverCooldownMinutes, DailyCap: req.TakeoverDailyCap,
		Grace: sessionHandoverGrace,
	})
	switch {
	case errors.Is(err, tenancy.ErrTenantNotFound):
		fail(w, http.StatusNotFound, "Tenant not found.")
		return
	case errors.Is(err, tenancy.ErrPlatformOwnerExempt):
		fail(w, http.StatusBadRequest, "Platform users are always exempt from the single-session rule.")
		return
	case err != nil:
		slog.Error("set session policy", "tenant", id, "error", err)
		fail(w, http.StatusInternalServerError, "Failed to update the setting.")
		return
	}
	// Switching the rule on ended everyone's extra browsers: make that take
	// effect at once on this machine.
	for _, sid := range ended {
		h.Sessions.Forget(sid)
	}
	details := req.auditDetails()
	logSecurityEvent(r, "single_session_setting_changed", "tenant", id, "actor", admin.ID,
		"changes", details, "sessions_ended", len(ended))
	logAuditErr("platform_audit", h.CP.LogPlatformAudit(r.Context(), admin.ID, admin.Email, id,
		"tenant.single_session", details))
	tenant, err := h.CP.TenantByID(r.Context(), id)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"success": true})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":                 true,
		"singleSessionEnforced":   tenant.SingleSessionEnforced,
		"takeoverCooldownMinutes": tenant.TakeoverCooldownMinutes,
		"takeoverDailyCap":        tenant.TakeoverDailyCap,
	})
}
