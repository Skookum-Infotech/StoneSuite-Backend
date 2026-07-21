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
	}{
		{name: "valid create", mutate: func(*ssoConfigRequest) {}, requireSecret: true, wantErr: false, wantProvider: "entra"},
		{name: "provider normalized", mutate: func(r *ssoConfigRequest) { r.Provider = "  OKTA " }, requireSecret: true, wantErr: false, wantProvider: "okta"},
		{name: "unknown provider", mutate: func(r *ssoConfigRequest) { r.Provider = "google" }, requireSecret: true, wantErr: true},
		{name: "empty provider", mutate: func(r *ssoConfigRequest) { r.Provider = "" }, requireSecret: true, wantErr: true},
		{name: "missing client id", mutate: func(r *ssoConfigRequest) { r.ClientID = "  " }, requireSecret: true, wantErr: true},
		{name: "missing secret on create", mutate: func(r *ssoConfigRequest) { r.ClientSecret = "" }, requireSecret: true, wantErr: true},
		{name: "missing secret allowed on update", mutate: func(r *ssoConfigRequest) { r.ClientSecret = "" }, requireSecret: false, wantErr: false, wantProvider: "entra"},
		{name: "bad issuer url", mutate: func(r *ssoConfigRequest) { r.Issuer = "not-a-url" }, requireSecret: true, wantErr: true},
		{name: "bad redirect scheme", mutate: func(r *ssoConfigRequest) { r.RedirectURI = "ftp://x/y" }, requireSecret: true, wantErr: true},
		{name: "blank optional urls ok", mutate: func(r *ssoConfigRequest) { r.Issuer = ""; r.RedirectURI = "" }, requireSecret: true, wantErr: false, wantProvider: "entra"},
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
