package controllers

import (
	"encoding/json"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/dashboard"
	"stonesuite-backend/middleware"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/workflow"
)

// DashboardOps groups the Dashboard Widgets handlers: the RBAC/tenant-config
// filtered widget catalog, per-user visibility/layout preferences, and the
// tenant-wide widget on/off admin config.
type DashboardOps struct{}

// NewDashboardOps constructs the handler group.
func NewDashboardOps() *DashboardOps { return &DashboardOps{} }

// resolveForCaller loads everything dashboard.Resolve needs for the current
// caller and returns the filtered, preference-overlaid widget list. Shared by
// ListWidgets, SavePreferences and ResetPreferences so all three return the
// same canonical shape. On failure it writes the response itself and returns
// ok=false.
func (h *DashboardOps) resolveForCaller(w http.ResponseWriter, r *http.Request) ([]dashboard.ResolvedWidget, bool) {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return nil, false
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return nil, false
	}
	grants, err := authz.EffectiveGrants(r.Context(), pool, payload.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load permissions.")
		return nil, false
	}
	overrides, err := dashboard.WidgetConfigOverrides(r.Context(), pool)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load dashboard configuration.")
		return nil, false
	}
	var prefs map[string]dashboard.UserPref
	if userID, idErr := workflow.UserIDByIdentity(r.Context(), pool, payload.ID); idErr == nil && userID != "" {
		prefs, err = dashboard.UserPrefs(r.Context(), pool, userID)
		if err != nil {
			fail(w, http.StatusInternalServerError, "Failed to load dashboard preferences.")
			return nil, false
		}
	}
	return dashboard.Resolve(dashboard.Catalog(), grants, overrides, prefs), true
}

// ListWidgets GET /api/tenant/dashboard/widgets
func (h *DashboardOps) ListWidgets(w http.ResponseWriter, r *http.Request) {
	widgets, ok := h.resolveForCaller(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "widgets": widgets})
}

// SavePreferences PUT /api/tenant/dashboard/widgets/preferences
// body: {"widgets":[{"widgetKey":"sales.quotes","visible":true,"position":0,"width":6,"height":4}]}
func (h *DashboardOps) SavePreferences(w http.ResponseWriter, r *http.Request) {
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
		Widgets []dashboard.PrefInput `json:"widgets"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	grants, err := authz.EffectiveGrants(r.Context(), pool, payload.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load permissions.")
		return
	}
	prefs, err := dashboard.ValidatePrefs(req.Widgets, grants)
	if err != nil {
		if key, isForbidden := dashboard.IsForbiddenWidgetError(err); isForbidden {
			logSecurityEvent(r, "dashboard_pref_denied", "identity", payload.ID, "widget", key)
		}
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	userID, err := workflow.UserIDByIdentity(r.Context(), pool, payload.ID)
	if err != nil || userID == "" {
		fail(w, http.StatusInternalServerError, "Failed to resolve user record.")
		return
	}
	if err := dashboard.SaveUserPrefs(r.Context(), pool, userID, prefs); err != nil {
		fail(w, http.StatusInternalServerError, "Failed to save dashboard preferences.")
		return
	}
	widgets, ok := h.resolveForCaller(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "widgets": widgets})
}

// ResetPreferences DELETE /api/tenant/dashboard/widgets/preferences
func (h *DashboardOps) ResetPreferences(w http.ResponseWriter, r *http.Request) {
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
	userID, err := workflow.UserIDByIdentity(r.Context(), pool, payload.ID)
	if err != nil || userID == "" {
		fail(w, http.StatusInternalServerError, "Failed to resolve user record.")
		return
	}
	if err := dashboard.ClearUserPrefs(r.Context(), pool, userID); err != nil {
		fail(w, http.StatusInternalServerError, "Failed to reset dashboard preferences.")
		return
	}
	widgets, ok := h.resolveForCaller(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "widgets": widgets})
}

// requireDashboardConfig checks the caller may configure workspace-wide
// dashboard settings, mirroring CRMAdminOps.requireConfig
// (controllers/crm_admin.go). Returns the tenant pool on success; on failure
// it writes the response itself.
func (h *DashboardOps) requireDashboardConfig(w http.ResponseWriter, r *http.Request) (*pgxpool.Pool, bool) {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return nil, false
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return nil, false
	}
	decision, err := authz.Check(r.Context(), pool, payload.ID, authz.ResourceWorkflowConfig, authz.ActionConfigure)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, false
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", payload.ID, "resource", "dashboard_config", "action", string(authz.ActionConfigure))
		fail(w, http.StatusForbidden, "You do not have permission to configure this workspace.")
		return nil, false
	}
	return pool, true
}

// configEntries builds the full GET /dashboard/config response: every
// catalog widget plus its tenant-effective enabled flag (defaulting true).
func configEntries(overrides map[string]bool) []dashboard.ConfigEntry {
	catalog := dashboard.Catalog()
	out := make([]dashboard.ConfigEntry, 0, len(catalog))
	for _, wgt := range catalog {
		enabled := true
		if v, ok := overrides[wgt.Key]; ok {
			enabled = v
		}
		out = append(out, dashboard.ConfigEntry{
			Key: wgt.Key, Title: wgt.Title, Category: string(wgt.Category), Enabled: enabled,
		})
	}
	return out
}

// GetConfig GET /api/tenant/dashboard/config
func (h *DashboardOps) GetConfig(w http.ResponseWriter, r *http.Request) {
	pool, ok := h.requireDashboardConfig(w, r)
	if !ok {
		return
	}
	overrides, err := dashboard.WidgetConfigOverrides(r.Context(), pool)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load dashboard configuration.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "widgets": configEntries(overrides)})
}

// SetConfig PUT /api/tenant/dashboard/config
// body: {"widgets":[{"widgetKey":"sales.payments","enabled":false}]}
func (h *DashboardOps) SetConfig(w http.ResponseWriter, r *http.Request) {
	pool, ok := h.requireDashboardConfig(w, r)
	if !ok {
		return
	}
	var req struct {
		Widgets []dashboard.ConfigInput `json:"widgets"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	if err := dashboard.ValidateConfigUpdates(req.Widgets); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := dashboard.SetWidgetConfig(r.Context(), pool, req.Widgets); err != nil {
		fail(w, http.StatusInternalServerError, "Failed to update dashboard configuration.")
		return
	}
	overrides, err := dashboard.WidgetConfigOverrides(r.Context(), pool)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load dashboard configuration.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "widgets": configEntries(overrides)})
}
