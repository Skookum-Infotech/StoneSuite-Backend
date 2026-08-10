package controllers

import (
	"encoding/json"
	"errors"
	"net/http"

	"stonesuite-backend/authz"
	"stonesuite-backend/dashboardui"
	"stonesuite-backend/middleware"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/workflow"
)

// DashboardUIOps groups the tenant-scoped dashboard-widget allocation
// handlers -- which roles' members may see which dashboard widgets. All
// routes run behind RequireAuth + the tenancy Resolver.
type DashboardUIOps struct{}

// NewDashboardUIOps constructs the handler group.
func NewDashboardUIOps() *DashboardUIOps { return &DashboardUIOps{} }

// authorize checks the caller holds dashboard_widget:action in the resolved
// tenant. On failure it writes the response and returns false.
func (h *DashboardUIOps) authorize(w http.ResponseWriter, r *http.Request, action authz.Action) bool {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return false
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return false
	}
	decision, err := authz.Check(r.Context(), pool, payload.ID, authz.ResourceDashboardWidget, action)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return false
	}
	if !decision.Allowed {
		fail(w, http.StatusForbidden, "You do not have permission to "+string(action)+" dashboard widgets.")
		return false
	}
	return true
}

// Me returns the widgets the caller may currently see -- every widget
// allocated to their assigned role(s), narrowed to just the active role
// when one is set, or the full catalog if any of their grants is the
// wildcard (super admin) grant. No dashboard_widget permission is required:
// every authenticated user may see their own dashboard.
// GET /api/tenant/dashboard/widgets/me
func (h *DashboardUIOps) Me(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		fail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
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
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to resolve tenant user.")
		return
	}
	widgetIDs, err := dashboardui.GetForIdentity(r.Context(), pool, payload.ID, userID, payload.ActiveRoleID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load dashboard widgets.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "widgetIds": widgetIDs})
}

// RoleAllocations dispatches GET (list, all roles) and PUT (batch save) on
// /api/tenant/dashboard/widgets/roles.
func (h *DashboardUIOps) RoleAllocations(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		h.listRoleAllocations(w, r)
	case http.MethodPut:
		h.setRoleAllocations(w, r)
	default:
		fail(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

// listRoleAllocations GET /api/tenant/dashboard/widgets/roles
// Returns every role's widget allocation (unconfigured roles fall back to
// the catalog defaults), for the admin allocation page.
func (h *DashboardUIOps) listRoleAllocations(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r, authz.ActionRead) {
		return
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return
	}
	roles, err := authz.ListRoles(r.Context(), pool)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load roles.")
		return
	}
	roleIDs := make([]string, len(roles))
	for i, role := range roles {
		roleIDs[i] = role.ID
	}
	allocations, err := dashboardui.GetForRoles(r.Context(), pool, roleIDs)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load dashboard widget allocations.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "allocations": allocations})
}

// setRoleAllocations PUT /api/tenant/dashboard/widgets/roles
// body {"allocations":[{"roleId":"...","widgetIds":["..."]}]}
// Validates and writes every given role's allocation atomically -- either
// every role in the batch is saved, or none are.
func (h *DashboardUIOps) setRoleAllocations(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r, authz.ActionConfigure) {
		return
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return
	}
	var req struct {
		Allocations []dashboardui.RoleAllocation `json:"allocations"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	if err := dashboardui.SetForRoles(r.Context(), pool, req.Allocations); err != nil {
		var invalidID *dashboardui.ErrInvalidWidgetID
		switch {
		case errors.Is(err, dashboardui.ErrRoleNotFound):
			fail(w, http.StatusNotFound, "One or more roles were not found.")
		case errors.Is(err, dashboardui.ErrRoleLocked):
			fail(w, http.StatusBadRequest, "A super admin role always has every widget and cannot be configured.")
		case errors.As(err, &invalidID):
			fail(w, http.StatusBadRequest, invalidID.Error())
		default:
			fail(w, http.StatusInternalServerError, "Failed to save dashboard widget allocations.")
		}
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "allocations": req.Allocations})
}
