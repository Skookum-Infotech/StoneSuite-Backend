//go:build dbtest

package controllers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/config"
	"stonesuite-backend/middleware"
	"stonesuite-backend/tenancy"
)

const (
	uaChromeWindows = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36"
	uaEdgeWindows   = uaChromeWindows + " Edg/130.0.0.0"
)

// browser is one simulated browser: its user agent and the cookies it holds.
type browser struct {
	ua       string
	refresh  *http.Cookie
	token    string
	takeover *http.Cookie
}

// signIn posts the password sign-in from b, keeping its cookies on success.
func (f *loginFixture) signIn(t *testing.T, b *browser) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/tenant-login", jsonBody(t, map[string]any{
		"email": f.identity.Email, "password": loginTestPassword,
	}))
	req.Header.Set("User-Agent", b.ua)
	if b.refresh != nil {
		req.AddCookie(b.refresh)
	}
	rec := httptest.NewRecorder()
	f.h.TenantLogin(rec, req)
	b.keep(t, rec)
	return rec
}

// takeover redeems the takeover ticket cookie b holds.
func (f *loginFixture) takeover(t *testing.T, b *browser) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/session/takeover", nil)
	req.Header.Set("User-Agent", b.ua)
	if b.takeover != nil {
		req.AddCookie(b.takeover)
	}
	rec := httptest.NewRecorder()
	f.h.SessionTakeover(rec, req)
	b.keep(t, rec)
	return rec
}

// status asks GET /api/auth/session as b.
func (f *loginFixture) status(t *testing.T, b *browser) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/auth/session", nil)
	req = req.WithContext(context.WithValue(req.Context(), middleware.UserContextKey,
		middleware.UserContextPayload{ID: f.identity.ID, TenantID: f.identity.TenantID, SessionID: b.sid(t)}))
	rec := httptest.NewRecorder()
	f.h.SessionStatus(rec, req)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var out map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &out))
	return out
}

// keep stores the access token and refresh cookie a 200 sign-in set.
func (b *browser) keep(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	for _, c := range rec.Result().Cookies() {
		if c.Name == takeoverCookieName && c.Value != "" {
			b.takeover = c
		}
	}
	if rec.Code != http.StatusOK {
		return
	}
	b.refresh = responseCookie(t, rec, "refresh_token")
	var body struct {
		Token string `json:"token"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body))
	b.token = body.Token
}

// sid is the session id in b's access token.
func (b *browser) sid(t *testing.T) string {
	t.Helper()
	tok, err := jwt.Parse(b.token, func(*jwt.Token) (any, error) { return []byte(config.AppConfig.JWTSecret), nil })
	require.NoError(t, err)
	sid, _ := tok.Claims.(jwt.MapClaims)[middleware.SessionIDClaim].(string)
	return sid
}

// TestSessionTakeover_EndToEnd_DB walks the whole single-session flow: a
// second browser is asked, confirms, and the first browser is replaced.
func TestSessionTakeover_EndToEnd_DB(t *testing.T) {
	f := newLoginFixture(t)
	a := &browser{ua: uaChromeWindows}
	b := &browser{ua: uaEdgeWindows}

	require.Equal(t, http.StatusOK, f.signIn(t, a).Code)
	require.NotEmpty(t, a.sid(t), "access token carries the session id")
	assert.Equal(t, tenancy.SessionActive, f.status(t, a)["status"])

	// Browser B is asked first.
	rec := f.signIn(t, b)
	require.Equal(t, http.StatusConflict, rec.Code, rec.Body.String())
	prompt := decodeBody(t, rec)
	assert.Equal(t, codeActiveSession, prompt["code"])
	assert.Equal(t, "Chrome on Windows", prompt["activeSession"].(map[string]any)["deviceLabel"])
	assert.NotContains(t, prompt, "takeoverTicket", "the ticket never travels in the body")
	require.NotNil(t, b.takeover, "the ticket is an HttpOnly cookie on this browser")
	assert.True(t, b.takeover.HttpOnly)
	assert.Equal(t, takeoverCookiePath, b.takeover.Path)
	ticketCookie := b.takeover
	assert.Equal(t, tenancy.SessionActive, f.status(t, a)["status"], "asking changes nothing")

	// Another browser without the cookie cannot redeem it.
	assert.Equal(t, http.StatusGone, f.takeover(t, &browser{ua: uaEdgeWindows}).Code)

	// B confirms.
	rec = f.takeover(t, b)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.NotEqual(t, a.sid(t), b.sid(t))

	st := f.status(t, a)
	assert.Equal(t, tenancy.SessionReplacing, st["status"])
	assert.Equal(t, "Edge on Windows", st["replacedBy"].(map[string]any)["deviceLabel"])
	endsAt := time.UnixMilli(int64(st["endsAt"].(float64)))
	assert.WithinDuration(t, time.Now().Add(sessionHandoverGrace), endsAt, 5*time.Second)

	// A can no longer refresh; B can.
	rec = f.refresh(t, a.refresh.Value)
	require.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, tenancy.CodeSessionReplaced, decodeBody(t, rec)["code"])
	require.Equal(t, http.StatusOK, f.refresh(t, b.refresh.Value).Code)

	// The ticket was single-use.
	assert.Equal(t, http.StatusGone, f.takeover(t, &browser{ua: uaEdgeWindows, takeover: ticketCookie}).Code)
}

func TestSessionGate_SameBrowserKeepsItsSession_DB(t *testing.T) {
	f := newLoginFixture(t)
	a := &browser{ua: uaChromeWindows}
	require.Equal(t, http.StatusOK, f.signIn(t, a).Code)
	first := a.sid(t)

	require.Equal(t, http.StatusOK, f.signIn(t, a).Code, "signing in again in the same browser is not asked")
	assert.Equal(t, first, a.sid(t), "and keeps the browser's session, so its other tabs stay signed in")
}

func TestSessionGate_LogoutThenSignInElsewhereIsNotAsked_DB(t *testing.T) {
	f := newLoginFixture(t)
	a := &browser{ua: uaChromeWindows}
	require.Equal(t, http.StatusOK, f.signIn(t, a).Code)

	req := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	req.AddCookie(a.refresh)
	f.h.Logout(httptest.NewRecorder(), req)

	b := &browser{ua: uaEdgeWindows}
	require.Equal(t, http.StatusOK, f.signIn(t, b).Code)
}

func TestSessionGate_IdleSessionReplacedWithoutAsking_DB(t *testing.T) {
	f := newLoginFixture(t)
	a := &browser{ua: uaChromeWindows}
	require.Equal(t, http.StatusOK, f.signIn(t, a).Code)
	_, err := f.cp.Pool().Exec(context.Background(),
		`UPDATE auth_sessions SET last_seen_at = NOW() - INTERVAL '1 hour' WHERE id = $1`, a.sid(t))
	require.NoError(t, err)

	b := &browser{ua: uaEdgeWindows}
	require.Equal(t, http.StatusOK, f.signIn(t, b).Code)
	assert.Equal(t, tenancy.SessionReplacing, f.status(t, a)["status"], "the idle browser still gets its countdown")
}

func TestSessionGate_SettingOffAllowsSeveralBrowsers_DB(t *testing.T) {
	f := newLoginFixture(t)
	require.NoError(t, f.cp.SetTenantSingleSession(context.Background(), f.tenant.ID, false))
	a, b := &browser{ua: uaChromeWindows}, &browser{ua: uaEdgeWindows}
	require.Equal(t, http.StatusOK, f.signIn(t, a).Code)
	require.Equal(t, http.StatusOK, f.signIn(t, b).Code)
	assert.Equal(t, tenancy.SessionActive, f.status(t, a)["status"])
}

func TestRefresh_LegacyTokenIsAdopted_DB(t *testing.T) {
	f := newLoginFixture(t)
	raw, _, err := issueRefreshToken(context.Background(), f.cp, f.identity.ID, "", false)
	require.NoError(t, err)

	rec := f.refresh(t, raw)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	legacy := &browser{ua: uaChromeWindows}
	legacy.keep(t, rec)
	require.NotEmpty(t, legacy.sid(t), "the refreshed token now carries a session")

	got, err := f.cp.RefreshTokenByHash(context.Background(), tenancy.HashRefreshToken(legacy.refresh.Value))
	require.NoError(t, err)
	assert.Equal(t, legacy.sid(t), got.SessionID, "the rotated refresh token belongs to it")
}
