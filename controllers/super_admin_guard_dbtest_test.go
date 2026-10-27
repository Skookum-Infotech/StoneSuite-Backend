//go:build dbtest

package controllers

import (
	"context"
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

// guardCall drives a handler as identity, with a path value for {id}.
func (f *adminMFAFixture) guardCall(t *testing.T, identity *tenancy.Identity, handler http.HandlerFunc, method, path, id string, body any) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, jsonBody(t, body))
	req.SetPathValue("id", id)
	req = req.WithContext(context.WithValue(req.Context(), middleware.UserContextKey,
		middleware.UserContextPayload{ID: identity.ID, Email: identity.Email, TenantID: f.tenantID}))
	rec := httptest.NewRecorder()
	f.resolver.Middleware(handler).ServeHTTP(rec, req)
	return rec
}

// grantManager gives the fixture's non-super-admin the rest of the permissions
// these routes check, so the only thing standing between them and a super
// admin target is the guard.
func (f *adminMFAFixture) grantManager(t *testing.T) {
	t.Helper()
	for _, g := range []authz.Grant{
		{Resource: authz.ResourceUser, Action: authz.ActionDelete, Scope: authz.ScopeAll},
		{Resource: authz.ResourceRole, Action: authz.ActionUpdate, Scope: authz.ScopeAll},
	} {
		role := seedRBACTestRole(t, f.pool, "guard-manager", g)
		require.NoError(t, authz.AssignRole(context.Background(), f.pool, f.manager.ID, role))
	}
}

func TestSuperAdminGuard_NonSuperAdminCannotChangeOne_DB(t *testing.T) {
	fakeNotify(t)
	f := newAdminMFAFixture(t)
	f.grantManager(t)
	ctx := context.Background()
	rbac := NewRBACOps()
	plainRole := seedRBACTestRole(t, f.pool, "guard-plain", authz.Grant{
		Resource: authz.ResourceQuote, Action: authz.ActionRead, Scope: authz.ScopeAll,
	})

	t.Run("rename or suspend is refused and nothing changes", func(t *testing.T) {
		rec := f.guardCall(t, f.managerI, f.ops.UpdateUser, http.MethodPatch, "/api/tenant/users/"+f.admin.ID, f.admin.ID,
			map[string]any{"fullName": "Hacked", "status": "suspended"})
		assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())
		assert.Contains(t, rec.Body.String(), msgSuperAdminOnly)

		got, err := userstore.GetUserByID(ctx, f.pool, f.admin.ID)
		require.NoError(t, err)
		assert.Equal(t, f.admin.FullName, got.FullName)
		assert.Equal(t, "active", got.Status)
	})

	t.Run("deactivate is refused", func(t *testing.T) {
		rec := f.guardCall(t, f.managerI, f.ops.DeactivateUser, http.MethodDelete, "/api/tenant/users/"+f.admin.ID, f.admin.ID, nil)
		assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())

		got, err := userstore.GetUserByID(ctx, f.pool, f.admin.ID)
		require.NoError(t, err)
		assert.Equal(t, "active", got.Status)
	})

	t.Run("assigning or removing a role on them is refused", func(t *testing.T) {
		rec := f.guardCall(t, f.managerI, rbac.UserRoles, http.MethodPost, "/api/tenant/users/"+f.admin.ID+"/roles", "",
			map[string]any{"roleId": plainRole})
		assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())

		saRole, err := authz.SeedSuperAdmin(ctx, f.pool)
		require.NoError(t, err)
		rec = f.guardCall(t, f.managerI, rbac.UserRoles, http.MethodDelete, "/api/tenant/users/"+f.admin.ID+"/roles/"+saRole, "", nil)
		assert.Equal(t, http.StatusForbidden, rec.Code, rec.Body.String())

		isSA, err := userstore.IsSuperAdmin(ctx, f.pool, f.admin.ID)
		require.NoError(t, err)
		assert.True(t, isSA, "they are still a super admin")
	})

	t.Run("ordinary members are still theirs to change", func(t *testing.T) {
		rec := f.guardCall(t, f.managerI, f.ops.UpdateUser, http.MethodPatch, "/api/tenant/users/"+f.members[0].ID, f.members[0].ID,
			map[string]any{"fullName": "Renamed Member"})
		assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

		rec = f.guardCall(t, f.managerI, rbac.UserRoles, http.MethodPost, "/api/tenant/users/"+f.members[0].ID+"/roles", "",
			map[string]any{"roleId": plainRole})
		assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	})
}

func TestSuperAdminGuard_SuperAdminMayChangeAnother_DB(t *testing.T) {
	fakeNotify(t)
	f := newAdminMFAFixture(t)
	ctx := context.Background()

	// A second super admin, so the last-super-admin guard doesn't get in the way.
	other := f.seedUser(t, "hash", "active")
	saRole, err := authz.SeedSuperAdmin(ctx, f.pool)
	require.NoError(t, err)
	require.NoError(t, authz.AssignRole(ctx, f.pool, other.ID, saRole))

	rec := f.guardCall(t, f.adminID, f.ops.UpdateUser, http.MethodPatch, "/api/tenant/users/"+other.ID, other.ID,
		map[string]any{"status": "suspended"})
	assert.Equal(t, http.StatusOK, rec.Code, rec.Body.String())

	got, err := userstore.GetUserByID(ctx, f.pool, other.ID)
	require.NoError(t, err)
	assert.Equal(t, "suspended", got.Status)
}
