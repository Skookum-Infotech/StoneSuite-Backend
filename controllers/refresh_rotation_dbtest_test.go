//go:build dbtest

package controllers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/tenancy"
)

// TestTenantOps_RefreshSession_ReplayInsideGraceKeepsSession_DB pins the
// reload-logout fix: a second refresh carrying the SAME refresh cookie (two
// tabs reloading together, or a retry after a lost response) must succeed and
// hand back a fresh session, not read as token theft and clear the cookies.
func TestTenantOps_RefreshSession_ReplayInsideGraceKeepsSession_DB(t *testing.T) {
	withTestJWTSecret(t)
	pool, dsn := testCustomerTenantPool(t)
	cp := newSAMLTestControlPlane(t)
	tenant := seedServableCustomerTestTenant(t, cp, dsn)
	identity, _ := seedRBACTestIdentity(t, cp, pool, tenant.ID, "correct-password")

	h := &TenantOps{CP: cp, Router: tenancy.NewRouter(nil)}

	loginRec := httptest.NewRecorder()
	h.TenantLogin(loginRec, httptest.NewRequest(http.MethodPost, "/api/auth/tenant-login", jsonBody(t, map[string]any{
		"email": identity.Email, "password": "correct-password",
	})))
	require.Equal(t, http.StatusOK, loginRec.Code)

	var refreshCookie *http.Cookie
	for _, c := range loginRec.Result().Cookies() {
		if c.Name == "refresh_token" {
			refreshCookie = c
		}
	}
	require.NotNil(t, refreshCookie, "expected login to set a refresh_token cookie")

	for i, label := range []string{"first refresh", "replay of the same cookie"} {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/refresh", nil)
		req.AddCookie(refreshCookie)
		rec := httptest.NewRecorder()
		h.RefreshSession(rec, req)
		require.Equal(t, http.StatusOK, rec.Code, "%s (#%d): body=%s", label, i, rec.Body.String())
		assert.NotEmpty(t, rec.Result().Cookies(), "%s must set replacement cookies", label)
	}
}

// TestTenantOps_Logout_RefreshAfterLogoutIsRefused_DB pins that logout is
// final: the rotation grace window must not let the just-logged-out refresh
// cookie mint a new session.
func TestTenantOps_Logout_RefreshAfterLogoutIsRefused_DB(t *testing.T) {
	withTestJWTSecret(t)
	pool, dsn := testCustomerTenantPool(t)
	cp := newSAMLTestControlPlane(t)
	tenant := seedServableCustomerTestTenant(t, cp, dsn)
	identity, _ := seedRBACTestIdentity(t, cp, pool, tenant.ID, "correct-password")

	h := &TenantOps{CP: cp, Router: tenancy.NewRouter(nil)}

	loginRec := httptest.NewRecorder()
	h.TenantLogin(loginRec, httptest.NewRequest(http.MethodPost, "/api/auth/tenant-login", jsonBody(t, map[string]any{
		"email": identity.Email, "password": "correct-password",
	})))
	require.Equal(t, http.StatusOK, loginRec.Code)

	var refreshCookie *http.Cookie
	for _, c := range loginRec.Result().Cookies() {
		if c.Name == "refresh_token" {
			refreshCookie = c
		}
	}
	require.NotNil(t, refreshCookie)

	logoutReq := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	logoutReq.AddCookie(refreshCookie)
	logoutRec := httptest.NewRecorder()
	h.Logout(logoutRec, logoutReq)
	require.Equal(t, http.StatusOK, logoutRec.Code)

	req := httptest.NewRequest(http.MethodPost, "/api/auth/refresh", nil)
	req.AddCookie(refreshCookie)
	rec := httptest.NewRecorder()
	h.RefreshSession(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code, "body=%s", rec.Body.String())
}

// TestTenantOps_Logout_ReplayDoesNotRevokeOtherSessions_DB pins that replaying
// a logged-out cookie answers 401 as an unknown token and does NOT take the
// reuse path that revokes every session of the identity: the identity's other
// live sessions (another device) must survive.
func TestTenantOps_Logout_ReplayDoesNotRevokeOtherSessions_DB(t *testing.T) {
	withTestJWTSecret(t)
	pool, dsn := testCustomerTenantPool(t)
	cp := newSAMLTestControlPlane(t)
	tenant := seedServableCustomerTestTenant(t, cp, dsn)
	identity, _ := seedRBACTestIdentity(t, cp, pool, tenant.ID, "correct-password")

	h := &TenantOps{CP: cp, Router: tenancy.NewRouter(nil)}

	login := func() *http.Cookie {
		rec := httptest.NewRecorder()
		h.TenantLogin(rec, httptest.NewRequest(http.MethodPost, "/api/auth/tenant-login", jsonBody(t, map[string]any{
			"email": identity.Email, "password": "correct-password",
		})))
		require.Equal(t, http.StatusOK, rec.Code)
		for _, c := range rec.Result().Cookies() {
			if c.Name == "refresh_token" {
				return c
			}
		}
		require.Fail(t, "login set no refresh_token cookie")
		return nil
	}
	deviceA, deviceB := login(), login()

	logoutReq := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	logoutReq.AddCookie(deviceA)
	logoutRec := httptest.NewRecorder()
	h.Logout(logoutRec, logoutReq)
	require.Equal(t, http.StatusOK, logoutRec.Code)

	// Replay the logged-out cookie, well past any grace: 401, cookies cleared.
	replayReq := httptest.NewRequest(http.MethodPost, "/api/auth/refresh", nil)
	replayReq.AddCookie(deviceA)
	replayRec := httptest.NewRecorder()
	h.RefreshSession(replayRec, replayReq)
	assert.Equal(t, http.StatusUnauthorized, replayRec.Code)

	// The other device's session is still live.
	otherReq := httptest.NewRequest(http.MethodPost, "/api/auth/refresh", nil)
	otherReq.AddCookie(deviceB)
	otherRec := httptest.NewRecorder()
	h.RefreshSession(otherRec, otherReq)
	assert.Equal(t, http.StatusOK, otherRec.Code, "body=%s", otherRec.Body.String())
}
