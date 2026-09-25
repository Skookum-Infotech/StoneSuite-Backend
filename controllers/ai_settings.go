package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"stonesuite-backend/aisettings"
	"stonesuite-backend/authz"
	"stonesuite-backend/middleware"
	"stonesuite-backend/services"
	"stonesuite-backend/tenancy"
)

// codeAssistantDisabled marks the 403 returned when the AI assistant is
// turned off, at either the platform master switch or this tenant's own
// switch — distinct from codeAssistantBusy (a capacity limit) and from a
// permission_denied (a role/scope decision): this is neither, it's a global
// availability flag prepareAsk, Reindex, and Warm all gate on the same way.
const codeAssistantDisabled = "assistant_disabled"

const (
	disabledReasonPlatform = "platform"
	disabledReasonTenant   = "tenant"

	msgDisabledByPlatform = "The StoneSuite Assistant has been turned off by the platform administrator."
	msgDisabledByTenant   = "The StoneSuite Assistant is turned off for your organization."

	// maxSettingsBodyBytes caps the {"enabled": bool} PUT bodies.
	maxSettingsBodyBytes = 1 << 10

	msgEnabledRequired = "Field 'enabled' is required."
)

// assistantDisabledPayload returns the message and reason ("platform" or
// "tenant") for a disabled status; the platform switch wins when both are off.
func assistantDisabledPayload(status aisettings.Status) (message, reason string) {
	if !status.PlatformEnabled {
		return msgDisabledByPlatform, disabledReasonPlatform
	}
	return msgDisabledByTenant, disabledReasonTenant
}

// writeAssistantDisabled sends the disabled 403 with the stable
// assistant_disabled code every AI entry point agrees on, plus a reason
// telling the client which switch is off.
func writeAssistantDisabled(w http.ResponseWriter, status aisettings.Status) {
	message, reason := assistantDisabledPayload(status)
	writeJSON(w, http.StatusForbidden, map[string]any{
		"success": false,
		"code":    codeAssistantDisabled,
		"reason":  reason,
		"message": message,
	})
}

// decodeEnabledBody reads a {"enabled": bool} body capped at
// maxSettingsBodyBytes. A missing field, malformed JSON, or a wrong type
// writes a 400 and returns ok=false.
func decodeEnabledBody(w http.ResponseWriter, r *http.Request) (enabled, ok bool) {
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxSettingsBodyBytes)).Decode(&body); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return false, false
	}
	if body.Enabled == nil {
		fail(w, http.StatusBadRequest, msgEnabledRequired)
		return false, false
	}
	return *body.Enabled, true
}

// PlatformToggleListener is notified after a successful write to the
// platform-wide AI switch, so a later step (stopping/starting the shared
// Ollama box, pausing the reindex queue) can react without this handler
// knowing about either. Defined at the point of use: UpdatePlatformSettings
// calls it, nil-safe, only if one was wired via WithPlatformToggleListener.
type PlatformToggleListener interface {
	OnPlatformToggle(ctx context.Context, enabled bool)
}

// WithPlatformToggleListener wires a hook to run after every successful
// platform AI-toggle write and returns the receiver for chaining at
// construction in main.go. Unset by default — a later step implements the
// listener; until then a toggle changes only the stored value and gating.
func (h *AIOps) WithPlatformToggleListener(l PlatformToggleListener) *AIOps {
	h.toggleListener = l
	return h
}

// tenantCatchUpNotifier lets UpdateSettings nudge that tenant's RAG index
// maintenance loop (rag_indexing.go's runTenantMaintenance) to catch up
// immediately — revive errored jobs, then reconcile — when AI is switched
// back on for this tenant, instead of waiting out ragMaintenanceInterval.
// Defined at the point of use; satisfied by *main.IndexCoordinator, wired via
// WithCatchUpNotifier.
type tenantCatchUpNotifier interface {
	CatchUp(tenantID string)
}

// WithCatchUpNotifier wires the RAG index catch-up hook UpdateSettings calls
// after a tenant switches AI back on, and returns the receiver for chaining
// at construction in main.go. Unset by default (nil-safe): UpdateSettings
// simply skips the nudge, same as before this existed.
func (h *AIOps) WithCatchUpNotifier(n tenantCatchUpNotifier) *AIOps {
	h.catchUpNotifier = n
	return h
}

// Status handles GET /api/tenant/ai/status. Any authenticated tenant user
// may read it — the toggle itself is gated on company_profile:configure, but
// knowing whether the assistant is on is not sensitive.
func (h *AIOps) Status(w http.ResponseWriter, r *http.Request) {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return
	}
	tenant, err := tenancy.TenantFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant not resolved.")
		return
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return
	}

	status, err := h.aiSettings.Status(r.Context(), h.cpPool, pool, tenant.ID)
	if err != nil {
		slog.Error("ai settings status check failed", "request_id", middleware.RequestIDFromContext(r.Context()), "tenant_id", tenant.ID, "err", err)
		fail(w, http.StatusInternalServerError, "Failed to load assistant status.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "status": status})
}

// UpdateSettings handles PUT /api/tenant/ai/settings, gated on the same
// company_profile:configure permission as PUT /api/tenant/company-profile —
// both are tenant-wide Configuration toggles a workspace admin controls.
func (h *AIOps) UpdateSettings(w http.ResponseWriter, r *http.Request) {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return
	}
	tenant, err := tenancy.TenantFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant not resolved.")
		return
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return
	}

	decision, err := authz.Check(r.Context(), pool, payload.ID, authz.ResourceCompanyProfile, authz.ActionConfigure)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", payload.ID, "resource", string(authz.ResourceCompanyProfile), "action", string(authz.ActionConfigure))
		fail(w, http.StatusForbidden, "You do not have permission to change the assistant settings.")
		return
	}

	enabled, ok := decodeEnabledBody(w, r)
	if !ok {
		return
	}

	if err := aisettings.SetTenantEnabled(r.Context(), pool, enabled, payload.Email); err != nil {
		slog.Error("save tenant ai settings failed", "request_id", middleware.RequestIDFromContext(r.Context()), "tenant_id", tenant.ID, "err", err)
		fail(w, http.StatusInternalServerError, "Failed to save assistant settings.")
		return
	}
	h.aiSettings.InvalidateTenant(tenant.ID)
	if enabled && h.catchUpNotifier != nil {
		h.catchUpNotifier.CatchUp(tenant.ID)
	}

	logSecurityEvent(r, "ai_settings_changed", "identity", payload.ID, "tenant_id", tenant.ID, "enabled", enabled)

	status, err := h.aiSettings.Status(r.Context(), h.cpPool, pool, tenant.ID)
	if err != nil {
		slog.Error("ai settings status check failed", "request_id", middleware.RequestIDFromContext(r.Context()), "tenant_id", tenant.ID, "err", err)
		fail(w, http.StatusInternalServerError, "Failed to load assistant status.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "status": status})
}

// platformAISettingsView is the row shape GET/PUT /api/platform/ai/settings
// both return (alongside the standard success:true envelope): the platform
// switch, who/when it last changed, and when the shared help corpus last
// finished syncing.
type platformAISettingsView struct {
	Enabled            bool       `json:"enabled"`
	UpdatedAt          *time.Time `json:"updatedAt"`
	UpdatedBy          *string    `json:"updatedBy"`
	HelpCorpusSyncedAt *time.Time `json:"helpCorpusSyncedAt"`
	// OllamaState summarizes the shared Ollama Machine's state
	// ("started"/"stopped"/"mixed"/"unknown") — see AIOps.WithOllamaStatus.
	// Always "unknown" when no Fly lifecycle is configured (local dev).
	OllamaState string `json:"ollamaState"`
	// LeaseHolders lists other environments' still-unexpired holders of the
	// shared Ollama lease, best-effort — always empty when no lease is
	// configured.
	LeaseHolders []string `json:"leaseHolders"`
}

// asResponse wraps view in the standard success:true envelope every other
// handler in this package returns on a 200.
func (view platformAISettingsView) asResponse() map[string]any {
	return map[string]any{
		"success":            true,
		"enabled":            view.Enabled,
		"updatedAt":          view.UpdatedAt,
		"updatedBy":          view.UpdatedBy,
		"helpCorpusSyncedAt": view.HelpCorpusSyncedAt,
		"ollamaState":        view.OllamaState,
		"leaseHolders":       view.LeaseHolders,
	}
}

// requirePlatformAdmin resolves the caller and checks platform-admin status,
// the same gate ReindexHelp uses. On failure it writes the response and
// returns ok=false.
func (h *AIOps) requirePlatformAdmin(w http.ResponseWriter, r *http.Request) (middleware.UserContextPayload, bool) {
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
		logSecurityEvent(r, "platform_ai_settings_denied", "identity", payload.ID)
		fail(w, http.StatusForbidden, "Platform admin privileges required.")
		return payload, false
	}
	return payload, true
}

// GetPlatformSettings handles GET /api/platform/ai/settings. Platform-admin
// only, gated exactly as ReindexHelp is (RequireAuth only, no tenant chain —
// this switch is per environment, not per tenant).
func (h *AIOps) GetPlatformSettings(w http.ResponseWriter, r *http.Request) {
	if _, ok := h.requirePlatformAdmin(w, r); !ok {
		return
	}

	view, err := h.loadPlatformSettingsView(r.Context())
	if err != nil {
		slog.Error("load platform ai settings failed", "request_id", middleware.RequestIDFromContext(r.Context()), "err", err)
		fail(w, http.StatusInternalServerError, "Failed to load assistant settings.")
		return
	}
	writeJSON(w, http.StatusOK, view.asResponse())
}

// UpdatePlatformSettings handles PUT /api/platform/ai/settings. Writes the
// platform switch, invalidates the cache, notifies the toggle listener (if
// one is wired), logs a security event, and returns the same shape GET does.
func (h *AIOps) UpdatePlatformSettings(w http.ResponseWriter, r *http.Request) {
	payload, ok := h.requirePlatformAdmin(w, r)
	if !ok {
		return
	}

	enabled, ok := decodeEnabledBody(w, r)
	if !ok {
		return
	}

	if err := aisettings.SetPlatformEnabled(r.Context(), h.cpPool, enabled, payload.Email); err != nil {
		slog.Error("save platform ai settings failed", "request_id", middleware.RequestIDFromContext(r.Context()), "err", err)
		fail(w, http.StatusInternalServerError, "Failed to save assistant settings.")
		return
	}
	h.aiSettings.InvalidatePlatform()
	if h.toggleListener != nil {
		h.toggleListener.OnPlatformToggle(r.Context(), enabled)
	}

	logSecurityEvent(r, "platform_ai_settings_changed", "identity", payload.ID, "enabled", enabled)

	view, err := h.loadPlatformSettingsView(r.Context())
	if err != nil {
		slog.Error("load platform ai settings failed", "request_id", middleware.RequestIDFromContext(r.Context()), "err", err)
		fail(w, http.StatusInternalServerError, "Failed to load assistant settings.")
		return
	}
	writeJSON(w, http.StatusOK, view.asResponse())
}

// loadPlatformSettingsView reads the platform switch row plus the shared
// help corpus's last sync time, defaulting to enabled with no history when
// platform_ai_settings has no row yet (pre-migration boot). OllamaState and
// LeaseHolders are read best-effort from h.ollamaState/h.leaseHolders (both
// nil-safe): "unknown"/empty when no Fly lifecycle is configured, and never
// a reason to fail this read.
func (h *AIOps) loadPlatformSettingsView(ctx context.Context) (platformAISettingsView, error) {
	view := platformAISettingsView{Enabled: true, OllamaState: services.OllamaStateUnknown}
	err := h.cpPool.QueryRow(ctx,
		`SELECT enabled, updated_by, updated_at FROM platform_ai_settings WHERE id = 1`,
	).Scan(&view.Enabled, &view.UpdatedBy, &view.UpdatedAt)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return platformAISettingsView{}, err
	}

	if err := h.cpPool.QueryRow(ctx,
		`SELECT synced_at FROM cp_help_corpus_state WHERE id = 1`,
	).Scan(&view.HelpCorpusSyncedAt); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return platformAISettingsView{}, err
	}

	if h.ollamaState != nil {
		if state, err := h.ollamaState.State(ctx); err != nil {
			slog.Warn("ollama state check failed", "err", err)
		} else {
			view.OllamaState = state
		}
	}
	if h.leaseHolders != nil {
		view.LeaseHolders = h.leaseHolders.OtherHolders(ctx)
	}
	return view, nil
}
