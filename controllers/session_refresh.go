package controllers

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"stonesuite-backend/config"
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
//
// A live session is also held to the inactivity and maximum-length limits
// (see lifetimeRefusal): past either, it is ended and the refresh refused with
// 401 session_idle / session_max_age. Otherwise the reported user activity is
// recorded, and tokenLife is the access-token lifetime cut to the session's
// remaining length.
func (h *TenantOps) refreshSessionID(w http.ResponseWriter, r *http.Request, identity *tenancy.Identity, rec *tenancy.RefreshTokenRecord, tenantEnforced bool, reportedActive *time.Time) (sid string, tokenLife time.Duration, adopted, ok bool) {
	tokenLife = accessTokenLifetime()
	if rec.SessionID != "" {
		s, err := h.CP.SessionByID(r.Context(), rec.SessionID)
		if err != nil && !errors.Is(err, tenancy.ErrSessionNotFound) {
			slog.Error("refresh: load session", "error", err)
			fail(w, http.StatusInternalServerError, "Failed to refresh session.")
			return "", 0, false, false
		}
		if err != nil || s.Status(time.Now()) != tenancy.SessionActive {
			logSecurityEvent(r, "replaced_session_refresh", "identity", identity.ID, "session", rec.SessionID)
			if err != nil || s.ReplacedAt == nil {
				// Logged out, timed out or revoked: just sign out.
				failWithCode(w, http.StatusUnauthorized, tenancy.SessionEndedMessage, tenancy.CodeSessionEnded)
				return "", 0, false, false
			}
			failWithCode(w, http.StatusUnauthorized, tenancy.SessionReplacedMessage, tenancy.CodeSessionReplaced)
			return "", 0, false, false
		}
		limits, now := currentSessionLimits(), time.Now()
		if code := lifetimeRefusal(s, reportedActive, limits, now); code != "" {
			h.endTimedOutSession(r, identity.ID, s.ID, code)
			failWithCode(w, http.StatusUnauthorized, lifetimeMessage(code, limits), code)
			return "", 0, false, false
		}
		if reportedActive != nil {
			if err := h.CP.RecordActivity(r.Context(), s.ID, *reportedActive); err != nil {
				slog.Warn("refresh: record session activity", "error", err)
			}
		}
		return s.ID, cappedTokenLifetime(tokenLife, s, limits, now), false, true
	}
	isPlatformAdmin, err := h.CP.IsPlatformAdmin(r.Context(), identity.ID)
	if err != nil {
		isPlatformAdmin = false
	}
	sid, done := beginSession(w, r, h.CP, sessionStart{
		identity: identity, takeover: true, reason: tenancy.ReplaceLegacy, guard: h.Sessions,
		enforced: singleSessionApplies(tenantEnforced, isPlatformAdmin, false),
	})
	if done {
		return "", 0, false, false
	}
	return sid, tokenLife, true, true
}

// currentSessionLimits reads the configured session limits.
func currentSessionLimits() sessionLimits {
	return sessionLimits{idle: config.AppConfig.SessionIdleTimeout, maxAge: config.AppConfig.SessionMaxAge}
}

// endTimedOutSession ends a session refused for inactivity or length, so a
// later sign-in elsewhere is not asked about it, and logs why.
func (h *TenantOps) endTimedOutSession(r *http.Request, identityID, sessionID, code string) {
	if err := h.CP.EndSession(r.Context(), sessionID); err != nil {
		slog.Warn("refresh: end timed-out session", "error", err)
	}
	h.Sessions.Forget(sessionID)
	logSecurityEvent(r, "session_timed_out", "identity", identityID, "session", sessionID, "reason", code)
}
