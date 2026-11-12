package controllers

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"stonesuite-backend/config"
	"stonesuite-backend/middleware"
	"stonesuite-backend/tenancy"
)

// Single-session (per-user licensing) tuning.
const (
	// sessionPromptWindow: another session seen this recently is "in use", so
	// signing in elsewhere asks before ending it. An older one is replaced
	// without asking (e.g. a browser closed without logging out).
	sessionPromptWindow = 15 * time.Minute
	// sessionHandoverGrace is how long a replaced session may still read: the
	// browser's 30-second countdown plus one status poll plus slack.
	sessionHandoverGrace = 45 * time.Second
	// takeoverTicketTTL bounds how long the "end the other session?" prompt
	// may stay open before the user has to sign in again.
	takeoverTicketTTL = 5 * time.Minute
	// maxUserAgentLen caps the stored User-Agent.
	maxUserAgentLen = 512
)

// Error codes the sign-in UI branches on.
const (
	codeActiveSession = "active_session"
	codeTakeoverGone  = "takeover_expired"
)

// The takeover ticket travels only as an HttpOnly cookie scoped to the one
// endpoint that redeems it: never in a JSON body, so it cannot leak through
// script or logs, and only the browser that finished the sign-in holds it.
const (
	takeoverCookieName = "takeover_ticket"
	takeoverCookiePath = "/api/auth/session/takeover"
)

// Why a sign-in replaced the user's other sessions, as logged.
const (
	replaceReasonConfirmed = "confirmed"      // the user confirmed the prompt
	replaceReasonIdle      = "idle"           // the other session was not in use
	replaceReasonLegacy    = "adopted_legacy" // a pre-session token was refreshed
)

// singleSessionApplies reports whether a sign-in is limited to one live
// session: only staff of a customer tenant that enforces it. Platform admins
// and customer-portal users are exempt; so is the platform-owner tenant,
// whose flag is always false.
func singleSessionApplies(tenantEnforced, isPlatformAdmin, isPortal bool) bool {
	return tenantEnforced && !isPlatformAdmin && !isPortal
}

// shouldPromptTakeover reports whether other is a different session recent
// enough that the user must confirm ending it.
func shouldPromptTakeover(other *tenancy.AuthSession, ownSessionID string, now time.Time) bool {
	return other != nil && other.ID != ownSessionID && now.Sub(other.LastSeenAt) <= sessionPromptWindow
}

// truncateUserAgent caps a User-Agent header at maxUserAgentLen bytes.
func truncateUserAgent(ua string) string {
	if len(ua) > maxUserAgentLen {
		return ua[:maxUserAgentLen]
	}
	return ua
}

// sessionStart carries what the gate decided for one sign-in.
type sessionStart struct {
	identity   *tenancy.Identity
	rememberMe bool
	// takeover is set when the gate must not ask (the user already confirmed
	// at POST /api/auth/session/takeover, or a legacy token is adopted).
	takeover bool
	enforced bool
	// reason is logged when other sessions are replaced; defaults to idle,
	// or confirmed when takeover is set.
	reason string
	// guard, when set, has its cached state for replaced sessions dropped so
	// they turn read-only at once on this machine.
	guard *tenancy.SessionGuard
}

// replaceReason is the logged reason for replacing other sessions.
func (st sessionStart) replaceReason() string {
	switch {
	case st.reason != "":
		return st.reason
	case st.takeover:
		return replaceReasonConfirmed
	default:
		return replaceReasonIdle
	}
}

// ownSessionID is the auth session the request's refresh_token cookie belongs
// to, when it is this identity's and still live -- i.e. the same browser
// signing in again. "" otherwise.
func ownSessionID(r *http.Request, cp *tenancy.ControlPlane, identityID string) string {
	hash := presentedRefreshTokenHash(r)
	if hash == "" {
		return ""
	}
	rec, err := cp.RefreshTokenByHash(r.Context(), hash)
	if err != nil || rec.IdentityID != identityID || rec.SessionID == "" {
		return ""
	}
	s, err := cp.SessionByID(r.Context(), rec.SessionID)
	if err != nil || s.Status(time.Now()) != tenancy.SessionActive {
		return ""
	}
	return s.ID
}

// beginSession resolves the auth session a sign-in mints its tokens for. When
// the rule applies and another browser is in use, it answers 409
// active_session with a takeover ticket and returns done=true. Otherwise it
// returns the session id -- the same browser's own session when it signs in
// again, else a new one -- having replaced any other live session.
func beginSession(w http.ResponseWriter, r *http.Request, cp *tenancy.ControlPlane, st sessionStart) (sid string, done bool) {
	ctx := r.Context()
	own := ownSessionID(r, cp, st.identity.ID)
	if st.enforced && !st.takeover {
		other, err := cp.LiveSessionFor(ctx, st.identity.ID)
		if err != nil && !errors.Is(err, tenancy.ErrSessionNotFound) {
			slog.Error("session gate: load live session", "identity", st.identity.ID, "error", err)
			fail(w, http.StatusInternalServerError, "Login failed.")
			return "", true
		}
		if err == nil && shouldPromptTakeover(other, own, time.Now()) {
			promptTakeover(w, r, cp, st, other)
			return "", true
		}
	}
	sid = own
	if sid == "" {
		var err error
		ua := truncateUserAgent(r.UserAgent())
		sid, err = cp.CreateSession(ctx, tenancy.NewSessionInput{
			IdentityID: st.identity.ID, UserAgent: ua,
			IP: middleware.ClientIP(r), DeviceLabel: deviceLabel(ua),
		})
		if err != nil {
			slog.Error("session gate: create session", "identity", st.identity.ID, "error", err)
			fail(w, http.StatusInternalServerError, "Login failed.")
			return "", true
		}
	}
	if st.enforced {
		if err := replaceOtherSessions(r, cp, st, sid); err != nil {
			fail(w, http.StatusInternalServerError, "Login failed.")
			return "", true
		}
	}
	return sid, false
}

// replaceOtherSessions ends every other live session of identityID after the
// handover grace and logs it.
func replaceOtherSessions(r *http.Request, cp *tenancy.ControlPlane, st sessionStart, keepID string) error {
	identityID := st.identity.ID
	replaced, err := cp.ReplaceSessions(r.Context(), identityID, keepID, sessionHandoverGrace)
	if err != nil {
		slog.Error("session gate: replace sessions", "identity", identityID, "error", err)
		return fmt.Errorf("replace sessions: %w", err)
	}
	for _, old := range replaced {
		st.guard.Forget(old)
		logSecurityEvent(r, "session_takeover", "identity", identityID,
			"old_session", old, "new_session", keepID, "reason", st.replaceReason())
	}
	return nil
}

// promptTakeover answers 409 active_session: the user must confirm ending the
// other session. The ticket is single-use and stands in for the sign-in just
// completed, so confirming needs no second password or code.
func promptTakeover(w http.ResponseWriter, r *http.Request, cp *tenancy.ControlPlane, st sessionStart, other *tenancy.AuthSession) {
	raw, err := randomToken()
	if err != nil {
		fail(w, http.StatusInternalServerError, "Login failed.")
		return
	}
	if err := cp.CreateTakeoverTicket(r.Context(), tenancy.HashRefreshToken(raw), st.identity.ID, st.rememberMe, takeoverTicketTTL); err != nil {
		slog.Error("session gate: create takeover ticket", "identity", st.identity.ID, "error", err)
		fail(w, http.StatusInternalServerError, "Login failed.")
		return
	}
	logSecurityEvent(r, "session_takeover_prompted", "identity", st.identity.ID, "other_session", other.ID)
	setTakeoverCookie(w, raw, int(takeoverTicketTTL.Seconds()))
	writeJSON(w, http.StatusConflict, map[string]any{
		"success": false,
		"code":    codeActiveSession,
		"message": "You are already signed in on another device.",
		"activeSession": map[string]any{
			"deviceLabel": other.DeviceLabel,
			"lastSeenAt":  other.LastSeenAt.UnixMilli(),
		},
	})
}

// setTakeoverCookie sets (maxAge > 0) or clears (maxAge < 0) the ticket cookie,
// with the same SameSite/Secure policy as the session cookies.
func setTakeoverCookie(w http.ResponseWriter, raw string, maxAge int) {
	sameSite := http.SameSiteLaxMode
	if config.AppConfig.CookieSameSite == "none" {
		sameSite = http.SameSiteNoneMode
	}
	http.SetCookie(w, &http.Cookie{
		Name: takeoverCookieName, Value: raw, Path: takeoverCookiePath,
		HttpOnly: true, Secure: config.AppConfig.IsProduction(), SameSite: sameSite, MaxAge: maxAge,
	})
}

// SessionTakeover ends the user's other session and signs this browser in,
// after they confirmed the active_session prompt. The sign-in is re-run from
// the workspace-standing checks on, exactly as MFAVerify does. The ticket is
// the takeover_ticket cookie the active_session answer set on this browser.
// Path: POST /api/auth/session/takeover
func (h *TenantOps) SessionTakeover(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie(takeoverCookieName)
	if err != nil || cookie.Value == "" {
		logSecurityEvent(r, "session_takeover_invalid_ticket", "reason", "missing")
		failWithCode(w, http.StatusGone, "This sign-in has expired. Please sign in again.", codeTakeoverGone)
		return
	}
	setTakeoverCookie(w, "", -1)
	identityID, rememberMe, err := h.CP.ConsumeTakeoverTicket(r.Context(), tenancy.HashRefreshToken(cookie.Value))
	if errors.Is(err, tenancy.ErrTakeoverTicketNotFound) {
		logSecurityEvent(r, "session_takeover_invalid_ticket", "reason", "unknown_or_expired")
		failWithCode(w, http.StatusGone, "This sign-in has expired. Please sign in again.", codeTakeoverGone)
		return
	}
	if err != nil {
		slog.Error("session takeover: consume ticket", "error", err)
		fail(w, http.StatusInternalServerError, "Login failed.")
		return
	}
	identity, err := h.CP.IdentityByID(r.Context(), identityID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Login failed.")
		return
	}
	h.completeLogin(w, r, identity, loginAttempt{
		email: identity.Email, rememberMe: rememberMe, codeVerified: true, takeover: true,
	})
}
