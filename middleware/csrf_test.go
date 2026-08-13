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
	t.Cleanup(func() { config.AppConfig.CookieSameSite = origMode })

	tests := []struct {
		name         string
		sameSiteMode string
		method       string
		cookieValue  string // "" means no cookie set at all
		headerValue  string
		want         bool
	}{
		{"lax mode: no-op even without token", "lax", http.MethodPost, "", "", true},
		{"none mode: matching token passes", "none", http.MethodPost, "abc123", "abc123", true},
		{"none mode: missing cookie fails", "none", http.MethodPost, "", "abc123", false},
		{"none mode: missing header fails", "none", http.MethodPost, "abc123", "", false},
		{"none mode: mismatched token fails", "none", http.MethodPost, "abc123", "xyz789", false},
		{"none mode: empty cookie value fails", "none", http.MethodPost, "", "abc123", false},
		{"none mode: GET is exempt regardless of token", "none", http.MethodGet, "", "", true},
		{"none mode: PUT is protected", "none", http.MethodPut, "abc123", "abc123", true},
		{"none mode: PUT mismatch fails", "none", http.MethodPut, "abc123", "wrong", false},
		{"none mode: PATCH is protected", "none", http.MethodPatch, "abc123", "wrong", false},
		{"none mode: DELETE is protected", "none", http.MethodDelete, "abc123", "wrong", false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			config.AppConfig.CookieSameSite = tc.sameSiteMode

			r := httptest.NewRequest(tc.method, "/api/tenant/whatever", nil)
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
