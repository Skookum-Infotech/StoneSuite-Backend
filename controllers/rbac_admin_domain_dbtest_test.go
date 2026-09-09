//go:build dbtest

package controllers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/authz"
	"stonesuite-backend/config"
	"stonesuite-backend/middleware"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/userstore"
)

// seedServablePlatformOwnerTestTenant mirrors seedServableCustomerTestTenant
// (customer_auth_dbtest_test.go) but creates a platform-owner tenant
// (is_platform_owner = true), so tests can exercise the super_admin domain
// gate that only fires for that tenant.
func seedServablePlatformOwnerTestTenant(t *testing.T, cp *tenancy.ControlPlane, dsn string) *tenancy.Tenant {
	t.Helper()
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenant, err := cp.CreateTenant(ctx, "owner-rbac-test-"+suffix, "Owner RBAC Test Tenant", true)
	require.NoError(t, err)
	// tenants.is_platform_owner is a database-enforced singleton (partial
	// unique index idx_tenants_platform_owner): only one row may hold it at
	// a time across the whole shared control-plane test DB. Delete this row
	// as soon as the (sub)test using it finishes so the next platform-owner
	// tenant created anywhere in this test binary doesn't collide with it.
	t.Cleanup(func() {
		_, err := cp.Pool().Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, tenant.ID)
		require.NoError(t, err)
	})
	require.NoError(t, cp.SetTenantProvisioned(ctx, tenant.ID, "owner_rbac_test_db", dsn, 1))
	got, err := cp.TenantByID(ctx, tenant.ID)
	require.NoError(t, err)
	require.True(t, got.Servable(), "seeded tenant must be Servable()")
	require.True(t, got.IsPlatformOwner)
	return got
}

// assignRoleRequest drives RBACOps.UserRoles through the real Resolver
// middleware (not a hand-built context), matching the established pattern in
// rbac_permission_refresh_dbtest_test.go.
func assignRoleRequest(t *testing.T, resolver *tenancy.Resolver, rbacOps *RBACOps, actorIdentityID, actorTenantID, targetUserID, roleID string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/tenant/users/"+targetUserID+"/roles",
		jsonBody(t, map[string]any{"roleId": roleID}))
	payload := middleware.UserContextPayload{ID: actorIdentityID, TenantID: actorTenantID}
	ctx := context.WithValue(req.Context(), middleware.UserContextKey, payload)
	req = req.WithContext(ctx)
	rec := httptest.NewRecorder()
	resolver.Middleware(http.HandlerFunc(rbacOps.UserRoles)).ServeHTTP(rec, req)
	return rec
}

// TestRBACOps_UserRoles_SuperAdminDomainGate_DB proves the two halves of the
// gate together: within the platform-owner tenant, assigning super_admin to
// a non-matching-domain user is denied while a matching-domain user is
// allowed; a normal (non-platform-owner) tenant is completely unaffected.
func TestRBACOps_UserRoles_SuperAdminDomainGate_DB(t *testing.T) {
	orig := config.AppConfig.PlatformAdminEmailDomain
	config.AppConfig.PlatformAdminEmailDomain = "skookuminfotech.com"
	t.Cleanup(func() { config.AppConfig.PlatformAdminEmailDomain = orig })

	pool, dsn := testCustomerTenantPool(t)
	cp := newSAMLTestControlPlane(t)
	rbacOps := NewRBACOps()
	resolver := tenancy.NewResolver(cp, tenancy.NewRouter(nil))

	grantUpdatePermission := func(actorUserID string) {
		roleID := seedRBACTestRole(t, pool, "role-updater", authz.Grant{
			Resource: authz.ResourceRole, Action: authz.ActionUpdate, Scope: authz.ScopeAll,
		})
		require.NoError(t, authz.AssignRole(context.Background(), pool, actorUserID, roleID))
	}

	t.Run("platform-owner tenant denies a non-matching-domain target", func(t *testing.T) {
		tenant := seedServablePlatformOwnerTestTenant(t, cp, dsn)
		actor, actorUser := seedRBACTestIdentity(t, cp, pool, tenant.ID, "actor-password")
		grantUpdatePermission(actorUser.ID)

		_, targetUser := seedRBACTestIdentity(t, cp, pool, tenant.ID, "target-password") // @example.com
		superAdminRoleID, err := authz.SeedSuperAdmin(context.Background(), pool)
		require.NoError(t, err)

		rec := assignRoleRequest(t, resolver, rbacOps, actor.ID, tenant.ID, targetUser.ID, superAdminRoleID)
		assert.Equal(t, http.StatusBadRequest, rec.Code, "body=%s", rec.Body.String())
	})

	t.Run("platform-owner tenant allows a matching-domain target", func(t *testing.T) {
		tenant := seedServablePlatformOwnerTestTenant(t, cp, dsn)
		actor, actorUser := seedRBACTestIdentity(t, cp, pool, tenant.ID, "actor-password")
		grantUpdatePermission(actorUser.ID)

		suffix := fmt.Sprintf("%d", time.Now().UnixNano())
		targetIdentity, err := cp.CreateIdentity(context.Background(), tenant.ID,
			fmt.Sprintf("staff-%s@skookuminfotech.com", suffix), "", "Staff", true)
		require.NoError(t, err)
		targetUser, err := userstore.CreateUser(context.Background(), pool, targetIdentity.ID, targetIdentity.Email, "Staff", "active")
		require.NoError(t, err)

		superAdminRoleID, err := authz.SeedSuperAdmin(context.Background(), pool)
		require.NoError(t, err)

		rec := assignRoleRequest(t, resolver, rbacOps, actor.ID, tenant.ID, targetUser.ID, superAdminRoleID)
		assert.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	})

	t.Run("non-platform-owner tenant is unaffected", func(t *testing.T) {
		tenant := seedServableCustomerTestTenant(t, cp, dsn)
		actor, actorUser := seedRBACTestIdentity(t, cp, pool, tenant.ID, "actor-password")
		grantUpdatePermission(actorUser.ID)

		_, targetUser := seedRBACTestIdentity(t, cp, pool, tenant.ID, "target-password") // @example.com, not skookuminfotech.com
		superAdminRoleID, err := authz.SeedSuperAdmin(context.Background(), pool)
		require.NoError(t, err)

		rec := assignRoleRequest(t, resolver, rbacOps, actor.ID, tenant.ID, targetUser.ID, superAdminRoleID)
		assert.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())
	})
}
