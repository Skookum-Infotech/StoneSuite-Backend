package controllers

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"stonesuite-backend/config"
	"stonesuite-backend/tenancy"
)

// Fallback lifetimes for when the env durations do not parse.
const (
	defaultAccessTokenLifetime  = time.Hour
	defaultRefreshTokenLifetime = 24 * time.Hour
	defaultRememberMeLifetime   = 720 * time.Hour
)

// tokenHashLogPrefixLen is how much of a refresh token's hash a security event
// may carry: enough to correlate log lines, never the full hash.
const tokenHashLogPrefixLen = 8

// parseDurationOr parses raw, returning fallback when it is empty or invalid.
func parseDurationOr(raw string, fallback time.Duration) time.Duration {
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return fallback
	}
	return d
}

// accessTokenLifetime is the lifetime of every access token, remember-me or
// not: a long-lived access token cannot be revoked, so "remember me" extends
// the refresh token instead (see refreshTokenLifetime).
func accessTokenLifetime() time.Duration {
	return parseDurationOr(config.AppConfig.JWTExpiresIn, defaultAccessTokenLifetime)
}

// refreshTokenLifetime is the lifetime of a new refresh token. A remember-me
// login gets JWT_REMEMBER_ME_EXPIRES_IN (never shorter than the normal one);
// rotation carries the lifetime forward, so the session stays remembered.
func refreshTokenLifetime(rememberMe bool) time.Duration {
	normal := parseDurationOr(config.AppConfig.RefreshTokenExpiresIn, defaultRefreshTokenLifetime)
	if !rememberMe {
		return normal
	}
	return max(normal, parseDurationOr(config.AppConfig.JWTRememberMeExpiresIn, defaultRememberMeLifetime))
}

// issueRefreshToken generates a random raw token, hashes it, persists it, and
// returns the raw value (for the cookie) plus the expiry time.
func issueRefreshToken(ctx context.Context, cp *tenancy.ControlPlane, identityID string, rememberMe bool) (string, time.Time, error) {
	raw, err := randomToken()
	if err != nil {
		return "", time.Time{}, fmt.Errorf("generate refresh token: %w", err)
	}
	expiry := time.Now().Add(refreshTokenLifetime(rememberMe))
	if err := cp.CreateRefreshToken(ctx, identityID, tenancy.HashRefreshToken(raw), expiry); err != nil {
		return "", time.Time{}, err
	}
	return raw, expiry, nil
}

// claimRefreshToken resolves the refresh_token cookie value to a usable stored
// token, shared by the staff and portal refresh handlers so both fail the same
// way. It writes the error response itself and returns ok=false when the caller
// must stop.
func claimRefreshToken(w http.ResponseWriter, r *http.Request, cp *tenancy.ControlPlane, raw, reuseEvent string) (rec *tenancy.RefreshTokenRecord, hash string, ok bool) {
	hash = tenancy.HashRefreshToken(raw)
	rec, err := cp.RefreshTokenByHash(r.Context(), hash)
	if err != nil {
		refreshTokenFailed(w, r, cp, err, hash, reuseEvent)
		return nil, "", false
	}
	return rec, hash, true
}

// refreshTokenFailed writes the response for a refresh token that cannot be
// used, whether the claim or the rotation found it out.
//
// Only a token that is definitively invalid (unknown, expired, or reused past
// the rotation grace) clears the session cookies and answers 401 — that is the
// one outcome that legitimately ends a session. Reuse past the grace is
// treated as theft: every refresh token of the identity is revoked, ending the
// thief's session along with the victim's. A superseded token (replayed in the
// grace after its successor was already rotated or revoked) answers 401 but
// leaves the cookies alone, since a concurrent response may just have set a
// live one. A lookup failure (database blip, cold start) answers 500 and
// leaves the cookies alone: the token may be perfectly good, and clearing it
// would log the user out for an outage that is not theirs.
func refreshTokenFailed(w http.ResponseWriter, r *http.Request, cp *tenancy.ControlPlane, err error, hash, reuseEvent string) {
	var reused *tenancy.RefreshTokenReusedError
	switch {
	case errors.As(err, &reused):
		revoked := true
		if rErr := cp.RevokeAllRefreshTokens(r.Context(), reused.IdentityID); rErr != nil {
			slog.Error("refresh: revoke sessions after token reuse", "identity", reused.IdentityID, "error", rErr)
			revoked = false
		}
		logSecurityEvent(r, reuseEvent, "token_hash_prefix", hash[:tokenHashLogPrefixLen],
			"identity", reused.IdentityID, "sessions_revoked", revoked)
		clearAuthCookies(w)
		fail(w, http.StatusUnauthorized, "Session invalid. Please sign in again.")
	case errors.Is(err, tenancy.ErrRefreshTokenSuperseded):
		logSecurityEvent(r, "refresh_token_superseded", "token_hash_prefix", hash[:tokenHashLogPrefixLen])
		fail(w, http.StatusUnauthorized, "Session ended. Please sign in again.")
	case errors.Is(err, tenancy.ErrRefreshTokenNotFound):
		clearAuthCookies(w)
		fail(w, http.StatusUnauthorized, "Refresh token expired. Please sign in again.")
	default:
		slog.Error("refresh: token check failed", "error", err)
		fail(w, http.StatusInternalServerError, "Failed to refresh session.")
	}
}

// rotateRefreshToken issues the replacement refresh token and revokes the one
// it replaces in a single transaction: either both happen or neither, so a
// failure can neither strand the user without a valid token nor leave two live.
// The replacement keeps the old token's lifetime. Errors from the token itself
// (reused, superseded, expired) go to refreshTokenFailed. A replay inside the
// rotation grace succeeds but revokes the successor someone else may hold, so
// it is logged as a security event (two tabs refreshing together log it too).
func rotateRefreshToken(r *http.Request, cp *tenancy.ControlPlane, oldHash string) (string, time.Time, error) {
	raw, err := randomToken()
	if err != nil {
		return "", time.Time{}, fmt.Errorf("generate refresh token: %w", err)
	}
	rec, err := cp.RotateRefreshToken(r.Context(), oldHash, tenancy.HashRefreshToken(raw),
		refreshTokenLifetime(false), refreshTokenLifetime(true))
	if err != nil {
		return "", time.Time{}, err
	}
	if rec.GraceReplay {
		logSecurityEvent(r, "refresh_token_grace_replay", "token_hash_prefix", oldHash[:tokenHashLogPrefixLen],
			"identity", rec.IdentityID)
	}
	return raw, rec.ExpiresAt, nil
}

// revokeSessionOnLogout revokes the presented refresh token immediately (no
// rotation grace), clears the auth cookies and writes the response. A revoke
// failure answers 500 success:false: the cookies are still cleared, but the
// client must not be told the server-side session ended when it did not.
func revokeSessionOnLogout(w http.ResponseWriter, r *http.Request, cp *tenancy.ControlPlane) {
	if cookie, err := r.Cookie("refresh_token"); err == nil && cookie.Value != "" {
		if err := cp.RevokeRefreshTokenNow(r.Context(), tenancy.HashRefreshToken(cookie.Value)); err != nil {
			slog.Error("logout: revoke refresh token", "error", err)
			clearAuthCookies(w)
			fail(w, http.StatusInternalServerError, "Failed to end session. Please try again.")
			return
		}
	}
	clearAuthCookies(w)
	writeJSON(w, http.StatusOK, map[string]any{"success": true})
}

// presentedRefreshTokenHash is the hash of the request's refresh_token cookie,
// or "" when there is none. ChangePassword passes it as the session to keep.
func presentedRefreshTokenHash(r *http.Request) string {
	cookie, err := r.Cookie("refresh_token")
	if err != nil || cookie.Value == "" {
		return ""
	}
	return tenancy.HashRefreshToken(cookie.Value)
}
