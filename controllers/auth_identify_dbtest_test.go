//go:build dbtest

package controllers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/tenancy"
)

func identifyResponse(t *testing.T, rr *httptest.ResponseRecorder) struct {
	Success  bool   `json:"success"`
	Method   string `json:"method"`
	Provider string `json:"provider"`
	TenantID string `json:"tenant_id"`
} {
	t.Helper()
	var resp struct {
		Success  bool   `json:"success"`
		Method   string `json:"method"`
		Provider string `json:"provider"`
		TenantID string `json:"tenant_id"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
	return resp
}

// TestTenantOps_Identify_UnregisteredDomain_DB covers a domain with no SSO
// config at all -- indistinguishable, by design, from every other
// non-SSO outcome below.
func TestTenantOps_Identify_UnregisteredDomain_DB(t *testing.T) {
	cp := newSAMLTestControlPlane(t)
	h := &TenantOps{CP: cp}

	body := fmt.Sprintf(`{"email":"user@no-such-domain-%d.example.com"}`, time.Now().UnixNano())
	req := httptest.NewRequest(http.MethodPost, "/api/auth/identify", strings.NewReader(body))
	rr := httptest.NewRecorder()

	assert.NotPanics(t, func() { h.Identify(rr, req) })
	require.Equal(t, http.StatusOK, rr.Code)
	resp := identifyResponse(t, rr)
	assert.True(t, resp.Success)
	assert.Equal(t, "password", resp.Method)
}

// TestTenantOps_Identify_DisabledConfig_DB covers a domain registered
// against a config that exists but is disabled -- DiscoverSSOByEmailDomain's
// query filters on enabled=TRUE, so this reads identically to no config at
// all.
func TestTenantOps_Identify_DisabledConfig_DB(t *testing.T) {
	cp := newSAMLTestControlPlane(t)
	ctx := context.Background()
	tenant := seedServableSAMLTestTenant(t, cp)

	cfg, err := cp.CreateSSOConfig(ctx, tenant.ID, tenancy.SSOConfigInput{
		Provider:     "cognito",
		Protocol:     "saml",
		MetadataURL:  "https://idp.example.com/metadata",
		IDPEntityID:  "https://idp.example.com/entity",
		SSOURL:       "https://idp.example.com/sso",
		SLOURL:       "https://idp.example.com/slo",
		NameIDFormat: "urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress",
		Enabled:      false,
	}, "", "encrypted-cert-pem", "fingerprint-disabled")
	require.NoError(t, err)
	domain := fmt.Sprintf("disabled-%d.example.com", time.Now().UnixNano())
	_, err = cp.CreateSSODomain(ctx, tenant.ID, cfg.ID, domain)
	require.NoError(t, err)

	h := &TenantOps{CP: cp}
	body := fmt.Sprintf(`{"email":"user@%s"}`, domain)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/identify", strings.NewReader(body))
	rr := httptest.NewRecorder()

	assert.NotPanics(t, func() { h.Identify(rr, req) })
	require.Equal(t, http.StatusOK, rr.Code)
	resp := identifyResponse(t, rr)
	assert.True(t, resp.Success)
	assert.Equal(t, "password", resp.Method)
}

// TestTenantOps_Identify_UnservableTenant_DB covers a domain registered
// against an enabled config whose tenant isn't Servable() (never
// provisioned) -- the tenant.Servable() guard mirrors Discover's, so a
// registered-but-unusable workspace still routes to the password field
// instead of dead-ending the user on an SSO redirect.
func TestTenantOps_Identify_UnservableTenant_DB(t *testing.T) {
	cp := newSAMLTestControlPlane(t)
	ctx := context.Background()
	tenantID := seedSAMLTestTenant(t, cp) // not provisioned -> not Servable()

	cfg, err := cp.CreateSSOConfig(ctx, tenantID, tenancy.SSOConfigInput{
		Provider:     "cognito",
		Protocol:     "saml",
		MetadataURL:  "https://idp.example.com/metadata",
		IDPEntityID:  "https://idp.example.com/entity",
		SSOURL:       "https://idp.example.com/sso",
		SLOURL:       "https://idp.example.com/slo",
		NameIDFormat: "urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress",
		Enabled:      true,
	}, "", "encrypted-cert-pem", "fingerprint-unservable")
	require.NoError(t, err)
	domain := fmt.Sprintf("unservable-%d.example.com", time.Now().UnixNano())
	_, err = cp.CreateSSODomain(ctx, tenantID, cfg.ID, domain)
	require.NoError(t, err)

	h := &TenantOps{CP: cp}
	body := fmt.Sprintf(`{"email":"user@%s"}`, domain)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/identify", strings.NewReader(body))
	rr := httptest.NewRecorder()

	assert.NotPanics(t, func() { h.Identify(rr, req) })
	require.Equal(t, http.StatusOK, rr.Code)
	resp := identifyResponse(t, rr)
	assert.True(t, resp.Success)
	assert.Equal(t, "password", resp.Method)
}

// TestTenantOps_Identify_MatchedSSO_DB covers the happy path: an enabled
// SAML config on a servable tenant, with the requested email's domain
// registered against it.
func TestTenantOps_Identify_MatchedSSO_DB(t *testing.T) {
	cp := newSAMLTestControlPlane(t)
	ctx := context.Background()
	tenant := seedServableSAMLTestTenant(t, cp)

	cfg, err := cp.CreateSSOConfig(ctx, tenant.ID, tenancy.SSOConfigInput{
		Provider:     "entra",
		Protocol:     "saml",
		MetadataURL:  "https://idp.example.com/metadata",
		IDPEntityID:  "https://idp.example.com/entity",
		SSOURL:       "https://idp.example.com/sso",
		SLOURL:       "https://idp.example.com/slo",
		NameIDFormat: "urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress",
		Enabled:      true,
	}, "", "encrypted-cert-pem", "fingerprint-matched")
	require.NoError(t, err)
	domain := fmt.Sprintf("matched-%d.example.com", time.Now().UnixNano())
	_, err = cp.CreateSSODomain(ctx, tenant.ID, cfg.ID, domain)
	require.NoError(t, err)

	h := &TenantOps{CP: cp}
	body := fmt.Sprintf(`{"email":"someone@%s"}`, domain)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/identify", strings.NewReader(body))
	rr := httptest.NewRecorder()

	assert.NotPanics(t, func() { h.Identify(rr, req) })
	require.Equal(t, http.StatusOK, rr.Code)
	resp := identifyResponse(t, rr)
	assert.True(t, resp.Success)
	assert.Equal(t, "sso", resp.Method)
	assert.Equal(t, "entra", resp.Provider)
	assert.Equal(t, tenant.ID, resp.TenantID)
}

// TestTenantOps_Identify_UppercaseDomain_DB confirms the email's domain is
// case-folded before lookup, matching a domain that was registered (and is
// always stored) in lowercase.
func TestTenantOps_Identify_UppercaseDomain_DB(t *testing.T) {
	cp := newSAMLTestControlPlane(t)
	ctx := context.Background()
	tenant := seedServableSAMLTestTenant(t, cp)

	cfg, err := cp.CreateSSOConfig(ctx, tenant.ID, tenancy.SSOConfigInput{
		Provider:     "cognito",
		Protocol:     "saml",
		MetadataURL:  "https://idp.example.com/metadata",
		IDPEntityID:  "https://idp.example.com/entity",
		SSOURL:       "https://idp.example.com/sso",
		SLOURL:       "https://idp.example.com/slo",
		NameIDFormat: "urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress",
		Enabled:      true,
	}, "", "encrypted-cert-pem", "fingerprint-case")
	require.NoError(t, err)
	domain := fmt.Sprintf("casefold-%d.example.com", time.Now().UnixNano())
	_, err = cp.CreateSSODomain(ctx, tenant.ID, cfg.ID, domain)
	require.NoError(t, err)

	h := &TenantOps{CP: cp}
	body := fmt.Sprintf(`{"email":"Someone@%s"}`, strings.ToUpper(domain))
	req := httptest.NewRequest(http.MethodPost, "/api/auth/identify", strings.NewReader(body))
	rr := httptest.NewRecorder()

	assert.NotPanics(t, func() { h.Identify(rr, req) })
	require.Equal(t, http.StatusOK, rr.Code)
	resp := identifyResponse(t, rr)
	assert.Equal(t, "sso", resp.Method)
	assert.Equal(t, "cognito", resp.Provider)
}
