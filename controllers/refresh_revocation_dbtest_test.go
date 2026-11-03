//go:build dbtest

package controllers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
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

// revocationFixture is a servable tenant with one password identity that holds
// a workspace user row.
type revocationFixture struct {
	h        *TenantOps
	cpPool   *pgxpool.Pool
	pool     *pgxpool.Pool
	identity *tenancy.Identity
	user     *userstore.User
}

const revocationTestPassword = "correct-password"

func newRevocationFixture(t *testing.T) *revocationFixture {
	t.Helper()
	withTestJWTSecret(t)
	withTokenLifetimes(t, "1h", "24h", "720h")
	pool, dsn := testCustomerTenantPool(t)
	cp := newSAMLTestControlPlane(t)
	cpPool, err := pgxpool.New(context.Background(), os.Getenv("TEST_CP_DATABASE_URL"))
	require.NoError(t, err)
	t.Cleanup(cpPool.Close)
	tenant := seedServableCustomerTestTenant(t, cp, dsn)
	identity, user := seedRBACTestIdentity(t, cp, pool, tenant.ID, revocationTestPassword)
	return &revocationFixture{
		h: &TenantOps{CP: cp, Router: tenancy.NewRouter(nil)}, cpPool: cpPool,
		pool: pool, identity: identity, user: user,
	}
}

// login signs in and returns the response recorder plus the refresh cookie.
func (f *revocationFixture) login(t *testing.T, rememberMe bool) (*httptest.ResponseRecorder, *http.Cookie) {
	t.Helper()
	rec := httptest.NewRecorder()
	f.h.TenantLogin(rec, httptest.NewRequest(http.MethodPost, "/api/auth/tenant-login", jsonBody(t, map[string]any{
		"email": f.identity.Email, "password": revocationTestPassword, "rememberMe": rememberMe,
	})))
	require.Equal(t, http.StatusOK, rec.Code, "login body=%s", rec.Body.String())
	return rec, responseCookie(t, rec, "refresh_token")
}

// refresh calls RefreshSession with c and returns the recorder.
func (f *revocationFixture) refresh(c *http.Cookie) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/auth/refresh", nil)
	req.AddCookie(c)
	rec := httptest.NewRecorder()
	f.h.RefreshSession(rec, req)
	return rec
}

// liveTokenCount counts the identity's unrevoked refresh tokens.
func (f *revocationFixture) liveTokenCount(t *testing.T) int {
	t.Helper()
	var n int
	require.NoError(t, f.cpPool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM refresh_tokens WHERE identity_id = $1 AND revoked_at IS NULL`,
		f.identity.ID).Scan(&n))
	return n
}

func responseCookie(t *testing.T, rec *httptest.ResponseRecorder, name string) *http.Cookie {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == name && c.Value != "" {
			return c
		}
	}
	require.FailNow(t, "missing cookie", name)
	return nil
}

// Reuse of a rotated token past the grace is theft: every session of the
// identity ends, including the thief's successor and other devices.
func TestTenantOps_RefreshSession_ReuseRevokesAllSessions_DB(t *testing.T) {
	f := newRevocationFixture(t)
	_, deviceA := f.login(t, false)
	_, deviceB := f.login(t, false)

	first := f.refresh(deviceA)
	require.Equal(t, http.StatusOK, first.Code, "body=%s", first.Body.String())
	successor := responseCookie(t, first, "refresh_token")

	// Push the rotation of deviceA's token past the grace window.
	_, err := f.cpPool.Exec(context.Background(),
		`UPDATE refresh_tokens SET revoked_at = NOW() - INTERVAL '1 hour' WHERE token_hash = $1`,
		tenancy.HashRefreshToken(deviceA.Value))
	require.NoError(t, err)

	replay := f.refresh(deviceA)
	assert.Equal(t, http.StatusUnauthorized, replay.Code, "body=%s", replay.Body.String())
	assert.Equal(t, 0, f.liveTokenCount(t), "reuse must revoke every token of the identity")
	for name, c := range map[string]*http.Cookie{"successor": successor, "other device": deviceB} {
		assert.Equal(t, http.StatusUnauthorized, f.refresh(c).Code, name)
	}
	assert.Equal(t, 0, f.liveTokenCount(t))
}

// Two refreshes with the same cookie inside the grace leave one live token,
// not a forked session.
func TestTenantOps_RefreshSession_GraceReplayDoesNotFork_DB(t *testing.T) {
	f := newRevocationFixture(t)
	_, c := f.login(t, false)
	for i := range 2 {
		rec := f.refresh(c)
		require.Equal(t, http.StatusOK, rec.Code, "refresh #%d body=%s", i, rec.Body.String())
		assert.Equal(t, 1, f.liveTokenCount(t), "refresh #%d", i)
	}
}

// Remember-me must not mint a long-lived access token: only the refresh token
// is long, and rotation keeps it long.
func TestTenantOps_Login_RememberMeKeepsShortAccessToken_DB(t *testing.T) {
	f := newRevocationFixture(t)
	rec, refresh := f.login(t, true)

	var body struct {
		ExpiresAt int64 `json:"expiresAt"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	assert.WithinDuration(t, time.Now().Add(time.Hour), time.UnixMilli(body.ExpiresAt), time.Minute)
	assert.Equal(t, int(time.Hour.Seconds()), responseCookie(t, rec, "auth_token").MaxAge)
	assert.InDelta(t, (720 * time.Hour).Seconds(), float64(refresh.MaxAge), 60)

	rotated := f.refresh(refresh)
	require.Equal(t, http.StatusOK, rotated.Code, "body=%s", rotated.Body.String())
	assert.Equal(t, int(time.Hour.Seconds()), responseCookie(t, rotated, "auth_token").MaxAge)
	assert.InDelta(t, (720 * time.Hour).Seconds(), float64(responseCookie(t, rotated, "refresh_token").MaxAge), 60,
		"rotation keeps the remember-me lifetime")
}

func TestTenantOps_ResetPassword_RevokesSessions_DB(t *testing.T) {
	f := newRevocationFixture(t)
	_, c := f.login(t, false)
	token := "reset-" + f.identity.ID
	require.NoError(t, f.h.CP.SetIdentityPasswordSetupToken(context.Background(), f.identity.ID, token, time.Now().Add(time.Hour)))

	rec := httptest.NewRecorder()
	f.h.ResetPassword(rec, httptest.NewRequest(http.MethodPost, "/api/auth/reset-password", jsonBody(t, map[string]any{
		"token": token, "newPassword": "brand-new-password",
	})))
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())

	assert.Equal(t, 0, f.liveTokenCount(t))
	assert.Equal(t, http.StatusUnauthorized, f.refresh(c).Code)
}

func TestTenantOps_ChangePassword_KeepsOnlyCurrentSession_DB(t *testing.T) {
	f := newRevocationFixture(t)
	_, current := f.login(t, false)
	_, other := f.login(t, false)

	req := httptest.NewRequest(http.MethodPost, "/api/auth/change-password", jsonBody(t, map[string]any{
		"currentPassword": revocationTestPassword, "newPassword": "brand-new-password",
	}))
	req.AddCookie(current)
	req = req.WithContext(context.WithValue(req.Context(), middleware.UserContextKey,
		middleware.UserContextPayload{ID: f.identity.ID, TenantID: f.identity.TenantID}))
	rec := httptest.NewRecorder()
	f.h.ChangePassword(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, "body=%s", rec.Body.String())

	assert.Equal(t, 1, f.liveTokenCount(t))
	assert.Equal(t, http.StatusUnauthorized, f.refresh(other).Code, "other session ends")
	assert.Equal(t, http.StatusOK, f.refresh(current).Code, "caller's session survives")
}

// A deactivated user's still-valid access token must authorize nothing.
func TestEffectiveGrants_DeactivatedUserHasNone_DB(t *testing.T) {
	f := newRevocationFixture(t)
	ctx := context.Background()
	roleID := seedRBACTestRole(t, f.pool, "deactivate-test-role", authz.Grant{
		Resource: authz.ResourceRole, Action: authz.ActionRead, Scope: authz.ScopeAll,
	})
	require.NoError(t, authz.AssignRole(ctx, f.pool, f.user.ID, roleID))

	g, err := authz.EffectiveGrants(ctx, f.pool, f.identity.ID)
	require.NoError(t, err)
	require.NotEmpty(t, g)

	require.NoError(t, userstore.DeactivateUser(ctx, f.pool, f.user.ID))
	authz.InvalidateGrants(f.pool)

	g, err = authz.EffectiveGrants(ctx, f.pool, f.identity.ID)
	require.NoError(t, err)
	assert.Empty(t, g)
}
