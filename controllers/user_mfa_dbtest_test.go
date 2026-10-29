//go:build dbtest

package controllers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/authz"
	"stonesuite-backend/middleware"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/userstore"
)

const adminMFAUnknownUser = "00000000-0000-4000-8000-000000000000"

// adminMFAFixture is a workspace with a super admin, a role-holder who is not
// one, and a few ordinary members to turn two-step on and off for.
type adminMFAFixture struct {
	pool     *pgxpool.Pool
	cp       *tenancy.ControlPlane
	ops      *UserOps
	resolver *tenancy.Resolver
	tenantID string
	admin    *userstore.User
	adminID  *tenancy.Identity
	manager  *userstore.User // holds user:update but is not a super admin
	managerI *tenancy.Identity
	members  []*userstore.User
}

func newAdminMFAFixture(t *testing.T) *adminMFAFixture {
	t.Helper()
	withTestJWTSecret(t)
	pool, dsn := testCustomerTenantPool(t)
	cp := newSAMLTestControlPlane(t)
	tenant := seedServableCustomerTestTenant(t, cp, dsn)
	ctx := context.Background()
	router := tenancy.NewRouter(nil)

	adminIdentity, admin := seedRBACTestIdentity(t, cp, pool, tenant.ID, "pw-admin")
	saRole, err := authz.SeedSuperAdmin(ctx, pool)
	require.NoError(t, err)
	require.NoError(t, authz.AssignRole(ctx, pool, admin.ID, saRole))

	managerIdentity, manager := seedRBACTestIdentity(t, cp, pool, tenant.ID, "pw-manager")
	role := seedRBACTestRole(t, pool, "mfa-manager", authz.Grant{
		Resource: authz.ResourceUser, Action: authz.ActionUpdate, Scope: authz.ScopeAll,
	})
	require.NoError(t, authz.AssignRole(ctx, pool, manager.ID, role))

	f := &adminMFAFixture{
		pool: pool, cp: cp, ops: NewUserOps(cp, router), resolver: tenancy.NewResolver(cp, router),
		tenantID: tenant.ID, admin: admin, adminID: adminIdentity, manager: manager, managerI: managerIdentity,
	}
	for i := 0; i < 2; i++ {
		_, member := seedRBACTestIdentity(t, cp, pool, tenant.ID, "pw-member")
		f.members = append(f.members, member)
	}
	return f
}

// seedUser adds a member whose identity has the given password hash ("" for a
// single-sign-on style identity) and status.
func (f *adminMFAFixture) seedUser(t *testing.T, passwordHash, status string) *userstore.User {
	t.Helper()
	email := fmt.Sprintf("admin-mfa-%d@example.com", time.Now().UnixNano())
	identity, err := f.cp.CreateIdentity(context.Background(), f.tenantID, email, passwordHash, "Seeded Member", true)
	require.NoError(t, err)
	u, err := userstore.CreateUser(context.Background(), f.pool, identity.ID, email, "Seeded Member", status)
	require.NoError(t, err)
	return u
}

func (f *adminMFAFixture) do(t *testing.T, identity *tenancy.Identity, user *userstore.User, handler http.HandlerFunc, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, jsonBody(t, body))
	req = req.WithContext(context.WithValue(req.Context(), middleware.UserContextKey,
		middleware.UserContextPayload{ID: identity.ID, Email: identity.Email, TenantID: f.tenantID}))
	rec := httptest.NewRecorder()
	f.resolver.Middleware(handler).ServeHTTP(rec, req)
	return rec
}

func (f *adminMFAFixture) setMFA(t *testing.T, ids []string, enabled bool) *httptest.ResponseRecorder {
	t.Helper()
	return f.do(t, f.adminID, f.admin, f.ops.SetUsersMFA, http.MethodPost, "/api/tenant/users/mfa",
		map[string]any{"userIds": ids, "enabled": enabled})
}

func (f *adminMFAFixture) identityEnabled(t *testing.T, u *userstore.User) bool {
	t.Helper()
	identity, err := f.cp.IdentityByID(context.Background(), u.IdentityID)
	require.NoError(t, err)
	return identity.MFAEnabled
}

// resultsByUser indexes the response's per-member results.
func resultsByUser(t *testing.T, rec *httptest.ResponseRecorder) map[string]map[string]any {
	t.Helper()
	body := decodeBody(t, rec)
	list, _ := body["results"].([]any)
	out := map[string]map[string]any{}
	for _, item := range list {
		m := item.(map[string]any)
		out[m["userId"].(string)] = m
	}
	return out
}

func TestSetUsersMFA_SuperAdminTurnsOnAndOff_DB(t *testing.T) {
	sent := fakeNotify(t)
	f := newAdminMFAFixture(t)
	ids := []string{f.members[0].ID, f.members[1].ID}

	on := f.setMFA(t, ids, true)
	require.Equal(t, http.StatusOK, on.Code, on.Body.String())
	for _, id := range ids {
		res := resultsByUser(t, on)[id]
		assert.Equal(t, "changed", res["status"])
		assert.Equal(t, true, res["emailSent"])
	}
	assert.True(t, f.identityEnabled(t, f.members[0]))
	assert.True(t, f.identityEnabled(t, f.members[1]))
	require.Len(t, *sent, 2, "each member is told an administrator changed it")
	assert.Equal(t, "identity.mfa_enabled", (*sent)[0]["eventType"])
	assert.Contains(t, (*sent)[0]["emailBodyHtml"], "A workspace administrator turned on")

	again := f.setMFA(t, ids, true)
	require.Equal(t, http.StatusOK, again.Code, again.Body.String())
	assert.Equal(t, "unchanged", resultsByUser(t, again)[ids[0]]["status"])
	assert.Len(t, *sent, 2, "no second notice when nothing changed")

	off := f.setMFA(t, ids[:1], false)
	require.Equal(t, http.StatusOK, off.Code, off.Body.String())
	assert.Equal(t, "changed", resultsByUser(t, off)[ids[0]]["status"])
	assert.False(t, f.identityEnabled(t, f.members[0]))
	assert.True(t, f.identityEnabled(t, f.members[1]), "members outside the request are untouched")
	require.Len(t, *sent, 3)
	assert.Equal(t, "identity.mfa_disabled", (*sent)[2]["eventType"])
}

func TestSetUsersMFA_SkipsWhatItCannotChange_DB(t *testing.T) {
	fakeNotify(t)
	f := newAdminMFAFixture(t)
	sso := f.seedUser(t, "", "active")
	gone := f.seedUser(t, "hash", "disabled")
	ok := f.members[0]

	rec := f.setMFA(t, []string{ok.ID, f.admin.ID, sso.ID, gone.ID, adminMFAUnknownUser}, true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	res := resultsByUser(t, rec)

	assert.Equal(t, "changed", res[ok.ID]["status"], "one bad row never blocks the rest")
	assert.Equal(t, "self", res[f.admin.ID]["reason"], "your own setting goes through Account Settings")
	assert.Equal(t, "mfa_unavailable", res[sso.ID]["reason"])
	assert.Equal(t, "deactivated", res[gone.ID]["reason"])
	assert.Equal(t, "not_found", res[adminMFAUnknownUser]["reason"])
	assert.False(t, f.identityEnabled(t, f.admin))
	assert.False(t, f.identityEnabled(t, sso))
	assert.False(t, f.identityEnabled(t, gone))
}

func TestSetUsersMFA_OnlySuperAdmins_DB(t *testing.T) {
	sent := fakeNotify(t)
	f := newAdminMFAFixture(t)

	rec := f.do(t, f.managerI, f.manager, f.ops.SetUsersMFA, http.MethodPost, "/api/tenant/users/mfa",
		map[string]any{"userIds": []string{f.members[0].ID}, "enabled": true})
	assert.Equal(t, http.StatusForbidden, rec.Code, "user:update alone is not enough")
	assert.False(t, f.identityEnabled(t, f.members[0]))
	assert.Empty(t, *sent)
}

func TestSetUsersMFA_RejectsBadRequests_DB(t *testing.T) {
	fakeNotify(t)
	f := newAdminMFAFixture(t)

	cases := map[string]any{
		"no ids":         map[string]any{"userIds": []string{}, "enabled": true},
		"missing enable": map[string]any{"userIds": []string{f.members[0].ID}},
		"bad id":         map[string]any{"userIds": []string{"not-a-uuid"}, "enabled": true},
	}
	for name, body := range cases {
		rec := f.do(t, f.adminID, f.admin, f.ops.SetUsersMFA, http.MethodPost, "/api/tenant/users/mfa", body)
		assert.Equal(t, http.StatusBadRequest, rec.Code, name)
	}
	assert.False(t, f.identityEnabled(t, f.members[0]))
}

func TestListUsers_ReportsTwoStepSetting_DB(t *testing.T) {
	fakeNotify(t)
	f := newAdminMFAFixture(t)
	sso := f.seedUser(t, "", "active")
	require.NoError(t, f.cp.SetIdentityMFAEnabled(context.Background(), f.members[0].IdentityID, true))

	rec := f.do(t, f.adminID, f.admin, f.ops.ListUsers, http.MethodGet, "/api/tenant/users", nil)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	byID := map[string]map[string]any{}
	for _, item := range decodeBody(t, rec)["users"].([]any) {
		m := item.(map[string]any)
		byID[m["id"].(string)] = m
	}

	assert.Equal(t, true, byID[f.members[0].ID]["mfaEnabled"])
	assert.Equal(t, false, byID[f.members[1].ID]["mfaEnabled"])
	assert.Equal(t, true, byID[f.members[1].ID]["mfaAvailable"])
	assert.Equal(t, false, byID[sso.ID]["mfaAvailable"], "single sign-on has no password step to add a code to")
	assert.Equal(t, f.members[0].Email, byID[f.members[0].ID]["email"], "the user fields are still flat")
}
