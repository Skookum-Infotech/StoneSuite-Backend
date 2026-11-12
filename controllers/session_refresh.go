package controllers

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"stonesuite-backend/tenancy"
)

// refreshSessionID decides the session a refresh mints its access token for.
// A token whose session was replaced or logged out is refused with 401
// session_replaced (ok=false, response written) -- its browser is showing, or
// is about to show, the "signed in on another computer" countdown, and the
// refresh must not hand it a fresh token. Its cookies are left alone so the
// browser's logout stays the one place that clears them.
//
// A token minted before sessions existed has none: it is adopted into a new
// session (adopted=true; the caller attaches the rotated token to it), and
// when the single-session rule applies that new session replaces the others,
// so legacy sign-ins converge on one live session instead of lingering until
// their refresh token expires.
func (h *TenantOps) refreshSessionID(w http.ResponseWriter, r *http.Request, identity *tenancy.Identity, rec *tenancy.RefreshTokenRecord, tenantEnforced bool) (sid string, adopted, ok bool) {
	if rec.SessionID != "" {
		s, err := h.CP.SessionByID(r.Context(), rec.SessionID)
		if err != nil && !errors.Is(err, tenancy.ErrSessionNotFound) {
			slog.Error("refresh: load session", "error", err)
			fail(w, http.StatusInternalServerError, "Failed to refresh session.")
			return "", false, false
		}
		if err != nil || s.Status(time.Now()) != tenancy.SessionActive {
			logSecurityEvent(r, "replaced_session_refresh", "identity", identity.ID, "session", rec.SessionID)
			failWithCode(w, http.StatusUnauthorized, tenancy.SessionReplacedMessage, tenancy.CodeSessionReplaced)
			return "", false, false
		}
		return s.ID, false, true
	}
	isPlatformAdmin, err := h.CP.IsPlatformAdmin(r.Context(), identity.ID)
	if err != nil {
		isPlatformAdmin = false
	}
	sid, done := beginSession(w, r, h.CP, sessionStart{
		identity: identity, takeover: true, reason: replaceReasonLegacy, guard: h.Sessions,
		enforced: singleSessionApplies(tenantEnforced, isPlatformAdmin, false),
	})
	if done {
		return "", false, false
	}
	return sid, true, true
}
