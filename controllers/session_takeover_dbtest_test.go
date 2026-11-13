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

	"github.com/golang-jwt/jwt/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/authz"
	"stonesuite-backend/config"
	"stonesuite-backend/middleware"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/userstore"
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
	if rec.Code != http.StatusOK || !hasAuthCookie(rec) {
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

func TestSessionGate_SameBrowserIsAskedToo_DB(t *testing.T) {
	f := newLoginFixture(t)
	f.setLimits(t, 0, 0)
	a := &browser{ua: uaChromeWindows}
	require.Equal(t, http.StatusOK, f.signIn(t, a).Code)

	rec := f.signIn(t, a)
	require.Equal(t, http.StatusConflict, rec.Code, "no own-browser shortcut: a presented cookie proves nothing")
	assert.Equal(t, codeActiveSession, decodeBody(t, rec)["code"])
}

// TestSessionGate_CopiedCookieCannotJoinTheSession_DB is the bypass where
// browser A's refresh cookie is copied into browser B before B signs in.
func TestSessionGate_CopiedCookieCannotJoinTheSession_DB(t *testing.T) {
	f := newLoginFixture(t)
	f.setLimits(t, 0, 0)
	a := &browser{ua: uaChromeWindows}
	require.Equal(t, http.StatusOK, f.signIn(t, a).Code)

	b := &browser{ua: uaChromeWindows, refresh: a.refresh} // same browser type, A's cookie
	rec := f.signIn(t, b)
	require.Equal(t, http.StatusConflict, rec.Code, "B is asked like any other browser")
	require.Equal(t, http.StatusOK, f.takeover(t, b).Code)

	assert.NotEqual(t, a.sid(t), b.sid(t), "B gets a session of its own")
	assert.Equal(t, tenancy.SessionReplacing, f.status(t, a)["status"], "and A is replaced, not joined")
	assert.Equal(t, http.StatusUnauthorized, f.refresh(t, a.refresh.Value).Code)
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
	_, err := f.cp.UpdateTenantSessionPolicy(context.Background(), f.tenant.ID, tenancy.SessionPolicyPatch{Enforced: new(bool)})
	require.NoError(t, err)
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

// setLimits sets the fixture tenant's takeover cooldown (minutes) and daily cap.
func (f *loginFixture) setLimits(t *testing.T, cooldownMinutes, dailyCap int) {
	t.Helper()
	_, err := f.cp.UpdateTenantSessionPolicy(context.Background(), f.tenant.ID,
		tenancy.SessionPolicyPatch{CooldownMinutes: &cooldownMinutes, DailyCap: &dailyCap})
	require.NoError(t, err)
}

// moveSignIn signs b in over the session open elsewhere and confirms the takeover.
func (f *loginFixture) moveSignIn(t *testing.T, b *browser) *httptest.ResponseRecorder {
	t.Helper()
	rec := f.signIn(t, b)
	if rec.Code != http.StatusConflict {
		return rec
	}
	return f.takeover(t, b)
}

func TestTakeoverCooldown_DB(t *testing.T) {
	f := newLoginFixture(t)
	f.setLimits(t, 5, 0)
	a, b := &browser{ua: uaChromeWindows}, &browser{ua: uaEdgeWindows}
	require.Equal(t, http.StatusOK, f.signIn(t, a).Code)
	require.Equal(t, http.StatusOK, f.moveSignIn(t, b).Code, "the first move is allowed")

	rec := f.signIn(t, a)
	require.Equal(t, http.StatusTooManyRequests, rec.Code, "a second move inside 5 minutes is refused up front")
	body := decodeBody(t, rec)
	assert.Equal(t, tenancy.CodeTakeoverCooldown, body["code"])
	assert.InDelta(t, 300, body["retryAfterSeconds"], 5)
	assert.Contains(t, body["message"], "Try again in 5 minutes")
	assert.Nil(t, a.takeover, "no ticket is handed out while waiting")
	assert.Equal(t, tenancy.SessionActive, f.status(t, b)["status"], "B keeps its session")
}

func TestTakeoverCooldown_RecheckedAtConfirmation_DB(t *testing.T) {
	f := newLoginFixture(t)
	f.setLimits(t, 5, 0)
	a, b, c := &browser{ua: uaChromeWindows}, &browser{ua: uaEdgeWindows}, &browser{ua: uaChromeWindows}
	require.Equal(t, http.StatusOK, f.signIn(t, a).Code)
	require.Equal(t, http.StatusConflict, f.signIn(t, b).Code)
	require.Equal(t, http.StatusConflict, f.signIn(t, c).Code, "two prompts open at once")

	require.Equal(t, http.StatusOK, f.takeover(t, b).Code)
	rec := f.takeover(t, c)
	assert.Equal(t, http.StatusTooManyRequests, rec.Code, "the second confirmation hits the cooldown")
	assert.Equal(t, tenancy.CodeTakeoverCooldown, decodeBody(t, rec)["code"])
}

func TestTakeoverDailyCap_RefusesAndAlertsAdmins_DB(t *testing.T) {
	sent := fakeNotify(t)
	f := newLoginFixture(t)
	f.setLimits(t, 0, 2)
	makeSuperAdmin(t, f.dsn, f.identity.ID)
	a, b := &browser{ua: uaChromeWindows}, &browser{ua: uaEdgeWindows}
	require.Equal(t, http.StatusOK, f.signIn(t, a).Code)
	require.Equal(t, http.StatusOK, f.moveSignIn(t, b).Code)
	require.Equal(t, http.StatusOK, f.moveSignIn(t, a).Code)

	rec := f.signIn(t, b)
	require.Equal(t, http.StatusTooManyRequests, rec.Code, "the third move in a day breaks a cap of 2")
	assert.Equal(t, tenancy.CodeTakeoverLimit, decodeBody(t, rec)["code"])
	require.Len(t, *sent, 1, "one alert to all of the customer's administrators")
	alert := (*sent)[0]
	assert.Equal(t, takeoverAlertEvent, alert["eventType"])
	assert.Contains(t, fmt.Sprint(alert["recipients"]), f.identity.ID, "super admins are the recipients")
	assert.Equal(t, f.identity.ID, alert["actorUserId"])
	assert.Contains(t, alert["idempotencyKey"], "takeover-limit:"+f.identity.ID+":", "one alert per user per day")
}

func TestTakeoverLimits_OffMeansUnlimited_DB(t *testing.T) {
	f := newLoginFixture(t)
	f.setLimits(t, 0, 0)
	a, b := &browser{ua: uaChromeWindows}, &browser{ua: uaEdgeWindows}
	require.Equal(t, http.StatusOK, f.signIn(t, a).Code)
	for i := range 6 {
		next := b
		if i%2 == 1 {
			next = a
		}
		require.Equal(t, http.StatusOK, f.moveSignIn(t, next).Code, "move %d", i+1)
	}
}

func TestSwitchRole_RefusesSessionlessStaffToken_DB(t *testing.T) {
	withTestJWTSecret(t)
	req := httptest.NewRequest(http.MethodPost, "/api/tenant/auth/switch-role", jsonBody(t, map[string]any{"roleId": ""}))
	req = req.WithContext(context.WithValue(req.Context(), middleware.UserContextKey,
		middleware.UserContextPayload{ID: "i", Email: "x@example.com", TenantID: "t"}))
	rec := httptest.NewRecorder()
	NewRBACOps().SwitchRole(rec, req)
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Equal(t, tenancy.CodeSessionRefreshRequired, decodeBody(t, rec)["code"])
}

func TestPasswordChange_OtherBrowserNotAskedAfterwards_DB(t *testing.T) {
	f := newLoginFixture(t)
	f.setLimits(t, 0, 0)
	a := &browser{ua: uaChromeWindows}
	require.Equal(t, http.StatusOK, f.signIn(t, a).Code)
	require.NoError(t, f.cp.RevokeAllRefreshTokens(context.Background(), f.identity.ID))

	b := &browser{ua: uaEdgeWindows}
	require.Equal(t, http.StatusOK, f.signIn(t, b).Code, "a session whose tokens were revoked is not 'in use'")
	assert.Equal(t, tenancy.SessionEnded, f.status(t, a)["status"])
}

func TestMFALogin_ThenTakeover_DB(t *testing.T) {
	sent := fakeNotify(t)
	f := newLoginFixture(t)
	f.setLimits(t, 0, 0)
	a := &browser{ua: uaChromeWindows}
	require.Equal(t, http.StatusOK, f.signIn(t, a).Code)
	require.NoError(t, f.cp.SetIdentityMFAEnabled(context.Background(), f.identity.ID, true))

	b := &browser{ua: uaEdgeWindows}
	rec := f.signIn(t, b)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	token, _ := decodeBody(t, rec)["challengeToken"].(string)
	require.NotEmpty(t, token, "the code step comes first")

	verified := f.verifyCode(t, token, lastEmailedCode(t, sent))
	require.Equal(t, http.StatusConflict, verified.Code, "then the active-session prompt")
	b.keep(t, verified)
	require.NotNil(t, b.takeover)
	require.Equal(t, http.StatusOK, f.takeover(t, b).Code, "confirming needs no second code")
	assert.Equal(t, tenancy.SessionReplacing, f.status(t, a)["status"])
}

// makeSuperAdmin gives the workspace user of identityID the super_admin role.
func makeSuperAdmin(t *testing.T, dsn, identityID string) {
	t.Helper()
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	defer pool.Close()
	roleID, err := authz.SeedSuperAdmin(ctx, pool)
	require.NoError(t, err)
	userID, err := userstore.UserIDByIdentityID(ctx, pool, identityID)
	require.NoError(t, err)
	require.NoError(t, authz.AssignRole(ctx, pool, userID, roleID))
}

// TestIdleSwitchesCountTowardTheCap_DB: switching whenever the other browser
// has been quiet past the 15-minute prompt window must not escape the limits.
func TestIdleSwitchesCountTowardTheCap_DB(t *testing.T) {
	f := newLoginFixture(t)
	f.setLimits(t, 0, 2)
	quiet := func(b *browser) {
		_, err := f.cp.Pool().Exec(context.Background(),
			`UPDATE auth_sessions SET last_seen_at = NOW() - INTERVAL '16 minutes' WHERE id = $1`, b.sid(t))
		require.NoError(t, err)
	}
	a, b := &browser{ua: uaChromeWindows}, &browser{ua: uaEdgeWindows}
	require.Equal(t, http.StatusOK, f.signIn(t, a).Code)
	quiet(a)
	require.Equal(t, http.StatusOK, f.signIn(t, b).Code, "no prompt: A was quiet")
	quiet(b)
	require.Equal(t, http.StatusOK, f.signIn(t, a).Code)
	quiet(a)
	rec := f.signIn(t, b)
	require.Equal(t, http.StatusTooManyRequests, rec.Code, "but each silent switch was a move")
	assert.Equal(t, tenancy.CodeTakeoverLimit, decodeBody(t, rec)["code"])
}

func TestRefresh_LegacyTokenRefusedWhileAnotherSessionIsInUse_DB(t *testing.T) {
	f := newLoginFixture(t)
	a := &browser{ua: uaChromeWindows}
	require.Equal(t, http.StatusOK, f.signIn(t, a).Code)
	raw, _, err := issueRefreshToken(context.Background(), f.cp, f.identity.ID, "", false)
	require.NoError(t, err)

	rec := f.refresh(t, raw)
	require.Equal(t, http.StatusUnauthorized, rec.Code, "an old token cannot push out the session in use")
	assert.Equal(t, tenancy.CodeSessionReplaced, decodeBody(t, rec)["code"])
	assert.Equal(t, tenancy.SessionActive, f.status(t, a)["status"])
}
