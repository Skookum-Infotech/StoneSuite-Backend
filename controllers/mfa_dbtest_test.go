//go:build dbtest

package controllers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/mfa"
	"stonesuite-backend/middleware"
)

// emailedCodePattern finds the code in the shared template's code box.
var emailedCodePattern = regexp.MustCompile(`>(\d{6})</td>`)

// lastEmailedCode returns the code in the newest email the fake notify got.
func lastEmailedCode(t *testing.T, sent *[]map[string]any) string {
	t.Helper()
	require.NotEmpty(t, *sent, "no email was sent")
	html, _ := (*sent)[len(*sent)-1]["emailBodyHtml"].(string)
	m := emailedCodePattern.FindStringSubmatch(html)
	require.Len(t, m, 2, "no code in the email body")
	return m[1]
}

// decodeBody decodes a JSON response body into a map.
func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var body map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &body), rec.Body.String())
	return body
}

// hasAuthCookie reports whether the response set a session cookie.
func hasAuthCookie(rec *httptest.ResponseRecorder) bool {
	for _, c := range rec.Result().Cookies() {
		if c.Name == "auth_token" && c.Value != "" {
			return true
		}
	}
	return false
}

func (f *loginFixture) verifyCode(t *testing.T, challengeToken, code string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/auth/mfa/verify", jsonBody(t, map[string]any{
		"challengeToken": challengeToken, "code": code,
	}))
	rec := httptest.NewRecorder()
	f.h.MFAVerify(rec, req)
	return rec
}

// wrongCode returns a well-formed code that differs from code.
func wrongCode(code string) string {
	if code == "000000" {
		return "111111"
	}
	return "000000"
}

// startMFALogin turns MFA on for the fixture's user and signs in, returning
// the challenge token and the emailed code.
func startMFALogin(t *testing.T, f *loginFixture, sent *[]map[string]any) (string, string) {
	t.Helper()
	require.NoError(t, f.cp.SetIdentityMFAEnabled(context.Background(), f.identity.ID, true))
	rec := f.login(t, f.identity.Email, loginTestPassword)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decodeBody(t, rec)
	require.Equal(t, true, body["mfaRequired"])
	token, _ := body["challengeToken"].(string)
	require.NotEmpty(t, token)
	return token, lastEmailedCode(t, sent)
}

func TestMFALogin_CodeStepThenSession_DB(t *testing.T) {
	sent := fakeNotify(t)
	f := newLoginFixture(t)

	require.NoError(t, f.cp.SetIdentityMFAEnabled(context.Background(), f.identity.ID, true))
	rec := f.login(t, f.identity.Email, loginTestPassword)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decodeBody(t, rec)
	assert.Equal(t, true, body["mfaRequired"])
	assert.Equal(t, mfa.MaskEmail(f.identity.Email), body["maskedEmail"])
	assert.Nil(t, body["token"], "a correct password alone must not hand out a session")
	assert.False(t, hasAuthCookie(rec), "no session cookie before the code")
	require.Len(t, *sent, 1)
	assert.Equal(t, "identity.mfa_code", (*sent)[0]["eventType"])

	token, _ := body["challengeToken"].(string)
	code := lastEmailedCode(t, sent)

	wrong := f.verifyCode(t, token, wrongCode(code))
	assert.Equal(t, http.StatusBadRequest, wrong.Code, wrong.Body.String())
	wrongBody := decodeBody(t, wrong)
	assert.Equal(t, mfaCodeInvalid, wrongBody["code"])
	assert.EqualValues(t, mfa.MaxAttempts-1, wrongBody["attemptsLeft"])

	ok := f.verifyCode(t, token, code[:3]+" "+code[3:])
	require.Equal(t, http.StatusOK, ok.Code, ok.Body.String())
	okBody := decodeBody(t, ok)
	assert.NotEmpty(t, okBody["token"], "the right code finishes the login")
	assert.True(t, hasAuthCookie(ok))

	again := f.verifyCode(t, token, code)
	assert.Equal(t, http.StatusGone, again.Code, "a code is good for one session")
	assert.Equal(t, mfaChallengeExpired, decodeBody(t, again)["code"])
}

func TestMFALogin_OffIsUnchanged_DB(t *testing.T) {
	sent := fakeNotify(t)
	f := newLoginFixture(t)

	rec := f.login(t, f.identity.Email, loginTestPassword)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	body := decodeBody(t, rec)
	assert.NotEmpty(t, body["token"])
	assert.Nil(t, body["mfaRequired"])
	assert.True(t, hasAuthCookie(rec))
	assert.Empty(t, *sent, "no code email when MFA is off")
}

func TestMFALogin_WrongPasswordSendsNoCode_DB(t *testing.T) {
	sent := fakeNotify(t)
	f := newLoginFixture(t)
	require.NoError(t, f.cp.SetIdentityMFAEnabled(context.Background(), f.identity.ID, true))

	rec := f.login(t, f.identity.Email, "wrong-password")
	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.Empty(t, *sent)
}

func TestMFALogin_LockedOutAfterMaxAttempts_DB(t *testing.T) {
	sent := fakeNotify(t)
	f := newLoginFixture(t)
	token, code := startMFALogin(t, f, sent)

	for i := 1; i < mfa.MaxAttempts; i++ {
		rec := f.verifyCode(t, token, wrongCode(code))
		require.Equal(t, http.StatusBadRequest, rec.Code, "attempt %d", i)
	}
	last := f.verifyCode(t, token, wrongCode(code))
	assert.Equal(t, http.StatusGone, last.Code)
	assert.Equal(t, mfaChallengeLocked, decodeBody(t, last)["code"])

	afterLockout := f.verifyCode(t, token, code)
	assert.Equal(t, http.StatusGone, afterLockout.Code, "the right code no longer works once locked out")
	assert.False(t, hasAuthCookie(afterLockout))
}

func TestMFALogin_BadFormatAndUnknownToken_DB(t *testing.T) {
	sent := fakeNotify(t)
	f := newLoginFixture(t)
	token, _ := startMFALogin(t, f, sent)

	badFormat := f.verifyCode(t, token, "12ab56")
	assert.Equal(t, http.StatusBadRequest, badFormat.Code)
	assert.Equal(t, mfaCodeFormat, decodeBody(t, badFormat)["code"])

	unknown := f.verifyCode(t, "not-a-real-token", "123456")
	assert.Equal(t, http.StatusGone, unknown.Code)
}

func TestMFALogin_ResendCooldown_DB(t *testing.T) {
	sent := fakeNotify(t)
	f := newLoginFixture(t)
	token, _ := startMFALogin(t, f, sent)

	req := httptest.NewRequest(http.MethodPost, "/api/auth/mfa/resend", jsonBody(t, map[string]any{"challengeToken": token}))
	rec := httptest.NewRecorder()
	f.h.MFAResend(rec, req)
	assert.Equal(t, http.StatusTooManyRequests, rec.Code, rec.Body.String())
	body := decodeBody(t, rec)
	assert.Equal(t, mfaResendTooSoon, body["code"])
	assert.Greater(t, body["retryAfterSeconds"], float64(0))
	assert.Len(t, *sent, 1, "no second email inside the cooldown")
}

// settingsRequest builds a signed-in Account Settings request for identityID.
func settingsRequest(t *testing.T, method, path, identityID, kind string, body any) *http.Request {
	t.Helper()
	var req *http.Request
	if body == nil {
		req = httptest.NewRequest(method, path, nil)
	} else {
		req = httptest.NewRequest(method, path, jsonBody(t, body))
	}
	payload := middleware.UserContextPayload{ID: identityID, Kind: kind}
	return req.WithContext(context.WithValue(req.Context(), middleware.UserContextKey, payload))
}

func TestMFASettings_EnableThenDisable_DB(t *testing.T) {
	sent := fakeNotify(t)
	f := newLoginFixture(t)
	id := f.identity.ID

	status := httptest.NewRecorder()
	f.h.MFAStatus(status, settingsRequest(t, http.MethodGet, "/api/auth/mfa", id, "", nil))
	require.Equal(t, http.StatusOK, status.Code, status.Body.String())
	statusBody := decodeBody(t, status)
	assert.Equal(t, false, statusBody["enabled"])
	assert.Equal(t, true, statusBody["available"])

	start := httptest.NewRecorder()
	f.h.MFAEnableStart(start, settingsRequest(t, http.MethodPost, "/api/auth/mfa/enable/start", id, "", nil))
	require.Equal(t, http.StatusOK, start.Code, start.Body.String())
	require.Len(t, *sent, 1)
	assert.Equal(t, "identity.mfa_setup_code", (*sent)[0]["eventType"])
	assert.Nil(t, decodeBody(t, start)["challengeToken"], "the enable flow never hands out a token")
	code := lastEmailedCode(t, sent)

	wrong := httptest.NewRecorder()
	f.h.MFAEnableConfirm(wrong, settingsRequest(t, http.MethodPost, "/api/auth/mfa/enable/confirm", id, "", map[string]any{"code": wrongCode(code)}))
	assert.Equal(t, http.StatusBadRequest, wrong.Code)
	identity, err := f.cp.IdentityByID(context.Background(), id)
	require.NoError(t, err)
	assert.False(t, identity.MFAEnabled, "a wrong code must not turn it on")

	confirm := httptest.NewRecorder()
	f.h.MFAEnableConfirm(confirm, settingsRequest(t, http.MethodPost, "/api/auth/mfa/enable/confirm", id, "", map[string]any{"code": code}))
	require.Equal(t, http.StatusOK, confirm.Code, confirm.Body.String())
	identity, err = f.cp.IdentityByID(context.Background(), id)
	require.NoError(t, err)
	assert.True(t, identity.MFAEnabled)
	assert.Equal(t, "identity.mfa_enabled", (*sent)[len(*sent)-1]["eventType"], "a security notice follows")

	badPassword := httptest.NewRecorder()
	f.h.MFADisable(badPassword, settingsRequest(t, http.MethodPost, "/api/auth/mfa/disable", id, "", map[string]any{"currentPassword": "nope"}))
	assert.Equal(t, http.StatusBadRequest, badPassword.Code)
	assert.Equal(t, invalidPassword, decodeBody(t, badPassword)["code"])

	disable := httptest.NewRecorder()
	f.h.MFADisable(disable, settingsRequest(t, http.MethodPost, "/api/auth/mfa/disable", id, "", map[string]any{"currentPassword": loginTestPassword}))
	require.Equal(t, http.StatusOK, disable.Code, disable.Body.String())
	identity, err = f.cp.IdentityByID(context.Background(), id)
	require.NoError(t, err)
	assert.False(t, identity.MFAEnabled)
	assert.Equal(t, "identity.mfa_disabled", (*sent)[len(*sent)-1]["eventType"])
}

func TestMFASettings_RefusesPortalAndSSO_DB(t *testing.T) {
	fakeNotify(t)
	f := newLoginFixture(t)

	portal := httptest.NewRecorder()
	f.h.MFAEnableStart(portal, settingsRequest(t, http.MethodPost, "/api/auth/mfa/enable/start", f.identity.ID, middleware.KindPortal, nil))
	assert.Equal(t, http.StatusForbidden, portal.Code)

	sso, err := f.cp.CreateSSOIdentity(context.Background(), f.tenant.ID,
		"sso-"+f.identity.Email, "SSO User", "entra", "subject-"+f.identity.ID)
	require.NoError(t, err)
	status := httptest.NewRecorder()
	f.h.MFAStatus(status, settingsRequest(t, http.MethodGet, "/api/auth/mfa", sso.ID, "", nil))
	require.Equal(t, http.StatusOK, status.Code, status.Body.String())
	assert.Equal(t, false, decodeBody(t, status)["available"])

	start := httptest.NewRecorder()
	f.h.MFAEnableStart(start, settingsRequest(t, http.MethodPost, "/api/auth/mfa/enable/start", sso.ID, "", nil))
	assert.Equal(t, http.StatusBadRequest, start.Code)
	assert.Equal(t, mfaUnavailable, decodeBody(t, start)["code"])
}
