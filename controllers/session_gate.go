package controllers

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

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
	// browser's 30-second countdown plus one 30-second status poll plus slack.
	sessionHandoverGrace = 75 * time.Second
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

// singleSessionApplies reports whether a sign-in is limited to one live
// session: only staff of a customer tenant that enforces it. Platform admins
// and customer-portal users are exempt; so is the platform-owner tenant,
// whose flag is always false.
func singleSessionApplies(tenantEnforced, isPlatformAdmin, isPortal bool) bool {
	return tenantEnforced && !isPlatformAdmin && !isPortal
}

// shouldPromptTakeover reports whether other is recent enough that the user
// must confirm ending it. Every browser is asked, the same one signing in
// again included: a "this is my own browser" shortcut keyed off a presented
// cookie would let a copied cookie skip the prompt and the takeover limits.
func shouldPromptTakeover(other *tenancy.AuthSession, now time.Time) bool {
	return other != nil && now.Sub(other.LastSeenAt) <= sessionPromptWindow
}

// limitsFor reads a tenant's limits on moving a sign-in between devices.
func limitsFor(t *tenancy.Tenant) tenancy.TakeoverLimits {
	return tenancy.TakeoverLimits{
		Cooldown: time.Duration(t.TakeoverCooldownMinutes) * time.Minute,
		DailyCap: t.TakeoverDailyCap,
	}
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
	// limits are the customer's takeover cooldown and daily cap. tenantID and
	// pool (the tenant database) say whose administrators to alert when the
	// daily cap is hit; a nil pool skips the alert.
	limits   tenancy.TakeoverLimits
	tenantID string
	pool     *pgxpool.Pool
}

// replaceReason is the logged reason for replacing other sessions.
func (st sessionStart) replaceReason() string {
	switch {
	case st.reason != "":
		return st.reason
	case st.takeover:
		return tenancy.ReplaceConfirmed
	default:
		return tenancy.ReplaceIdle
	}
}

// beginSession resolves the auth session a sign-in mints its tokens for.
// When the rule applies and another browser is in use, it answers -- having
// written the response, done=true -- either 409 active_session with a
// takeover ticket, or 429 when moving the sign-in would break the customer's
// cooldown or daily cap. Otherwise it creates the session and, when the rule
// applies, replaces every other live one (StartSession). StartSession
// re-checks the limits under the per-user lock, so a confirmation or a
// silent replacement of a recently used session is held to them too.
func beginSession(w http.ResponseWriter, r *http.Request, cp *tenancy.ControlPlane, st sessionStart) (sid string, done bool) {
	ctx := r.Context()
	if st.enforced && !st.takeover {
		other, err := cp.LiveSessionFor(ctx, st.identity.ID)
		if err != nil && !errors.Is(err, tenancy.ErrSessionNotFound) {
			slog.Error("session gate: load live session", "identity", st.identity.ID, "error", err)
			fail(w, http.StatusInternalServerError, "Login failed.")
			return "", true
		}
		if err == nil && shouldPromptTakeover(other, time.Now()) {
			if refuseOverLimit(w, r, cp, st) {
				return "", true
			}
			promptTakeover(w, r, cp, st, other)
			return "", true
		}
	}
	ua := truncateUserAgent(r.UserAgent())
	res, err := cp.StartSession(ctx, tenancy.StartSessionInput{
		NewSessionInput: tenancy.NewSessionInput{
			IdentityID: st.identity.ID, UserAgent: ua,
			IP: middleware.ClientIP(r), DeviceLabel: deviceLabel(ua),
		},
		Enforced: st.enforced, Grace: sessionHandoverGrace, Reason: st.replaceReason(),
		Limits: st.limits, RefuseIfLive: st.replaceReason() == tenancy.ReplaceLegacy,
	})
	var limited *tenancy.TakeoverLimitError
	switch {
	case errors.As(err, &limited):
		writeLimitRefusal(w, r, st, limited)
		return "", true
	case errors.Is(err, tenancy.ErrLiveSessionExists):
		logSecurityEvent(r, "legacy_session_refused", "identity", st.identity.ID)
		failWithCode(w, http.StatusUnauthorized, tenancy.SessionReplacedMessage, tenancy.CodeSessionReplaced)
		return "", true
	case err != nil:
		slog.Error("session gate: start session", "identity", st.identity.ID, "error", err)
		fail(w, http.StatusInternalServerError, "Login failed.")
		return "", true
	}
	for _, old := range res.Replaced {
		st.guard.Forget(old)
		logSecurityEvent(r, "session_takeover", "identity", st.identity.ID,
			"old_session", old, "new_session", res.ID, "reason", st.replaceReason())
	}
	return res.ID, false
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
