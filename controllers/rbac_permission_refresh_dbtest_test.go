//go:build dbtest

package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"

	"stonesuite-backend/authz"
	"stonesuite-backend/middleware"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/userstore"
)

// grantsBearingResponse covers the fields Login/Refresh/SwitchRole now share:
// the token plus the caller's live effective grants, so a refetch after any
// of the three never needs a separate call to /users/me/permissions.
type grantsBearingResponse struct {
	Success      bool          `json:"success"`
	Token        string        `json:"token"`
	ActiveRoleID string        `json:"activeRoleId"`
	Grants       []authz.Grant `json:"grants"`
}

// seedRBACTestIdentity creates a control-plane identity plus a tenant-DB user
// row and returns both, ready to be assigned roles via authz.AssignRole.
func seedRBACTestIdentity(t *testing.T, cp *tenancy.ControlPlane, pool *pgxpool.Pool, tenantID, password string) (*tenancy.Identity, *userstore.User) {
	t.Helper()
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	email := fmt.Sprintf("rbac-refresh-%s@example.com", suffix)

	hash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.MinCost)
	require.NoError(t, err)
	identity, err := cp.CreateIdentity(ctx, tenantID, email, string(hash), "RBAC Refresh Test User", true)
	require.NoError(t, err)

	user, err := userstore.CreateUser(ctx, pool, identity.ID, email, "RBAC Refresh Test User", "active")
	require.NoError(t, err)

	return identity, user
}

// seedRBACTestRole creates a custom role with a single, distinguishable
// permission and returns its id.
func seedRBACTestRole(t *testing.T, pool *pgxpool.Pool, namePrefix string, grant authz.Grant) string {
	t.Helper()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	roleID, err := authz.CreateRole(context.Background(), pool, namePrefix+"-"+suffix, namePrefix, "", []authz.Grant{grant})
	require.NoError(t, err)
	return roleID
}

// TestTenantOps_Login_ReturnsGrants_DB confirms the login response carries
// the caller's effective grants atomically with the token, so the frontend
// never has to make a separate /users/me/permissions call that could be
// skipped and leave the UI on stale permissions.
func TestTenantOps_Login_ReturnsGrants_DB(t *testing.T) {
	withTestJWTSecret(t)
	pool, dsn := testCustomerTenantPool(t)
	cp := newSAMLTestControlPlane(t)
	tenant := seedServableCustomerTestTenant(t, cp, dsn)

	identity, user := seedRBACTestIdentity(t, cp, pool, tenant.ID, "correct-password")
	roleID := seedRBACTestRole(t, pool, "login-test-role", authz.Grant{
		Resource: authz.ResourceRole, Action: authz.ActionRead, Scope: authz.ScopeAll,
	})
	require.NoError(t, authz.AssignRole(context.Background(), pool, user.ID, roleID))

	h := &TenantOps{CP: cp, Router: tenancy.NewRouter(nil)}
	req := httptest.NewRequest(http.MethodPost, "/api/auth/tenant-login", jsonBody(t, map[string]any{
		"email": identity.Email, "password": "correct-password",
	}))
	rec := httptest.NewRecorder()
	h.TenantLogin(rec, req)

	require.Equal(t, http.StatusOK, rec.Code)
	var resp grantsBearingResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.True(t, resp.Success)
	assert.NotEmpty(t, resp.Token)
	assert.Equal(t, []authz.Grant{{Resource: authz.ResourceRole, Action: authz.ActionRead, Scope: authz.ScopeAll}}, resp.Grants)
}

// TestRBACOps_SwitchRole_ReturnsRoleScopedGrants_DB confirms switch-role
// returns the NEWLY selected role's grants, not the caller's grants at the
// time of the request (the old active role from the token that authenticated
// the switch-role call itself) -- the core correctness risk this fix guards
// against, since EffectiveGrants narrows by whatever active role is on the
// request context.
func TestRBACOps_SwitchRole_ReturnsRoleScopedGrants_DB(t *testing.T) {
	withTestJWTSecret(t)
	pool, dsn := testCustomerTenantPool(t)
	cp := newSAMLTestControlPlane(t)
	tenant := seedServableCustomerTestTenant(t, cp, dsn)

	identity, user := seedRBACTestIdentity(t, cp, pool, tenant.ID, "correct-password")
	oldRoleID := seedRBACTestRole(t, pool, "old-role", authz.Grant{
		Resource: authz.ResourceRole, Action: authz.ActionRead, Scope: authz.ScopeAll,
	})
	newRoleID := seedRBACTestRole(t, pool, "new-role", authz.Grant{
		Resource: authz.ResourceRole, Action: authz.ActionCreate, Scope: authz.ScopeAll,
	})
	require.NoError(t, authz.AssignRole(context.Background(), pool, user.ID, oldRoleID))
	require.NoError(t, authz.AssignRole(context.Background(), pool, user.ID, newRoleID))

	rbacOps := NewRBACOps()
	resolver := tenancy.NewResolver(cp, tenancy.NewRouter(nil))

	// Simulate a request authenticated with the OLD role already active --
	// mirrors what RequireAuth would have injected from a real token minted
	// before this switch.
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

	require.Equal(t, http.StatusOK, rec.Code)
	var resp grantsBearingResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	assert.True(t, resp.Success)
	assert.Equal(t, newRoleID, resp.ActiveRoleID)
	assert.Equal(t, []authz.Grant{{Resource: authz.ResourceRole, Action: authz.ActionCreate, Scope: authz.ScopeAll}}, resp.Grants,
		"grants must reflect the newly-selected role only, never the old active role from the request's own token")
}

// TestTenantOps_RefreshSession_ReturnsAggregateGrants_DB confirms a refreshed
// session always reports the caller's full aggregate grants (every assigned
// role's permissions combined) and an empty activeRoleId, matching the fact
// that refresh always drops any prior active-role narrowing.
func TestTenantOps_RefreshSession_ReturnsAggregateGrants_DB(t *testing.T) {
	withTestJWTSecret(t)
	pool, dsn := testCustomerTenantPool(t)
	cp := newSAMLTestControlPlane(t)
	tenant := seedServableCustomerTestTenant(t, cp, dsn)

	identity, user := seedRBACTestIdentity(t, cp, pool, tenant.ID, "correct-password")
	roleA := seedRBACTestRole(t, pool, "refresh-role-a", authz.Grant{
		Resource: authz.ResourceRole, Action: authz.ActionRead, Scope: authz.ScopeAll,
	})
	roleB := seedRBACTestRole(t, pool, "refresh-role-b", authz.Grant{
		Resource: authz.ResourceRole, Action: authz.ActionCreate, Scope: authz.ScopeAll,
	})
	require.NoError(t, authz.AssignRole(context.Background(), pool, user.ID, roleA))
	require.NoError(t, authz.AssignRole(context.Background(), pool, user.ID, roleB))

	h := &TenantOps{CP: cp, Router: tenancy.NewRouter(nil)}

	loginReq := httptest.NewRequest(http.MethodPost, "/api/auth/tenant-login", jsonBody(t, map[string]any{
		"email": identity.Email, "password": "correct-password",
	}))
	loginRec := httptest.NewRecorder()
	h.TenantLogin(loginRec, loginReq)
	require.Equal(t, http.StatusOK, loginRec.Code)

	var refreshCookie *http.Cookie
	for _, c := range loginRec.Result().Cookies() {
		if c.Name == "refresh_token" {
			refreshCookie = c
		}
	}
	require.NotNil(t, refreshCookie, "expected login to set a refresh_token cookie")

	refreshReq := httptest.NewRequest(http.MethodPost, "/api/auth/refresh", nil)
	refreshReq.AddCookie(refreshCookie)
	refreshRec := httptest.NewRecorder()
	h.RefreshSession(refreshRec, refreshReq)

	require.Equal(t, http.StatusOK, refreshRec.Code)
	var resp grantsBearingResponse
	require.NoError(t, json.Unmarshal(refreshRec.Body.Bytes(), &resp))
	assert.True(t, resp.Success)
	assert.Equal(t, "", resp.ActiveRoleID)
	assert.ElementsMatch(t, []authz.Grant{
		{Resource: authz.ResourceRole, Action: authz.ActionRead, Scope: authz.ScopeAll},
		{Resource: authz.ResourceRole, Action: authz.ActionCreate, Scope: authz.ScopeAll},
	}, resp.Grants)
}

// jsonBody marshals v into a request body reader.
func jsonBody(t *testing.T, v any) *strings.Reader {
	t.Helper()
	b, err := json.Marshal(v)
	require.NoError(t, err)
	return strings.NewReader(string(b))
}
