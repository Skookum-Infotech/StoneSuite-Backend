package middleware

import (
	"crypto/subtle"
	"net/http"
	"strings"

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

// csrfValid checks that a state-changing request is not a cross-site forgery.
// It only applies when config.AppConfig.CookieSameSite is "none" —
// same-origin/SameSite=Lax deployments are already immune to CSRF via the
// browser's cookie policy, so this is a no-op there.
//
// Two independent signals are accepted, in order:
//
//  1. Origin allowlist. Browsers set the Origin header on every cross-origin
//     request and on all state-changing same-origin requests, and a page on
//     another site cannot forge it (Origin is a Forbidden header name). If the
//     Origin is one this backend serves (CORS_ORIGIN), the request came from
//     our own frontend and is not a forgery. This is the primary check: it
//     does not depend on any cookie, so it cannot break when the csrf_token
//     cookie is missing or expired — the failure mode that made the pure
//     double-submit check below unreliable across this app's scale-to-zero
//     restarts (recurring 403 csrf_mismatch on dev, PRs #167/#170/#171/#178).
//
//  2. Double-submit cookie. The non-HttpOnly csrf_token cookie value must
//     equal the X-CSRF-Token request header. Kept as defense in depth and for
//     any client the browser does not send an Origin for.
//
// Login, token refresh, and logout never reach this check — they aren't
// wrapped in RequireAuth (login/refresh have no session to forge; a forged
// logout only ends a session).
func csrfValid(r *http.Request) bool {
	if config.AppConfig.CookieSameSite != "none" {
		return true
	}
	if !csrfProtectedMethods[r.Method] {
		return true
	}

	// 1. Origin allowlist.
	if origin := r.Header.Get("Origin"); origin != "" && originAllowed(origin) {
		return true
	}

	// 2. Double-submit cookie.
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

// originAllowed reports whether origin is in the CORS_ORIGIN allowlist. It
// mirrors the exact-match comparison main.go's CORS handler uses to build its
// Access-Control-Allow-Origin response, so the set of origins that can drive
// the API is identical to the set the API will answer.
func originAllowed(origin string) bool {
	for _, o := range strings.Split(config.AppConfig.CorsOrigin, ",") {
		if strings.TrimSpace(o) == origin {
			return true
		}
	}
	return false
}
