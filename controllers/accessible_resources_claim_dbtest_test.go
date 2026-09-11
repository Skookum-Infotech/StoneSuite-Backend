//go:build dbtest

package controllers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/authz"
	"stonesuite-backend/middleware"
	"stonesuite-backend/tenancy"
)

// decodedAccessibleResources parses a signed access token with the test
// secret (see withTestJWTSecret) and returns its accessible_resources claim,
// or nil if the claim is absent.
func decodedAccessibleResources(t *testing.T, tokenStr string) []string {
	t.Helper()
	token, err := jwt.Parse(tokenStr, func(*jwt.Token) (any, error) {
		return []byte("test-secret"), nil
	})
	require.NoError(t, err)
	claims, ok := token.Claims.(jwt.MapClaims)
	require.True(t, ok)
	raw, ok := claims["accessible_resources"]
	if !ok {
		return nil
	}
	list, ok := raw.([]any)
	require.True(t, ok, "accessible_resources claim is not a list: %v", raw)
	out := make([]string, 0, len(list))
	for _, v := range list {
		s, ok := v.(string)
		require.True(t, ok, "accessible_resources entry is not a string: %v", v)
		out = append(out, s)
	}
	return out
}

// TestTenantOps_Login_MintsAccessibleResourcesClaim_DB confirms a staff
// login token carries accessible_resources derived from the caller's RBAC
// read grants -- the claim stonesuite-notify's bell/feed filters by.
func TestTenantOps_Login_MintsAccessibleResourcesClaim_DB(t *testing.T) {
	withTestJWTSecret(t)
	pool, dsn := testCustomerTenantPool(t)
	cp := newSAMLTestControlPlane(t)
	tenant := seedServableCustomerTestTenant(t, cp, dsn)

	identity, user := seedRBACTestIdentity(t, cp, pool, tenant.ID, "correct-password")
	roleID := seedRBACTestRole(t, pool, "accessible-resources-login", authz.Grant{
		Resource: authz.ResourceEstimate, Action: authz.ActionRead, Scope: authz.ScopeAll,
	})
	require.NoError(t, authz.AssignRole(context.Background(), pool, user.ID, roleID))

	h := &TenantOps{CP: cp, Router: tenancy.NewRouter(nil)}
	req := httptest.NewRequest(http.MethodPost, "/api/auth/tenant-login", jsonBody(t, map[string]any{
		"email": identity.Email, "password": "correct-password",
	}))
	rec := httptest.NewRecorder()
	h.TenantLogin(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	var resp grantsBearingResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, []string{"estimate"}, decodedAccessibleResources(t, resp.Token))
}

// TestRBACOps_SwitchRole_MintsAccessibleResourcesForNewRole_DB confirms
// switch-role's token carries accessible_resources for the NEWLY selected
// role, not the role still active on the request's own (pre-switch) token --
// the same correctness risk TestRBACOps_SwitchRole_ReturnsRoleScopedGrants_DB
// guards for the "grants" response field.
func TestRBACOps_SwitchRole_MintsAccessibleResourcesForNewRole_DB(t *testing.T) {
	withTestJWTSecret(t)
	pool, dsn := testCustomerTenantPool(t)
	cp := newSAMLTestControlPlane(t)
	tenant := seedServableCustomerTestTenant(t, cp, dsn)

	identity, user := seedRBACTestIdentity(t, cp, pool, tenant.ID, "correct-password")
	oldRoleID := seedRBACTestRole(t, pool, "old-role-ar", authz.Grant{
		Resource: authz.ResourceEstimate, Action: authz.ActionRead, Scope: authz.ScopeAll,
	})
	newRoleID := seedRBACTestRole(t, pool, "new-role-ar", authz.Grant{
		Resource: authz.ResourceSalesOrder, Action: authz.ActionRead, Scope: authz.ScopeAll,
	})
	require.NoError(t, authz.AssignRole(context.Background(), pool, user.ID, oldRoleID))
	require.NoError(t, authz.AssignRole(context.Background(), pool, user.ID, newRoleID))

	rbacOps := NewRBACOps()
	resolver := tenancy.NewResolver(cp, tenancy.NewRouter(nil))

	payload := middleware.UserContextPayload{
		ID: identity.ID, Email: identity.Email, TenantID: tenant.ID,
		UserID: user.ID, ActiveRoleID: oldRoleID,
	}
	req := httptest.NewRequest(http.MethodPost, "/api/tenant/auth/switch-role",
		jsonBody(t, map[string]any{"roleId": newRoleID}))
	ctx := context.WithValue(req.Context(), middleware.UserContextKey, payload)
	req = req.WithContext(ctx)

	rec := httptest.NewRecorder()
	resolver.Middleware(http.HandlerFunc(rbacOps.SwitchRole)).ServeHTTP(rec, req)

	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	var resp grantsBearingResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.Equal(t, []string{"sales_order"}, decodedAccessibleResources(t, resp.Token),
		"accessible_resources must reflect the newly-selected role only, never the old active role from the request's own token")
}
