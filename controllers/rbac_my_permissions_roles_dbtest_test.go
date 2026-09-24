//go:build dbtest

package controllers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/authz"
	"stonesuite-backend/middleware"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/userstore"
)

// myPermissionsResponse is the GET /api/tenant/users/me/permissions body.
type myPermissionsResponse struct {
	Success      bool                    `json:"success"`
	Grants       []authz.Grant           `json:"grants"`
	ActiveRoleID string                  `json:"activeRoleId"`
	Roles        []userstore.RoleSummary `json:"roles"`
}

// callMyPermissions invokes MyPermissions the way the router does -- behind the
// tenancy resolver, with the caller's identity on the request context.
func callMyPermissions(t *testing.T, cp *tenancy.ControlPlane, tenantID string, identity *tenancy.Identity, user *userstore.User) myPermissionsResponse {
	t.Helper()
	payload := middleware.UserContextPayload{
		ID: identity.ID, Email: identity.Email, TenantID: tenantID, UserID: user.ID,
	}
	req := httptest.NewRequest(http.MethodGet, "/api/tenant/users/me/permissions", nil)
	req = req.WithContext(context.WithValue(req.Context(), middleware.UserContextKey, payload))

	rec := httptest.NewRecorder()
	resolver := tenancy.NewResolver(cp, tenancy.NewRouter(nil))
	resolver.Middleware(http.HandlerFunc(NewRBACOps().MyPermissions)).ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp myPermissionsResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.True(t, resp.Success)
	return resp
}

func roleIDsOf(roles []userstore.RoleSummary) []string {
	ids := make([]string, 0, len(roles))
	for _, r := range roles {
		ids = append(ids, r.ID)
	}
	return ids
}

// TestRBACOps_MyPermissions_ReturnsLiveRoles_DB confirms the caller's own roles
// ride along with their grants and track assignments made mid-session -- the
// source the header menu and Account Settings read so a role assigned or
// removed shows up without signing out and back in.
func TestRBACOps_MyPermissions_ReturnsLiveRoles_DB(t *testing.T) {
	withTestJWTSecret(t)
	pool, dsn := testCustomerTenantPool(t)
	cp := newSAMLTestControlPlane(t)
	tenant := seedServableCustomerTestTenant(t, cp, dsn)
	ctx := context.Background()

	identity, user := seedRBACTestIdentity(t, cp, pool, tenant.ID, "correct-password")

	// No roles yet: an empty list, never null, so the frontend can tell "none
	// assigned" apart from a backend that predates the field.
	resp := callMyPermissions(t, cp, tenant.ID, identity, user)
	require.NotNil(t, resp.Roles, "roles must serialize as [] (not null) when none are assigned")
	assert.Empty(t, resp.Roles)

	roleA := seedRBACTestRole(t, pool, "my-roles-a", authz.Grant{
		Resource: authz.ResourceRole, Action: authz.ActionRead, Scope: authz.ScopeAll,
	})
	roleB := seedRBACTestRole(t, pool, "my-roles-b", authz.Grant{
		Resource: authz.ResourceRole, Action: authz.ActionCreate, Scope: authz.ScopeAll,
	})

	require.NoError(t, authz.AssignRole(ctx, pool, user.ID, roleA))
	resp = callMyPermissions(t, cp, tenant.ID, identity, user)
	assert.ElementsMatch(t, []string{roleA}, roleIDsOf(resp.Roles))

	require.NoError(t, authz.AssignRole(ctx, pool, user.ID, roleB))
	resp = callMyPermissions(t, cp, tenant.ID, identity, user)
	assert.ElementsMatch(t, []string{roleA, roleB}, roleIDsOf(resp.Roles),
		"a role assigned mid-session must appear on the very next call")

	require.NoError(t, authz.UnassignRole(ctx, pool, user.ID, roleA))
	resp = callMyPermissions(t, cp, tenant.ID, identity, user)
	assert.ElementsMatch(t, []string{roleB}, roleIDsOf(resp.Roles),
		"a role removed mid-session must disappear on the very next call")
}

// TestRBACOps_MyPermissions_RolesNotNarrowedByActiveRole confirms roles lists
// every role the caller holds even while one is selected as the active role:
// the switch-role menu needs the full set to let the user switch back.
func TestRBACOps_MyPermissions_RolesNotNarrowedByActiveRole_DB(t *testing.T) {
	withTestJWTSecret(t)
	pool, dsn := testCustomerTenantPool(t)
	cp := newSAMLTestControlPlane(t)
	tenant := seedServableCustomerTestTenant(t, cp, dsn)
	ctx := context.Background()

	identity, user := seedRBACTestIdentity(t, cp, pool, tenant.ID, "correct-password")
	roleA := seedRBACTestRole(t, pool, "active-a", authz.Grant{
		Resource: authz.ResourceRole, Action: authz.ActionRead, Scope: authz.ScopeAll,
	})
	roleB := seedRBACTestRole(t, pool, "active-b", authz.Grant{
		Resource: authz.ResourceRole, Action: authz.ActionCreate, Scope: authz.ScopeAll,
	})
	require.NoError(t, authz.AssignRole(ctx, pool, user.ID, roleA))
	require.NoError(t, authz.AssignRole(ctx, pool, user.ID, roleB))

	payload := middleware.UserContextPayload{
		ID: identity.ID, Email: identity.Email, TenantID: tenant.ID,
		UserID: user.ID, ActiveRoleID: roleA,
	}
	req := httptest.NewRequest(http.MethodGet, "/api/tenant/users/me/permissions", nil)
	req = req.WithContext(context.WithValue(req.Context(), middleware.UserContextKey, payload))
	rec := httptest.NewRecorder()
	resolver := tenancy.NewResolver(cp, tenancy.NewRouter(nil))
	resolver.Middleware(http.HandlerFunc(NewRBACOps().MyPermissions)).ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp myPermissionsResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, roleA, resp.ActiveRoleID)
	assert.ElementsMatch(t, []string{roleA, roleB}, roleIDsOf(resp.Roles),
		"roles must list every held role, not just the active one")
}
