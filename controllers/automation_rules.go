package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"stonesuite-backend/authz"
	"stonesuite-backend/middleware"
	"stonesuite-backend/tenancy"
)

// maxAutomationRuleBodyBytes caps the toggle request body.
const maxAutomationRuleBodyBytes = 4 << 10

// automationRuleEnv resolves the per-request tenant dependencies.
type automationRuleEnv struct {
	store automationRuleStore
	check func(ctx context.Context, identityID string, action authz.Action) (authz.Decision, error)
}

// AutomationRuleOps serves GET/PUT /api/tenant/automation/rules.
type AutomationRuleOps struct {
	resolve func(r *http.Request) (automationRuleEnv, error)
}

// NewAutomationRuleOps builds the handler set backed by the resolved tenant pool.
func NewAutomationRuleOps() *AutomationRuleOps {
	return &AutomationRuleOps{resolve: func(r *http.Request) (automationRuleEnv, error) {
		pool, err := tenancy.PoolFromContext(r.Context())
		if err != nil {
			return automationRuleEnv{}, err
		}
		return automationRuleEnv{
			store: pgAutomationRuleStore{pool: pool},
			check: func(ctx context.Context, identityID string, action authz.Action) (authz.Decision, error) {
				return authz.Check(ctx, pool, identityID, authz.ResourceAutomation, action)
			},
		}, nil
	}}
}

func (h *AutomationRuleOps) auth(w http.ResponseWriter, r *http.Request, action authz.Action) (automationRuleEnv, string, bool) {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return automationRuleEnv{}, "", false
	}
	env, err := h.resolve(r)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return automationRuleEnv{}, "", false
	}
	decision, err := env.check(r.Context(), payload.ID, action)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return automationRuleEnv{}, "", false
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", payload.ID, "resource", string(authz.ResourceAutomation), "action", string(action))
		fail(w, http.StatusForbidden, "You do not have permission to "+string(action)+" automation rules.")
		return automationRuleEnv{}, "", false
	}
	return env, payload.ID, true
}

// List handles GET /api/tenant/automation/rules.
func (h *AutomationRuleOps) List(w http.ResponseWriter, r *http.Request) {
	env, _, ok := h.auth(w, r, authz.ActionRead)
	if !ok {
		return
	}
	rules, err := env.store.List(r.Context())
	if err != nil {
		slog.Error("list automation rules", "error", err)
		fail(w, http.StatusInternalServerError, "Failed to load automation rules.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "rules": rules})
}

// SetEnabled handles PUT /api/tenant/automation/rules/{key}.
func (h *AutomationRuleOps) SetEnabled(w http.ResponseWriter, r *http.Request) {
	env, identityID, ok := h.auth(w, r, authz.ActionConfigure)
	if !ok {
		return
	}
	var body struct {
		Enabled *bool `json:"enabled"`
	}
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxAutomationRuleBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil || body.Enabled == nil {
		fail(w, http.StatusBadRequest, "Body must be {\"enabled\": true|false}.")
		return
	}
	key := r.PathValue("key")
	rule, err := env.store.SetEnabled(r.Context(), key, *body.Enabled)
	if errors.Is(err, ErrAutomationRuleNotFound) {
		fail(w, http.StatusNotFound, "Automation rule not found.")
		return
	}
	if err != nil {
		slog.Error("set automation rule", "rule", key, "error", err)
		fail(w, http.StatusInternalServerError, "Failed to update automation rule.")
		return
	}
	tenantID := ""
	if t, terr := tenancy.TenantFromContext(r.Context()); terr == nil {
		tenantID = t.ID
	}
	slog.Info("automation_rule_changed",
		"tenant_id", tenantID, "rule_key", key, "identity", identityID, "enabled", rule.Enabled)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "rule": rule})
}
