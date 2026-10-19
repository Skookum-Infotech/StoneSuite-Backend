package controllers

import (
	"context"
	"log/slog"
	"net/http"

	"stonesuite-backend/automation"
	"stonesuite-backend/middleware"
)

// msgAutomationScope tells the admin what this switch controls, since the
// tick also dispatches scheduled vendor payments, not only reminders.
const msgAutomationScope = "Controls the 15-minute scheduler for all tenants, including scheduled vendor-payment dispatch."

// automationSwitch is the platform switch store (automation.PlatformStore).
type automationSwitch interface {
	State(ctx context.Context) (automation.PlatformState, error)
	Set(ctx context.Context, enabled bool, by string) error
}

// automationMirror pushes the switch to Cloudflare KV (automation.KVMirror).
type automationMirror interface {
	Configured() bool
	Set(ctx context.Context, enabled bool) error
}

// AutomationPlatformOps serves GET/PUT /api/platform/automation: the
// platform-wide on/off switch for date-triggered automation. Platform-admin
// only (RequireAuth + IsPlatformAdmin), no tenant chain: the switch is per
// environment, not per tenant.
type AutomationPlatformOps struct {
	cp     platformAdminChecker
	store  automationSwitch
	mirror automationMirror
	// requireKV makes enabling fail with 409 while the KV mirror is not
	// configured: the Worker would never see "on", so the switch would claim
	// success while nothing ever ran.
	requireKV bool
}

// NewAutomationPlatformOps wires the handler. mirror may be a nil
// *automation.KVMirror (Configured() is false): the DB switch still works,
// but the Worker is not told, so it keeps its previous state. Set requireKV in
// production so enabling without a configured mirror is refused (409).
func NewAutomationPlatformOps(cp platformAdminChecker, store automationSwitch, mirror automationMirror, requireKV bool) *AutomationPlatformOps {
	return &AutomationPlatformOps{cp: cp, store: store, mirror: mirror, requireKV: requireKV}
}

type automationPlatformResponse struct {
	Success bool `json:"success"`
	automation.PlatformState
	KVConfigured bool   `json:"kvConfigured"`
	KVSynced     *bool  `json:"kvSynced,omitempty"`
	Message      string `json:"message"`
}

func (h *AutomationPlatformOps) requirePlatformAdmin(w http.ResponseWriter, r *http.Request) (middleware.UserContextPayload, bool) {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return payload, false
	}
	isAdmin, err := h.cp.IsPlatformAdmin(r.Context(), payload.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return payload, false
	}
	if !isAdmin {
		logSecurityEvent(r, "platform_automation_denied", "identity", payload.ID)
		fail(w, http.StatusForbidden, "Platform admin privileges required.")
		return payload, false
	}
	return payload, true
}

// Get handles GET /api/platform/automation.
func (h *AutomationPlatformOps) Get(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requirePlatformAdmin(w, r); !ok {
		return
	}
	st, err := h.store.State(r.Context())
	if err != nil {
		slog.Error("load platform automation settings failed", "request_id", middleware.RequestIDFromContext(r.Context()), "err", err)
		fail(w, http.StatusInternalServerError, "Failed to load automation settings.")
		return
	}
	writeJSON(w, http.StatusOK, automationPlatformResponse{
		Success: true, PlatformState: st, KVConfigured: h.mirror.Configured(), Message: msgAutomationScope,
	})
}

// Update handles PUT /api/platform/automation with {"enabled": bool}. The
// database is written first (source of truth), then the KV mirror. If the
// mirror fails the response is 502 with the saved state: PUT again to re-sync.
func (h *AutomationPlatformOps) Update(w http.ResponseWriter, r *http.Request) {
	payload, ok := h.requirePlatformAdmin(w, r)
	if !ok {
		return
	}
	enabled, ok := decodeEnabledBody(w, r)
	if !ok {
		return
	}
	if enabled && h.requireKV && !h.mirror.Configured() {
		fail(w, http.StatusConflict, "Cannot enable automation: the Cloudflare KV mirror is not configured (AUTOMATION_KV_NAMESPACE_ID, AUTOMATION_KV_API_TOKEN, CLOUDFLARE_ACCOUNT_ID), so the scheduler would never start.")
		return
	}
	if err := h.store.Set(r.Context(), enabled, payload.Email); err != nil {
		slog.Error("save platform automation settings failed", "request_id", middleware.RequestIDFromContext(r.Context()), "err", err)
		fail(w, http.StatusInternalServerError, "Failed to save automation settings.")
		return
	}
	logSecurityEvent(r, "platform_automation_changed", "identity", payload.ID, "enabled", enabled)

	resp := automationPlatformResponse{Success: true, KVConfigured: h.mirror.Configured(), Message: msgAutomationScope}
	status := http.StatusOK
	if resp.KVConfigured {
		synced := true
		if err := h.mirror.Set(r.Context(), enabled); err != nil {
			slog.Error("sync platform automation switch to cloudflare kv failed",
				"request_id", middleware.RequestIDFromContext(r.Context()), "enabled", enabled, "err", err)
			synced = false
			status = http.StatusBadGateway
			resp.Success = false
			resp.Message = "Saved, but syncing to Cloudflare failed. The scheduler may not reflect this yet; retry the request."
		}
		resp.KVSynced = &synced
	}
	st, err := h.store.State(r.Context())
	if err != nil {
		slog.Error("load platform automation settings failed", "request_id", middleware.RequestIDFromContext(r.Context()), "err", err)
		fail(w, http.StatusInternalServerError, "Saved, but failed to reload automation settings.")
		return
	}
	resp.PlatformState = st
	writeJSON(w, status, resp)
}
