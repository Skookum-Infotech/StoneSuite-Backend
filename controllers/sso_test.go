package controllers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestValidateSSORequest(t *testing.T) {
	base := ssoConfigRequest{
		Provider:     "entra",
		ClientID:     "client-123",
		ClientSecret: "shhh",
		Issuer:       "https://login.example.com",
		RedirectURI:  "https://app.example.com/callback",
		Enabled:      true,
	}

	tests := []struct {
		name          string
		mutate        func(r *ssoConfigRequest)
		requireSecret bool
		wantErr       bool
		wantProvider  string
		wantProtocol  string
	}{
		{name: "valid create", mutate: func(*ssoConfigRequest) {}, requireSecret: true, wantErr: false, wantProvider: "entra", wantProtocol: "oidc"},
		{name: "provider normalized", mutate: func(r *ssoConfigRequest) { r.Provider = "  OKTA " }, requireSecret: true, wantErr: false, wantProvider: "okta", wantProtocol: "oidc"},
		{name: "unknown provider", mutate: func(r *ssoConfigRequest) { r.Provider = "google" }, requireSecret: true, wantErr: true},
		{name: "empty provider", mutate: func(r *ssoConfigRequest) { r.Provider = "" }, requireSecret: true, wantErr: true},
		{name: "missing client id", mutate: func(r *ssoConfigRequest) { r.ClientID = "  " }, requireSecret: true, wantErr: true},
		{name: "missing secret on create", mutate: func(r *ssoConfigRequest) { r.ClientSecret = "" }, requireSecret: true, wantErr: true},
		{name: "missing secret allowed on update", mutate: func(r *ssoConfigRequest) { r.ClientSecret = "" }, requireSecret: false, wantErr: false, wantProvider: "entra", wantProtocol: "oidc"},
		{name: "bad issuer url", mutate: func(r *ssoConfigRequest) { r.Issuer = "not-a-url" }, requireSecret: true, wantErr: true},
		{name: "bad redirect scheme", mutate: func(r *ssoConfigRequest) { r.RedirectURI = "ftp://x/y" }, requireSecret: true, wantErr: true},
		{name: "blank optional urls ok", mutate: func(r *ssoConfigRequest) { r.Issuer = ""; r.RedirectURI = "" }, requireSecret: true, wantErr: false, wantProvider: "entra", wantProtocol: "oidc"},
		{name: "empty protocol defaults to oidc", mutate: func(r *ssoConfigRequest) { r.Protocol = "" }, requireSecret: true, wantErr: false, wantProvider: "entra", wantProtocol: "oidc"},
		{name: "unknown protocol rejected", mutate: func(r *ssoConfigRequest) { r.Protocol = "ldap" }, requireSecret: true, wantErr: true},
		{name: "valid SAML request", mutate: func(r *ssoConfigRequest) {
			r.Protocol = "saml"
			r.MetadataURL = "https://idp.example.com/metadata"
		}, requireSecret: true, wantErr: false, wantProvider: "entra", wantProtocol: "saml"},
		{name: "valid SAML request cognito provider", mutate: func(r *ssoConfigRequest) {
			r.Protocol = "SAML"
			r.Provider = " Cognito "
			r.MetadataURL = "https://idp.example.com/metadata"
		}, requireSecret: true, wantErr: false, wantProvider: "cognito", wantProtocol: "saml"},
		{name: "SAML with okta provider rejected", mutate: func(r *ssoConfigRequest) {
			r.Protocol = "saml"
			r.Provider = "okta"
			r.MetadataURL = "https://idp.example.com/metadata"
		}, requireSecret: true, wantErr: true},
		{name: "SAML missing metadata_url rejected", mutate: func(r *ssoConfigRequest) {
			r.Protocol = "saml"
			r.MetadataURL = ""
		}, requireSecret: true, wantErr: true},
		{name: "SAML http metadata_url rejected", mutate: func(r *ssoConfigRequest) {
			r.Protocol = "saml"
			r.MetadataURL = "http://idp.example.com/metadata"
		}, requireSecret: true, wantErr: true},
		{name: "SAML update requires metadata_url even though secret not required", mutate: func(r *ssoConfigRequest) {
			r.Protocol = "saml"
			r.MetadataURL = ""
			r.ClientSecret = ""
		}, requireSecret: false, wantErr: true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := base
			tc.mutate(&req)
			in, msg := validateSSORequest(req, tc.requireSecret)
			if tc.wantErr && msg == "" {
				t.Fatalf("expected validation error, got none")
			}
			if !tc.wantErr {
				if msg != "" {
					t.Fatalf("unexpected validation error: %s", msg)
				}
				if in.Provider != tc.wantProvider {
					t.Fatalf("provider = %q, want %q", in.Provider, tc.wantProvider)
				}
				if in.Protocol != tc.wantProtocol {
					t.Fatalf("protocol = %q, want %q", in.Protocol, tc.wantProtocol)
				}
			}
		})
	}
}

func TestIsHTTPURL(t *testing.T) {
	tests := []struct {
		in   string
		want bool
	}{
		{"https://example.com", true},
		{"http://example.com/path", true},
		{"https://host:8443/cb", true},
		{"ftp://example.com", false},
		{"example.com", false},
		{"", false},
		{"https://", false},
		{"//example.com", false},
	}
	for _, tc := range tests {
		if got := isHTTPURL(tc.in); got != tc.want {
			t.Errorf("isHTTPURL(%q) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

func TestIsValidDomain(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"simple valid domain", "contoso.com", true},
		{"subdomain valid", "mail.contoso.com", true},
		{"numeric labels valid", "123.456.com", true},
		{"single label with no dot is invalid", "contoso", false},
		{"empty is invalid", "", false},
		{"leading hyphen label is invalid", "-contoso.com", false},
		{"trailing hyphen label is invalid", "contoso-.com", false},
		{"double dot is invalid", "contoso..com", false},
		{"trailing dot is invalid", "contoso.com.", false},
		{"underscore is invalid", "cont_oso.com", false},
		{"uppercase is invalid (caller must normalize first)", "Contoso.com", false},
		{"leading/trailing space is invalid (caller must trim first)", " contoso.com ", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := isValidDomain(tc.in); got != tc.want {
				t.Errorf("isValidDomain(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

// TestIsValidDomain_LengthLimit exercises the len(s) <= 253 guard on a domain
// built entirely from otherwise-valid <=63-char labels, so the failure can
// only be attributed to the overall length check and not domainPattern.
func TestIsValidDomain_LengthLimit(t *testing.T) {
	label := strings.Repeat("a", 60) // well under the 63-char per-label cap
	tooLong := strings.Join([]string{label, label, label, label, label, "com"}, ".")
	if len(tooLong) <= 253 {
		t.Fatalf("test fixture too short to exercise the length guard: len=%d", len(tooLong))
	}
	if isValidDomain(tooLong) {
		t.Errorf("isValidDomain(%d-char domain) = true, want false", len(tooLong))
	}
}

// TestPublicEmailDomainBlocklist spot-checks that the largest public email
// providers are blocked (CreateDomain must reject them, see
// controllers/sso.go's doc comment on why) and that an ordinary business
// domain is not caught by the same check.
func TestPublicEmailDomainBlocklist(t *testing.T) {
	for _, d := range []string{
		"gmail.com", "googlemail.com",
		"outlook.com", "hotmail.com", "live.com", "msn.com",
		"yahoo.com", "ymail.com",
		"icloud.com", "me.com", "mac.com",
		"aol.com",
		"protonmail.com", "proton.me",
		"zoho.com",
	} {
		if !publicEmailDomainBlocklist[d] {
			t.Errorf("expected %q to be in publicEmailDomainBlocklist", d)
		}
	}
	if publicEmailDomainBlocklist["contoso.com"] {
		t.Error("contoso.com must not be in publicEmailDomainBlocklist")
	}
}

// TestSSOOps_ValidateDefaultRoleID_NoDBBranches covers the two branches of
// validateDefaultRoleID that must short-circuit before ever touching the
// tenant DB pool: an empty default_role_id (always valid, regardless of
// protocol) and a non-empty one on a non-saml config (rejected outright).
// The role-lookup/system-role/permission-check branches need a real tenant
// DB pool with authz's role tables seeded (authz.GetRole/authz.Check) and are
// exercised by a dbtest suite instead -- see the file-level note below.
func TestSSOOps_ValidateDefaultRoleID_NoDBBranches(t *testing.T) {
	h := &SSOOps{}
	tests := []struct {
		name          string
		protocol      string
		defaultRoleID string
		wantOK        bool
		wantRoleID    string
		wantStatus    int
	}{
		{"empty role id valid for oidc", ssoProtocolOIDC, "", true, "", 0},
		{"empty role id valid for saml", ssoProtocolSAML, "", true, "", 0},
		{"whitespace-only role id treated as empty", ssoProtocolSAML, "   ", true, "", 0},
		{"non-empty role id rejected for oidc protocol", ssoProtocolOIDC, "11111111-1111-1111-1111-111111111111", false, "", http.StatusBadRequest},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/tenant/sso-configs", nil)
			rr := httptest.NewRecorder()
			roleID, ok := h.validateDefaultRoleID(rr, req, nil, "identity-1", tc.protocol, tc.defaultRoleID)
			if ok != tc.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tc.wantOK)
			}
			if roleID != tc.wantRoleID {
				t.Fatalf("roleID = %q, want %q", roleID, tc.wantRoleID)
			}
			if !tc.wantOK && rr.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d", rr.Code, tc.wantStatus)
			}
		})
	}
}

// NOTE on coverage gap: validateDefaultRoleID's DB-dependent branches
// (nonexistent role, system role rejected, role:update permission denied)
// call authz.GetRole/authz.Check against the tenant DB pool and have no
// dbtest fixture in this package to seed roles/user_roles against -- the
// controllers package's only dbtest file (saml_dbtest_test.go) only stands up
// the control-plane pool via TEST_CP_DATABASE_URL, not a per-tenant pool.
// InviteUser's identical guard in controllers/user.go (the pattern this
// mirrors) has the same gap: there is no controllers/user_test.go at all.
// Flagged in the review rather than worked around here.
