package middleware

import (
	"crypto/subtle"
	"net/http"

	"stonesuite-backend/config"
)

// csrfProtectedMethods are the HTTP methods CSRF validation applies to.
// GET/HEAD/OPTIONS don't change state and are exempt.
var csrfProtectedMethods = map[string]bool{
	http.MethodPost:   true,
	http.MethodPut:    true,
	http.MethodPatch:  true,
	http.MethodDelete: true,
}

// csrfValid checks the double-submit CSRF token for state-changing requests.
// It only applies when config.AppConfig.CookieSameSite is "none" —
// same-origin deployments (SameSite=Lax) are already immune to CSRF via the
// browser's cookie policy, so this is a no-op there.
//
// Login, token refresh, and logout are exempt because they never reach this
// check: they don't go through RequireAuth (login has no session yet to
// forge; a forged refresh/logout can't be read cross-origin by the attacker
// per the same-origin policy, so the worst case is a spurious token
// rotation or session end, not data exposure).
func csrfValid(r *http.Request) bool {
	if config.AppConfig.CookieSameSite != "none" {
		return true
	}
	if !csrfProtectedMethods[r.Method] {
		return true
	}

	cookie, err := r.Cookie("csrf_token")
	if err != nil || cookie.Value == "" {
		return false
	}

	header := r.Header.Get("X-CSRF-Token")
	if header == "" {
		return false
	}

	return subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(header)) == 1
}
