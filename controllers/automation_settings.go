package controllers

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"

	"stonesuite-backend/authz"
	"stonesuite-backend/middleware"
	"stonesuite-backend/tenancy"
)

// maxAutomationSettingsBodyBytes caps the settings request body.
const maxAutomationSettingsBodyBytes = 4 << 10

// platformSwitchReader reports the platform-wide automation switch (automation.PlatformStore).
type platformSwitchReader interface {
	Enabled(ctx context.Context) (bool, error)
}

// automationSettingsEnv resolves the per-request tenant dependencies.
type automationSettingsEnv struct {
	store automationSettingsStore
	check func(ctx context.Context, identityID string, action authz.Action) (authz.Decision, error)
}

// AutomationSettingsOps serves GET/PUT /api/tenant/automation/settings.
type AutomationSettingsOps struct {
	resolve  func(r *http.Request) (automationSettingsEnv, error)
	platform platformSwitchReader
}

// NewAutomationSettingsOps builds the handler set; platform is the control-plane switch.
func NewAutomationSettingsOps(platform platformSwitchReader) *AutomationSettingsOps {
	return &AutomationSettingsOps{platform: platform, resolve: func(r *http.Request) (automationSettingsEnv, error) {
		pool, err := tenancy.PoolFromContext(r.Context())
		if err != nil {
			return automationSettingsEnv{}, err
		}
		return automationSettingsEnv{
			store: pgAutomationSettingsStore{pool: pool},
			check: func(ctx context.Context, identityID string, action authz.Action) (authz.Decision, error) {
				return authz.Check(ctx, pool, identityID, authz.ResourceAutomation, action)
			},
		}, nil
	}}
}

func (h *AutomationSettingsOps) auth(w http.ResponseWriter, r *http.Request, action authz.Action) (automationSettingsEnv, string, bool) {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return automationSettingsEnv{}, "", false
	}
	env, err := h.resolve(r)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return automationSettingsEnv{}, "", false
	}
	decision, err := env.check(r.Context(), payload.ID, action)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return automationSettingsEnv{}, "", false
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", payload.ID, "resource", string(authz.ResourceAutomation), "action", string(action))
		fail(w, http.StatusForbidden, "You do not have permission to "+string(action)+" automation settings.")
		return automationSettingsEnv{}, "", false
	}
	return env, payload.ID, true
}

// withPlatform fills PlatformEnabled; a control-plane failure leaves it nil ("unknown").
func (h *AutomationSettingsOps) withPlatform(ctx context.Context, s AutomationSettings) AutomationSettings {
	if h.platform == nil {
		return s
	}
	on, err := h.platform.Enabled(ctx)
	if err != nil {
		slog.Error("read automation platform switch", "error", err)
		return s
	}
	s.PlatformEnabled = &on
	return s
}

// Get handles GET /api/tenant/automation/settings.
func (h *AutomationSettingsOps) Get(w http.ResponseWriter, r *http.Request) {
	env, _, ok := h.auth(w, r, authz.ActionRead)
	if !ok {
		return
	}
	s, err := env.store.Get(r.Context())
	if err != nil {
		slog.Error("get automation settings", "error", err)
		fail(w, http.StatusInternalServerError, "Failed to load automation settings.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "settings": h.withPlatform(r.Context(), s)})
}

// Put handles PUT /api/tenant/automation/settings.
func (h *AutomationSettingsOps) Put(w http.ResponseWriter, r *http.Request) {
	env, identityID, ok := h.auth(w, r, authz.ActionConfigure)
	if !ok {
		return
	}
	var body struct {
		KillSwitch *bool `json:"killSwitch"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAutomationSettingsBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || body.KillSwitch == nil {
		fail(w, http.StatusBadRequest, "Body must be {\"killSwitch\": true|false}.")
		return
	}
	s, err := env.store.SetKillSwitch(r.Context(), *body.KillSwitch)
	if err != nil {
		slog.Error("set automation kill switch", "error", err)
		fail(w, http.StatusInternalServerError, "Failed to update automation settings.")
		return
	}
	tenantID := ""
	if t, terr := tenancy.TenantFromContext(r.Context()); terr == nil {
		tenantID = t.ID
	}
	slog.Info("automation_settings_changed",
		"tenant_id", tenantID, "identity", identityID, "kill_switch", s.KillSwitch)
	logSecurityEvent(r, "automation_kill_switch_changed",
		"identity", identityID, "kill_switch", s.KillSwitch)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "settings": h.withPlatform(r.Context(), s)})
}
