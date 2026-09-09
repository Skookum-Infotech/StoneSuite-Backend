//go:build dbtest

package controllers

import (
	"context"
	"encoding/json"
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
	"stonesuite-backend/models"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/userstore"
)

// inviteTestEnv bundles everything InviteUser needs: a control plane, a
// servable tenant whose DB is the tenant-schema test database, and an inviter
// holding user:create (plus role:update, so the initialRoleId path is
// exercisable).
type inviteTestEnv struct {
	cp       *tenancy.ControlPlane
	pool     *pgxpool.Pool
	tenant   *tenancy.Tenant
	ops      *UserOps
	resolver *tenancy.Resolver
	payload  middleware.UserContextPayload
}

func newInviteTestEnv(t *testing.T) *inviteTestEnv {
	t.Helper()
	withTestJWTSecret(t)
	pool, dsn := testCustomerTenantPool(t)
	cp := newSAMLTestControlPlane(t)
	tenant := seedServableCustomerTestTenant(t, cp, dsn)

	identity, user := seedRBACTestIdentity(t, cp, pool, tenant.ID, "correct-password")
	roleID, err := authz.CreateRole(context.Background(), pool,
		fmt.Sprintf("invite-admin-%d", time.Now().UnixNano()), "Invite Admin", "",
		[]authz.Grant{
			{Resource: authz.ResourceUser, Action: authz.ActionCreate, Scope: authz.ScopeAll},
			{Resource: authz.ResourceRole, Action: authz.ActionUpdate, Scope: authz.ScopeAll},
		})
	require.NoError(t, err)
	require.NoError(t, authz.AssignRole(context.Background(), pool, user.ID, roleID))

	return &inviteTestEnv{
		cp:       cp,
		pool:     pool,
		tenant:   tenant,
		ops:      NewUserOps(cp, tenancy.NewRouter(nil)),
		resolver: tenancy.NewResolver(cp, tenancy.NewRouter(nil)),
		payload: middleware.UserContextPayload{
			ID: identity.ID, Email: identity.Email,
			TenantID: tenant.ID, UserID: user.ID,
		},
	}
}

// invite drives POST /api/tenant/users/invite through the tenancy resolver, the
// way the real route does, and returns the recorder plus the decoded envelope.
func (e *inviteTestEnv) invite(t *testing.T, body map[string]any) (*httptest.ResponseRecorder, models.APIResponse) {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/tenant/users/invite", jsonBody(t, body))
	req = req.WithContext(context.WithValue(req.Context(), middleware.UserContextKey, e.payload))
	rec := httptest.NewRecorder()
	e.resolver.Middleware(http.HandlerFunc(e.ops.InviteUser)).ServeHTTP(rec, req)

	var resp models.APIResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	return rec, resp
}

// invitesFor returns this tenant's invites for one email address.
func (e *inviteTestEnv) invitesFor(t *testing.T, email string) []tenancy.UserInvite {
	t.Helper()
	all, err := e.cp.ListUserInvitesByTenant(context.Background(), e.tenant.ID)
	require.NoError(t, err)
	var out []tenancy.UserInvite
	for _, inv := range all {
		if inv.Email == email {
			out = append(out, inv)
		}
	}
	return out
}

func inviteTestEmail(prefix string) string {
	return fmt.Sprintf("%s-%d@example.com", prefix, time.Now().UnixNano())
}

// TestUserOps_InviteUser_ConflictMessages_DB covers the guard chain in
// InviteUser. The regression it exists for: an *expired* pending invite used to
// slip past the guard and collide with the user_invites partial unique index,
// surfacing as a bare 500 "Failed to create invitation." Every un-invitable
// case must now produce a specific message, and an expired invite must be
// superseded rather than rejected.
//
// services.SendUserInviteEmailWithResult has no NOTIFY config here and fails
// cleanly; the handler treats that as non-fatal, so these assertions are on
// status + message + control-plane row state, never on emailSent.
func TestUserOps_InviteUser_ConflictMessages_DB(t *testing.T) {
	env := newInviteTestEnv(t)
	ctx := context.Background()

	t.Run("fresh email creates one pending invite", func(t *testing.T) {
		email := inviteTestEmail("fresh")
		rec, resp := env.invite(t, map[string]any{"email": email, "fullName": "Fresh Invitee"})

		require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())
		assert.True(t, resp.Success)
		assert.Equal(t, "Invitation sent.", resp.Message)

		invites := env.invitesFor(t, email)
		require.Len(t, invites, 1)
		assert.Equal(t, "pending", invites[0].Status)
	})

	t.Run("email already a workspace member", func(t *testing.T) {
		_, member := seedRBACTestIdentity(t, env.cp, env.pool, env.tenant.ID, "member-password")

		rec, resp := env.invite(t, map[string]any{"email": member.Email})

		assert.Equal(t, http.StatusConflict, rec.Code)
		assert.False(t, resp.Success)
		assert.Equal(t, "This email address is already a member of this workspace.", resp.Message)
	})

	t.Run("still-valid pending invite is not superseded", func(t *testing.T) {
		email := inviteTestEmail("valid-pending")
		_, err := env.cp.CreateUserInvite(ctx, env.tenant.ID, email, "Pending Invitee", "",
			"tok-valid-"+email, "", time.Now().Add(userInviteExpiry))
		require.NoError(t, err)

		rec, resp := env.invite(t, map[string]any{"email": email})

		assert.Equal(t, http.StatusConflict, rec.Code)
		assert.False(t, resp.Success)
		assert.Contains(t, resp.Message, "already has a pending invitation")
		assert.Contains(t, resp.Message, "Resend")

		// Untouched: still exactly one invite, still pending.
		invites := env.invitesFor(t, email)
		require.Len(t, invites, 1)
		assert.Equal(t, "pending", invites[0].Status)
	})

	t.Run("expired pending invite is superseded", func(t *testing.T) {
		email := inviteTestEmail("expired-pending")
		stale, err := env.cp.CreateUserInvite(ctx, env.tenant.ID, email, "Stale Invitee", "",
			"tok-stale-"+email, "", time.Now().Add(-time.Hour))
		require.NoError(t, err)

		rec, resp := env.invite(t, map[string]any{"email": email, "fullName": "Fresh Name"})

		require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())
		assert.True(t, resp.Success)
		assert.Equal(t, "Invitation sent.", resp.Message)

		invites := env.invitesFor(t, email)
		require.Len(t, invites, 2, "expected the stale invite plus its replacement")

		byID := map[string]tenancy.UserInvite{}
		for _, inv := range invites {
			byID[inv.ID] = inv
		}
		old, ok := byID[stale.ID]
		require.True(t, ok, "the stale invite row should still exist")
		assert.Equal(t, "revoked", old.Status, "stale invite must be revoked, not left pending")

		var fresh tenancy.UserInvite
		for _, inv := range invites {
			if inv.ID != stale.ID {
				fresh = inv
			}
		}
		assert.Equal(t, "pending", fresh.Status)
		assert.NotEqual(t, stale.Token, fresh.Token, "replacement must carry a new token")
		assert.True(t, time.Now().Before(fresh.ExpiresAt), "replacement must not be born expired")
		// revoke-then-create (not RefreshUserInvite) so the new request's
		// fullName reaches the row rather than being silently dropped.
		assert.Equal(t, "Fresh Name", fresh.FullName)
	})

	t.Run("superseding carries the new initialRoleId", func(t *testing.T) {
		email := inviteTestEmail("expired-role")
		stale, err := env.cp.CreateUserInvite(ctx, env.tenant.ID, email, "Stale Invitee", "",
			"tok-stale-role-"+email, "", time.Now().Add(-time.Hour))
		require.NoError(t, err)
		require.Empty(t, stale.InitialRoleID, "stale invite starts with no role")

		roleID := seedRBACTestRole(t, env.pool, "invite-preassigned", authz.Grant{
			Resource: authz.ResourceRole, Action: authz.ActionRead, Scope: authz.ScopeAll,
		})
		rec, _ := env.invite(t, map[string]any{"email": email, "initialRoleId": roleID})
		require.Equal(t, http.StatusCreated, rec.Code, "body: %s", rec.Body.String())

		var fresh tenancy.UserInvite
		for _, inv := range env.invitesFor(t, email) {
			if inv.ID != stale.ID {
				fresh = inv
			}
		}
		assert.Equal(t, roleID, fresh.InitialRoleID,
			"RefreshUserInvite would have dropped the newly requested role")
	})

	t.Run("email registered to another workspace", func(t *testing.T) {
		otherTenantID := seedSAMLTestTenant(t, env.cp)
		email := inviteTestEmail("other-workspace")
		_, err := env.cp.CreateIdentity(ctx, otherTenantID, email, "", "Other Workspace User", true)
		require.NoError(t, err)

		rec, resp := env.invite(t, map[string]any{"email": email})

		assert.Equal(t, http.StatusConflict, rec.Code)
		assert.False(t, resp.Success)
		assert.Equal(t, "This email address is already registered to another workspace.", resp.Message)
	})

	t.Run("caller cannot invite themselves", func(t *testing.T) {
		rec, resp := env.invite(t, map[string]any{"email": env.payload.Email})

		assert.Equal(t, http.StatusBadRequest, rec.Code)
		assert.Equal(t, "You cannot invite yourself.", resp.Message)
	})
}

// TestUserOps_InviteUser_MemberLookupSentinel_DB pins the assumption the
// member guard's three-way switch rests on: GetUserByEmail reports a genuine
// miss as userstore.ErrUserNotFound, so anything else really is a database
// error worth a 500 rather than a silently skipped guard.
func TestUserOps_InviteUser_MemberLookupSentinel_DB(t *testing.T) {
	pool, _ := testCustomerTenantPool(t)
	_, err := userstore.GetUserByEmail(context.Background(), pool, inviteTestEmail("nobody"))
	assert.ErrorIs(t, err, userstore.ErrUserNotFound)
}
