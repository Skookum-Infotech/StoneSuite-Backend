package controllers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"stonesuite-backend/authz"
	"stonesuite-backend/config"
	"stonesuite-backend/middleware"
	"stonesuite-backend/models"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/userstore"
)

// RBACOps groups the tenant-scoped role/permission management handlers. All
// routes run behind RequireAuth + the tenancy Resolver, so the tenant DB pool
// and caller identity are available on the request context.
type RBACOps struct{}

// NewRBACOps constructs the handler group.
func NewRBACOps() *RBACOps { return &RBACOps{} }

// authorize checks the caller holds resource:action in the resolved tenant.
// On failure it writes the response and returns false.
func (h *RBACOps) authorize(w http.ResponseWriter, r *http.Request, resource authz.Resource, action authz.Action) bool {
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
	decision, err := authz.Check(r.Context(), pool, payload.ID, resource, action)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return false
	}
	if !decision.Allowed {
		fail(w, http.StatusForbidden, "You do not have permission to "+string(action)+" "+string(resource)+".")
		return false
	}
	return true
}

// Catalog returns the full permission catalog. GET /api/tenant/permissions/catalog
func (h *RBACOps) Catalog(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		fail(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	if !h.authorize(w, r, authz.ResourceRole, authz.ActionRead) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":     true,
		"permissions": authz.Catalog(),
		"scopes":      []authz.Scope{authz.ScopeAll, authz.ScopeOwn},
	})
}

// Roles dispatches GET (list) and POST (create) on /api/tenant/roles.
func (h *RBACOps) Roles(w http.ResponseWriter, r *http.Request) {
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return
	}
	switch r.Method {
	case http.MethodGet:
		if !h.authorize(w, r, authz.ResourceRole, authz.ActionRead) {
			return
		}
		roles, err := authz.ListRoles(r.Context(), pool)
		if err != nil {
			fail(w, http.StatusInternalServerError, "Failed to list roles.")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "roles": roles})

	case http.MethodPost:
		if !h.authorize(w, r, authz.ResourceRole, authz.ActionCreate) {
			return
		}
		var req roleRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			fail(w, http.StatusBadRequest, "Invalid request body.")
			return
		}
		req.Key = strings.ToLower(strings.TrimSpace(req.Key))
		if req.Key == "" || strings.TrimSpace(req.Name) == "" {
			fail(w, http.StatusBadRequest, "key and name are required.")
			return
		}
		if req.Key == authz.RoleSuperAdmin {
			fail(w, http.StatusBadRequest, "The super_admin role is reserved.")
			return
		}
		id, err := authz.CreateRole(r.Context(), pool, req.Key, req.Name, req.Description, req.Permissions)
		if err != nil {
			if isValidationErr(err) {
				fail(w, http.StatusBadRequest, err.Error())
				return
			}
			fail(w, http.StatusInternalServerError, "Failed to create role (key may be taken).")
			return
		}
		writeJSON(w, http.StatusCreated, map[string]any{"success": true, "id": id})

	default:
		fail(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

// Role dispatches GET/PUT/DELETE on /api/tenant/roles/{id}.
func (h *RBACOps) Role(w http.ResponseWriter, r *http.Request) {
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/tenant/roles/")
	if id == "" || strings.Contains(id, "/") {
		fail(w, http.StatusBadRequest, "Expected /api/tenant/roles/{id}.")
		return
	}

	switch r.Method {
	case http.MethodGet:
		if !h.authorize(w, r, authz.ResourceRole, authz.ActionRead) {
			return
		}
		role, err := authz.GetRole(r.Context(), pool, id)
		if errors.Is(err, authz.ErrRoleNotFound) {
			fail(w, http.StatusNotFound, "Role not found.")
			return
		}
		if err != nil {
			fail(w, http.StatusInternalServerError, "Failed to load role.")
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "role": role})

	case http.MethodPut:
		if !h.authorize(w, r, authz.ResourceRole, authz.ActionUpdate) {
			return
		}
		existing, err := authz.GetRole(r.Context(), pool, id)
		if errors.Is(err, authz.ErrRoleNotFound) {
			fail(w, http.StatusNotFound, "Role not found.")
			return
		}
		if err != nil {
			fail(w, http.StatusInternalServerError, "Failed to load role.")
			return
		}
		if existing.IsSystem {
			fail(w, http.StatusForbidden, "System roles cannot be modified.")
			return
		}
		var req roleRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			fail(w, http.StatusBadRequest, "Invalid request body.")
			return
		}
		if strings.TrimSpace(req.Name) == "" {
			fail(w, http.StatusBadRequest, "name is required.")
			return
		}
		if err := authz.UpdateRole(r.Context(), pool, id, req.Name, req.Description, req.Permissions); err != nil {
			if isValidationErr(err) {
				fail(w, http.StatusBadRequest, err.Error())
				return
			}
			fail(w, http.StatusInternalServerError, "Failed to update role.")
			return
		}
		writeJSON(w, http.StatusOK, models.APIResponse{Success: true, Message: "Role updated."})

	case http.MethodDelete:
		if !h.authorize(w, r, authz.ResourceRole, authz.ActionDelete) {
			return
		}
		if err := authz.DeleteRole(r.Context(), pool, id); err != nil {
			if errors.Is(err, authz.ErrRoleNotFound) {
				fail(w, http.StatusNotFound, "Role not found or is a protected system role.")
				return
			}
			fail(w, http.StatusInternalServerError, "Failed to delete role.")
			return
		}
		writeJSON(w, http.StatusOK, models.APIResponse{Success: true, Message: "Role deleted."})

	default:
		fail(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

// UserRoles dispatches role assignment on /api/tenant/users/{userId}/roles
// (POST assign {roleId}) and /api/tenant/users/{userId}/roles/{roleId} (DELETE).
func (h *RBACOps) UserRoles(w http.ResponseWriter, r *http.Request) {
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/tenant/users/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	// Expect: {userId}/roles            (POST)
	//      or: {userId}/roles/{roleId}  (DELETE)
	if len(parts) < 2 || parts[1] != "roles" || parts[0] == "" {
		fail(w, http.StatusBadRequest, "Expected /api/tenant/users/{userId}/roles[/{roleId}].")
		return
	}
	userID := parts[0]

	if !h.authorize(w, r, authz.ResourceRole, authz.ActionUpdate) {
		return
	}

	switch r.Method {
	case http.MethodPost:
		var req struct {
			RoleID string `json:"roleId"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.RoleID == "" {
			fail(w, http.StatusBadRequest, "roleId is required.")
			return
		}
		if err := authz.AssignRole(r.Context(), pool, userID, req.RoleID); err != nil {
			fail(w, http.StatusInternalServerError, "Failed to assign role.")
			return
		}
		writeJSON(w, http.StatusOK, models.APIResponse{Success: true, Message: "Role assigned."})

	case http.MethodDelete:
		if len(parts) != 3 || parts[2] == "" {
			fail(w, http.StatusBadRequest, "Expected /api/tenant/users/{userId}/roles/{roleId}.")
			return
		}
		roleID := parts[2]
		// Guard: cannot revoke super_admin if the target user is the last active one.
		role, err := authz.GetRole(r.Context(), pool, roleID)
		if err != nil && !errors.Is(err, authz.ErrRoleNotFound) {
			fail(w, http.StatusInternalServerError, "Failed to load role.")
			return
		}
		if err == nil && role.Key == authz.RoleSuperAdmin {
			count, cErr := userstore.CountActiveSuperAdmins(r.Context(), pool)
			if cErr != nil {
				fail(w, http.StatusInternalServerError, "Failed to check admin count.")
				return
			}
			if count <= 1 {
				isSA, _ := userstore.IsSuperAdmin(r.Context(), pool, userID)
				if isSA {
					fail(w, http.StatusConflict, "Cannot remove the last super admin role. Assign it to another user first.")
					return
				}
			}
		}
		if err := authz.UnassignRole(r.Context(), pool, userID, roleID); err != nil {
			fail(w, http.StatusInternalServerError, "Failed to unassign role.")
			return
		}
		writeJSON(w, http.StatusOK, models.APIResponse{Success: true, Message: "Role unassigned."})

	default:
		fail(w, http.StatusMethodNotAllowed, "Method not allowed")
	}
}

// MyPermissions returns every effective grant the calling user holds in the
// resolved tenant. Used by the frontend to drive role-based sidebar visibility.
// GET /api/tenant/users/me/permissions
func (h *RBACOps) MyPermissions(w http.ResponseWriter, r *http.Request) {
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
	grants, err := authz.EffectiveGrants(r.Context(), pool, payload.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load permissions.")
		return
	}
	if grants == nil {
		grants = []authz.Grant{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":      true,
		"grants":       grants,
		"activeRoleId": payload.ActiveRoleID,
	})
}

// SwitchRole sets or clears the caller's active role for this session. When a
// roleId is set, subsequent authz checks (via authz.EffectiveGrants) consult
// only that role's grants instead of the union of all assigned roles — a hard
// context switch, not a UI-only filter. An empty roleId clears the active
// role, restoring the full aggregate. POST /api/tenant/auth/switch-role
func (h *RBACOps) SwitchRole(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
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

	var req struct {
		RoleID string `json:"roleId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}

	activeRoleID := ""
	if req.RoleID != "" {
		user, err := userstore.GetUserByIdentityID(r.Context(), pool, payload.ID)
		if err != nil {
			fail(w, http.StatusInternalServerError, "Failed to load user.")
			return
		}
		held := false
		for _, ur := range user.Roles {
			if ur.ID == req.RoleID {
				held = true
				break
			}
		}
		if !held {
			fail(w, http.StatusForbidden, "You do not hold this role.")
			return
		}
		activeRoleID = req.RoleID
	}

	d, err := time.ParseDuration(config.AppConfig.JWTExpiresIn)
	if err != nil {
		d = time.Hour
	}
	token, err := generateTenantJWT(payload.ID, payload.Email, payload.TenantID, activeRoleID, d)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to sign token.")
		return
	}

	accessExpiry := time.Now().Add(d)
	http.SetCookie(w, &http.Cookie{
		Name:     "auth_token",
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		Secure:   config.AppConfig.IsProduction(),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(d.Seconds()),
	})

	logSecurityEvent(r, "role_switched", "identity", payload.ID, "active_role_id", activeRoleID)

	writeJSON(w, http.StatusOK, map[string]any{
		"success":      true,
		"token":        token,
		"expiresAt":    accessExpiry.UnixMilli(),
		"activeRoleId": activeRoleID,
	})
}

type roleRequest struct {
	Key         string        `json:"key"`
	Name        string        `json:"name"`
	Description string        `json:"description"`
	Permissions []authz.Grant `json:"permissions"`
}

// isValidationErr reports whether err is a caller-input validation failure
// (unknown permission / invalid scope) versus an infrastructure error.
func isValidationErr(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "unknown permission") || strings.Contains(msg, "invalid scope")
}
