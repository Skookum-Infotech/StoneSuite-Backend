package controllers

import "testing"

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
