package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"stonesuite-backend/config"
)

func TestCsrfValid(t *testing.T) {
	origMode := config.AppConfig.CookieSameSite
	origCors := config.AppConfig.CorsOrigin
	t.Cleanup(func() {
		config.AppConfig.CookieSameSite = origMode
		config.AppConfig.CorsOrigin = origCors
	})
	config.AppConfig.CorsOrigin = "https://dev.stonesuite.app, https://dev-stonesuite-webui.pages.dev"

	tests := []struct {
		name         string
		sameSiteMode string
		method       string
		origin       string // "" means no Origin header
		cookieValue  string // "" means no cookie set at all
		headerValue  string
		want         bool
	}{
		{"lax mode: no-op even without token", "lax", http.MethodPost, "", "", "", true},
		{"lax mode: no-op even with a foreign origin", "lax", http.MethodPost, "https://evil.example", "", "", true},

		// Double-submit fallback (no usable Origin).
		{"none mode: matching token passes", "none", http.MethodPost, "", "abc123", "abc123", true},
		{"none mode: missing cookie fails", "none", http.MethodPost, "", "", "abc123", false},
		{"none mode: missing header fails", "none", http.MethodPost, "", "abc123", "", false},
		{"none mode: mismatched token fails", "none", http.MethodPost, "", "abc123", "xyz789", false},
		{"none mode: empty cookie value fails", "none", http.MethodPost, "", "", "abc123", false},
		{"none mode: no signal at all fails", "none", http.MethodPost, "", "", "", false},

		// Origin allowlist (primary check) — passes with no csrf_token cookie at all.
		{"none mode: allowlisted origin passes without any token", "none", http.MethodPost, "https://dev.stonesuite.app", "", "", true},
		{"none mode: second allowlisted origin passes", "none", http.MethodPost, "https://dev-stonesuite-webui.pages.dev", "", "", true},
		{"none mode: foreign origin falls through to (absent) token and fails", "none", http.MethodPost, "https://evil.example", "", "", false},
		{"none mode: foreign origin cannot override a good token", "none", http.MethodPost, "https://evil.example", "abc123", "abc123", true},
		{"none mode: allowlisted origin passes even with a mismatched token", "none", http.MethodPost, "https://dev.stonesuite.app", "abc123", "xyz789", true},

		// Method exemptions.
		{"none mode: GET is exempt regardless of token or origin", "none", http.MethodGet, "https://evil.example", "", "", true},
		{"none mode: PUT is protected", "none", http.MethodPut, "", "abc123", "abc123", true},
		{"none mode: PUT mismatch fails", "none", http.MethodPut, "", "abc123", "wrong", false},
		{"none mode: PATCH is protected", "none", http.MethodPatch, "", "abc123", "wrong", false},
		{"none mode: DELETE is protected", "none", http.MethodDelete, "", "abc123", "wrong", false},
		{"none mode: DELETE from allowlisted origin passes", "none", http.MethodDelete, "https://dev.stonesuite.app", "", "", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			config.AppConfig.CookieSameSite = tc.sameSiteMode

			r := httptest.NewRequest(tc.method, "/api/tenant/whatever", nil)
			if tc.origin != "" {
				r.Header.Set("Origin", tc.origin)
			}
			if tc.cookieValue != "" {
				r.AddCookie(&http.Cookie{Name: "csrf_token", Value: tc.cookieValue})
			}
			if tc.headerValue != "" {
				r.Header.Set("X-CSRF-Token", tc.headerValue)
			}

			assert.Equal(t, tc.want, csrfValid(r))
		})
	}
}

func TestOriginAllowed(t *testing.T) {
	orig := config.AppConfig.CorsOrigin
	t.Cleanup(func() { config.AppConfig.CorsOrigin = orig })
	config.AppConfig.CorsOrigin = "https://dev.stonesuite.app, https://dev-stonesuite-webui.pages.dev"

	tests := []struct {
		origin string
		want   bool
	}{
		{"https://dev.stonesuite.app", true},
		{"https://dev-stonesuite-webui.pages.dev", true},
		{"https://dev.stonesuite.app.evil.com", false},
		{"http://dev.stonesuite.app", false}, // scheme mismatch
		{"https://evil.example", false},
		{"", false},
		{"null", false},
	}
	for _, tc := range tests {
		t.Run(tc.origin, func(t *testing.T) {
			assert.Equal(t, tc.want, originAllowed(tc.origin))
		})
	}
}
