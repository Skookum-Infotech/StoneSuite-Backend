//go:build dbtest

package controllers

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/config"
	"stonesuite-backend/middleware"
	"stonesuite-backend/secret"
	"stonesuite-backend/tenancy"
)

// newSAMLTestControlPlane connects to the control-plane-schema test database
// used by this package's dbtest suite, mirroring tenancy's own
// newCPTestControlPlane helper (tenancy/identity_test.go) via the exported
// constructor -- ControlPlane's pool field is unexported outside package
// tenancy. Skips cleanly when TEST_CP_DATABASE_URL is unset.
func newSAMLTestControlPlane(t *testing.T) *tenancy.ControlPlane {
	t.Helper()
	dsn := os.Getenv("TEST_CP_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_CP_DATABASE_URL not set; skipping DB-backed test")
	}
	cp, err := tenancy.NewControlPlane(context.Background(), dsn)
	require.NoError(t, err)
	t.Cleanup(cp.Close)
	return cp
}

// seedSAMLTestTenant inserts a minimal tenant row, for tests that only need
// a valid tenants.id to satisfy identities'/saml_login_codes' FK.
func seedSAMLTestTenant(t *testing.T, cp *tenancy.ControlPlane) string {
	t.Helper()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	tenant, err := cp.CreateTenant(context.Background(), "saml-test-tenant-"+suffix, "SAML Test Tenant", false)
	require.NoError(t, err)
	return tenant.ID
}

// withTestJWTSecret sets a JWT signing secret for the duration of the test,
// mirroring the save/restore pattern in middleware/middleware_test.go.
func withTestJWTSecret(t *testing.T) {
	t.Helper()
	orig := config.AppConfig.JWTSecret
	config.AppConfig.JWTSecret = "test-secret"
	t.Cleanup(func() { config.AppConfig.JWTSecret = orig })
}

func TestSAMLAuthOps_Exchange_InvalidCode_DB(t *testing.T) {
	cp := newSAMLTestControlPlane(t)
	h := NewSAMLAuthOps(cp, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/auth/saml/exchange", strings.NewReader(`{"code":"no-such-code"}`))
	rr := httptest.NewRecorder()
	h.Exchange(rr, req)

	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

func TestSAMLAuthOps_Exchange_HappyPath_DB(t *testing.T) {
	withTestJWTSecret(t)
	cp := newSAMLTestControlPlane(t)
	ctx := context.Background()
	tenantID := seedSAMLTestTenant(t, cp)
	email := fmt.Sprintf("exchange-%d@example.com", time.Now().UnixNano())
	identity, err := cp.CreateIdentity(ctx, tenantID, email, "", "Exchange User", true)
	require.NoError(t, err)

	code := fmt.Sprintf("exchange-code-%d", time.Now().UnixNano())
	require.NoError(t, cp.CreateSAMLLoginCode(ctx, code, identity.ID, tenantID, time.Minute))

	h := NewSAMLAuthOps(cp, nil, nil)
	reqBody := fmt.Sprintf(`{"code":%q}`, code)

	req := httptest.NewRequest(http.MethodPost, "/api/auth/saml/exchange", strings.NewReader(reqBody))
	rr := httptest.NewRecorder()
	h.Exchange(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)
	var resp struct {
		Success bool   `json:"success"`
		Token   string `json:"token"`
		User    struct {
			ID       string `json:"id"`
			Email    string `json:"email"`
			TenantID string `json:"tenantId"`
		} `json:"user"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.True(t, resp.Success)
	assert.NotEmpty(t, resp.Token)
	assert.Equal(t, identity.ID, resp.User.ID)
	assert.Equal(t, email, resp.User.Email)
	assert.Equal(t, tenantID, resp.User.TenantID)

	var sawAuthCookie bool
	for _, c := range rr.Result().Cookies() {
		if c.Name == "auth_token" && c.Value != "" {
			sawAuthCookie = true
		}
	}
	assert.True(t, sawAuthCookie, "expected auth_token cookie to be set")

	// The code is single-use -- a second exchange attempt must fail.
	req2 := httptest.NewRequest(http.MethodPost, "/api/auth/saml/exchange", strings.NewReader(reqBody))
	rr2 := httptest.NewRecorder()
	h.Exchange(rr2, req2)
	assert.Equal(t, http.StatusBadRequest, rr2.Code)
}

// seedServableSAMLTestTenant inserts a tenant and marks it provisioned/active
// so tenancy.Tenant.Servable() returns true. seedSAMLTestTenant alone leaves
// a tenant in the default 'invited' status, which Initiate's
// "This workspace is not available for sign-in." (403) gate would reject
// before ever reaching the SSO-config lookup this test exercises.
func seedServableSAMLTestTenant(t *testing.T, cp *tenancy.ControlPlane) *tenancy.Tenant {
	t.Helper()
	ctx := context.Background()
	tenantID := seedSAMLTestTenant(t, cp)
	require.NoError(t, cp.SetTenantProvisioned(ctx, tenantID, "tenant_saml_test_db", "plain:unused", 1))
	tenant, err := cp.TenantByID(ctx, tenantID)
	require.NoError(t, err)
	require.True(t, tenant.Servable(), "seeded tenant must be Servable() for an Initiate happy-path test")
	return tenant
}

// TestSAMLAuthOps_Initiate_Success_DB covers Initiate's full DB-backed happy
// path: tenant resolution, loading the enabled SAML config, decrypting the
// stored certificate, building the AuthnRequest, and persisting request
// state (CreateSAMLRequestState) -- reaching the 302 redirect at all proves
// that last write succeeded, since a failure there returns 500 instead
// (saml_auth.go). Only the unknown-tenant 404 branch had DB coverage before.
func TestSAMLAuthOps_Initiate_Success_DB(t *testing.T) {
	cp := newSAMLTestControlPlane(t)
	cipher := testSecretCipher(t)
	ctx := context.Background()
	tenant := seedServableSAMLTestTenant(t, cp)

	certPEM := selfSignedTestCertPEM(t)
	encCert, err := cipher.Encrypt(certPEM)
	require.NoError(t, err)
	_, err = cp.CreateSSOConfig(ctx, tenant.ID, tenancy.SSOConfigInput{
		Provider:     "cognito",
		Protocol:     "saml",
		MetadataURL:  "https://idp.example.com/metadata",
		IDPEntityID:  "https://idp.example.com/entity",
		SSOURL:       "https://idp.example.com/sso",
		SLOURL:       "https://idp.example.com/slo",
		NameIDFormat: "urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress",
		Enabled:      true,
	}, "", encCert, "fingerprint-initiate")
	require.NoError(t, err)

	h := NewSAMLAuthOps(cp, nil, cipher)
	req := httptest.NewRequest(http.MethodGet,
		"/api/auth/saml/cognito/initiate?tenant_id="+tenant.ID+"&return_to=%2Fdashboard", nil)
	req.SetPathValue("provider", "cognito")
	rr := httptest.NewRecorder()

	assert.NotPanics(t, func() { h.Initiate(rr, req) })
	require.Equal(t, http.StatusFound, rr.Code)

	loc := rr.Header().Get("Location")
	assert.True(t, strings.HasPrefix(loc, "https://idp.example.com/sso"), "Location = %q", loc)
	parsed, err := url.Parse(loc)
	require.NoError(t, err)
	assert.NotEmpty(t, parsed.Query().Get("SAMLRequest"))
	assert.Equal(t, "/dashboard", parsed.Query().Get("RelayState"))
}

// TestSAMLAuthOps_Initiate_NoSSOConfig_DB covers the 404 branch reached when
// a servable tenant has no enabled protocol=saml config for the requested
// provider -- distinct from the pre-existing unknown-tenant 404 test below,
// which never gets far enough to look up a config at all.
func TestSAMLAuthOps_Initiate_NoSSOConfig_DB(t *testing.T) {
	cp := newSAMLTestControlPlane(t)
	tenant := seedServableSAMLTestTenant(t, cp)
	h := NewSAMLAuthOps(cp, nil, nil)

	req := httptest.NewRequest(http.MethodGet,
		"/api/auth/saml/cognito/initiate?tenant_id="+tenant.ID, nil)
	req.SetPathValue("provider", "cognito")
	rr := httptest.NewRecorder()

	assert.NotPanics(t, func() { h.Initiate(rr, req) })
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

func TestSAMLAuthOps_Initiate_UnknownTenant_DB(t *testing.T) {
	cp := newSAMLTestControlPlane(t)
	h := NewSAMLAuthOps(cp, nil, nil)

	req := httptest.NewRequest(http.MethodGet,
		"/api/auth/saml/cognito/initiate?tenant_id=00000000-0000-0000-0000-000000000000", nil)
	req.SetPathValue("provider", "cognito")
	rr := httptest.NewRecorder()
	h.Initiate(rr, req)

	assert.Equal(t, http.StatusNotFound, rr.Code)
}

// unverifiedSAMLResponseXML builds the smallest envelope
// gosaml2.DecodeUnverifiedBaseResponse will successfully parse: a bare,
// entirely unsigned <Response> carrying only InResponseTo. It exercises
// ConsumeSAMLRequestState's not-found path without needing a real IdP-signed
// assertion -- signature validation happens strictly later, against a cert
// this test never reaches.
func unverifiedSAMLResponseXML(inResponseTo string) string {
	raw := `<Response xmlns="urn:oasis:names:tc:SAML:2.0:protocol" ID="_resp1" InResponseTo="` +
		inResponseTo + `" Version="2.0"></Response>`
	return base64.StdEncoding.EncodeToString([]byte(raw))
}

func TestSAMLAuthOps_ACS_UnconsumableRequestState_DB(t *testing.T) {
	cp := newSAMLTestControlPlane(t)
	h := NewSAMLAuthOps(cp, nil, nil)

	samlResponse := unverifiedSAMLResponseXML("never-issued-request-id-" + fmt.Sprintf("%d", time.Now().UnixNano()))
	form := url.Values{"SAMLResponse": {samlResponse}}
	req := httptest.NewRequest(http.MethodPost, "/api/auth/saml/cognito/acs", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("provider", "cognito")
	rr := httptest.NewRecorder()

	assert.NotPanics(t, func() { h.ACS(rr, req) })
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Equal(t, samlErrorPageHTML, rr.Body.String())
}

func TestSAMLAuthOps_ACS_ExpiredRequestState_DB(t *testing.T) {
	cp := newSAMLTestControlPlane(t)
	h := NewSAMLAuthOps(cp, nil, nil)
	tenantID := seedSAMLTestTenant(t, cp)

	requestID := fmt.Sprintf("expired-req-%d", time.Now().UnixNano())
	require.NoError(t, cp.CreateSAMLRequestState(context.Background(), requestID, tenantID, "cognito", -time.Minute))

	samlResponse := unverifiedSAMLResponseXML(requestID)
	form := url.Values{"SAMLResponse": {samlResponse}}
	req := httptest.NewRequest(http.MethodPost, "/api/auth/saml/cognito/acs", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("provider", "cognito")
	rr := httptest.NewRecorder()

	assert.NotPanics(t, func() { h.ACS(rr, req) })
	assert.Equal(t, http.StatusBadRequest, rr.Code)
	assert.Equal(t, samlErrorPageHTML, rr.Body.String())
}

// contextWithSAMLTestUser attaches a middleware.UserContextPayload to ctx,
// mirroring what middleware.RequireAuth does after verifying a JWT. Logout is
// exercised here by calling the handler directly (bypassing the HTTP
// middleware chain, matching this file's other handler-direct DB tests), so
// the auth context has to be constructed by hand.
func contextWithSAMLTestUser(ctx context.Context, identityID, tenantID string) context.Context {
	return context.WithValue(ctx, middleware.UserContextKey, middleware.UserContextPayload{ID: identityID, TenantID: tenantID})
}

// selfSignedTestCertPEM generates a minimal, real (parseable) self-signed
// X.509 certificate for tests that only need saml.BuildLogoutRequestURL's PEM
// parsing to succeed. SP-initiated LogoutRequests are never signature-checked
// against this certificate by us -- only inbound Responses are (ACS) -- so
// the certificate's own trust chain is irrelevant to what these tests verify.
func selfSignedTestCertPEM(t *testing.T) string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "saml-test-idp"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	require.NoError(t, err)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

// testSecretCipher builds a secret.Cipher from a fixed, in-memory test key.
// These tests only round-trip through it (encrypt when seeding, decrypt
// inside the handler under test), so a fixed all-zero key is fine -- nothing
// persists beyond the test.
func testSecretCipher(t *testing.T) *secret.Cipher {
	t.Helper()
	c, err := secret.New(base64.StdEncoding.EncodeToString(make([]byte, 32)))
	require.NoError(t, err)
	return c
}

// assertAuthCookiesCleared fails the test unless a Set-Cookie for auth_token
// with an expiring Max-Age is present -- Logout's documented behavior is to
// clear the local session unconditionally, before anything SLO-related is
// even attempted (saml_logout.go).
func assertAuthCookiesCleared(t *testing.T, rr *httptest.ResponseRecorder) {
	t.Helper()
	for _, c := range rr.Result().Cookies() {
		if c.Name == "auth_token" && c.MaxAge < 0 {
			return
		}
	}
	t.Fatal("expected a cleared (Max-Age<0) auth_token cookie in the response")
}

// TestSAMLAuthOps_Logout_NotEstablishedViaSAML_DB covers Logout's DB-backed
// 400 branch: an authenticated caller whose identity has no SAML link at all
// (password-only identity) hitting a SAML provider's logout route.
func TestSAMLAuthOps_Logout_NotEstablishedViaSAML_DB(t *testing.T) {
	cp := newSAMLTestControlPlane(t)
	ctx := context.Background()
	tenantID := seedSAMLTestTenant(t, cp)
	email := fmt.Sprintf("logout-pw-%d@example.com", time.Now().UnixNano())
	identity, err := cp.CreateIdentity(ctx, tenantID, email, "hashed-pw", "Password User", true)
	require.NoError(t, err)

	h := NewSAMLAuthOps(cp, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/saml/cognito/logout", nil)
	req.SetPathValue("provider", "cognito")
	req = req.WithContext(contextWithSAMLTestUser(req.Context(), identity.ID, tenantID))
	rr := httptest.NewRecorder()

	assert.NotPanics(t, func() { h.Logout(rr, req) })
	assert.Equal(t, http.StatusBadRequest, rr.Code)
}

// TestSAMLAuthOps_Logout_NoSSOConfig_DB covers the slo_available=false branch
// reached when the caller's identity is SAML-linked but no SSO config row
// exists at all for tenant+provider (GetSSOConfigForAuth returns
// ErrSSOConfigNotFound) -- e.g. the config was deleted after the identity
// linked to it. Local logout must still succeed.
func TestSAMLAuthOps_Logout_NoSSOConfig_DB(t *testing.T) {
	cp := newSAMLTestControlPlane(t)
	ctx := context.Background()
	tenantID := seedSAMLTestTenant(t, cp)
	email := fmt.Sprintf("logout-nocfg-%d@example.com", time.Now().UnixNano())
	identity, err := cp.CreateSSOIdentity(ctx, tenantID, email, "SAML User", "cognito", "subject-"+tenantID)
	require.NoError(t, err)

	h := NewSAMLAuthOps(cp, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/saml/cognito/logout", nil)
	req.SetPathValue("provider", "cognito")
	req = req.WithContext(contextWithSAMLTestUser(req.Context(), identity.ID, tenantID))
	rr := httptest.NewRecorder()

	assert.NotPanics(t, func() { h.Logout(rr, req) })
	require.Equal(t, http.StatusOK, rr.Code)

	var resp struct {
		Success      bool `json:"success"`
		SLOAvailable bool `json:"slo_available"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.True(t, resp.Success)
	assert.False(t, resp.SLOAvailable)
	assertAuthCookiesCleared(t, rr)
}

// TestSAMLAuthOps_Logout_ConfigWithNoSLOURL_DB covers the slo_available=false
// branch reached when an enabled protocol=saml config exists but the IdP
// never advertised a SingleLogoutService (cfg.SLOURL == "") -- the AWS
// Cognito shape in saml/metadata_test.go's cognitoMetadataTemplate is exactly
// this case in practice.
func TestSAMLAuthOps_Logout_ConfigWithNoSLOURL_DB(t *testing.T) {
	cp := newSAMLTestControlPlane(t)
	ctx := context.Background()
	tenantID := seedSAMLTestTenant(t, cp)
	email := fmt.Sprintf("logout-noslo-%d@example.com", time.Now().UnixNano())
	identity, err := cp.CreateSSOIdentity(ctx, tenantID, email, "SAML User", "cognito", "subject-"+tenantID)
	require.NoError(t, err)

	_, err = cp.CreateSSOConfig(ctx, tenantID, tenancy.SSOConfigInput{
		Provider:     "cognito",
		Protocol:     "saml",
		MetadataURL:  "https://idp.example.com/metadata",
		IDPEntityID:  "https://idp.example.com/entity",
		SSOURL:       "https://idp.example.com/sso",
		SLOURL:       "", // no SLO endpoint advertised
		NameIDFormat: "urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress",
		Enabled:      true,
	}, "", "encrypted-cert-pem", "fingerprint-noslo")
	require.NoError(t, err)

	h := NewSAMLAuthOps(cp, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/saml/cognito/logout", nil)
	req.SetPathValue("provider", "cognito")
	req = req.WithContext(contextWithSAMLTestUser(req.Context(), identity.ID, tenantID))
	rr := httptest.NewRecorder()

	assert.NotPanics(t, func() { h.Logout(rr, req) })
	require.Equal(t, http.StatusOK, rr.Code)

	var resp struct {
		Success      bool `json:"success"`
		SLOAvailable bool `json:"slo_available"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.True(t, resp.Success)
	assert.False(t, resp.SLOAvailable)
	assertAuthCookiesCleared(t, rr)
}

// TestSAMLAuthOps_Logout_SLOAvailable_DB covers Logout's full happy path: an
// enabled protocol=saml config with an SLO URL and a decryptable certificate
// produces slo_available=true and a real logout_url built by
// saml.BuildLogoutRequestURL, while still clearing the local session.
func TestSAMLAuthOps_Logout_SLOAvailable_DB(t *testing.T) {
	cp := newSAMLTestControlPlane(t)
	cipher := testSecretCipher(t)
	ctx := context.Background()
	tenantID := seedSAMLTestTenant(t, cp)
	email := fmt.Sprintf("logout-slo-%d@example.com", time.Now().UnixNano())
	subject := "subject-" + tenantID
	identity, err := cp.CreateSSOIdentity(ctx, tenantID, email, "SAML User", "cognito", subject)
	require.NoError(t, err)
	// LinkSSOIdentity is what a real repeat login uses to populate
	// sso_session_index (CreateSSOIdentity, the JIT path, never sets it --
	// see the documented first-login-then-immediate-logout limitation).
	// Simulate a repeat login here so this test also confirms Logout reads
	// SSOSessionIndex back correctly into the LogoutRequest.
	require.NoError(t, cp.LinkSSOIdentity(ctx, identity.ID, "cognito", subject, "session-idx-1"))

	certPEM := selfSignedTestCertPEM(t)
	encCert, err := cipher.Encrypt(certPEM)
	require.NoError(t, err)
	_, err = cp.CreateSSOConfig(ctx, tenantID, tenancy.SSOConfigInput{
		Provider:     "cognito",
		Protocol:     "saml",
		MetadataURL:  "https://idp.example.com/metadata",
		IDPEntityID:  "https://idp.example.com/entity",
		SSOURL:       "https://idp.example.com/sso",
		SLOURL:       "https://idp.example.com/slo",
		NameIDFormat: "urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress",
		Enabled:      true,
	}, "", encCert, "fingerprint-slo")
	require.NoError(t, err)

	h := NewSAMLAuthOps(cp, nil, cipher)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/saml/cognito/logout", nil)
	req.SetPathValue("provider", "cognito")
	req = req.WithContext(contextWithSAMLTestUser(req.Context(), identity.ID, tenantID))
	rr := httptest.NewRecorder()

	assert.NotPanics(t, func() { h.Logout(rr, req) })
	require.Equal(t, http.StatusOK, rr.Code)

	var resp struct {
		Success      bool   `json:"success"`
		SLOAvailable bool   `json:"slo_available"`
		LogoutURL    string `json:"logout_url"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	assert.True(t, resp.Success)
	assert.True(t, resp.SLOAvailable)
	assert.True(t, strings.HasPrefix(resp.LogoutURL, "https://idp.example.com/slo"), "logout_url = %q", resp.LogoutURL)
	assert.Contains(t, resp.LogoutURL, "SAMLRequest=")
	assertAuthCookiesCleared(t, rr)
}
