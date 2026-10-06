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

// claimRefreshToken resolves the refresh_token cookie value to a usable stored
// token, shared by the staff and portal refresh handlers so both fail the same
// way. It writes the error response itself and returns ok=false when the caller
// must stop.
//
// Only a token that is definitively invalid (unknown, expired, or reused past
// the rotation grace) clears the session cookies and answers 401 — that is the
// one outcome that legitimately ends a session. A lookup failure (database
// blip, cold start) answers 500 and leaves the cookies alone: the token may be
// perfectly good, and clearing it would log the user out for an outage that is
// not theirs.
func claimRefreshToken(w http.ResponseWriter, r *http.Request, cp *tenancy.ControlPlane, raw, reuseEvent string) (rec *tenancy.RefreshTokenRecord, hash string, ok bool) {
	hash = tenancy.HashRefreshToken(raw)
	rec, err := cp.RefreshTokenByHash(r.Context(), hash)
	switch {
	case err == nil:
		return rec, hash, true
	case errors.Is(err, tenancy.ErrRefreshTokenReused):
		logSecurityEvent(r, reuseEvent, "token_hash_prefix", hash[:8])
		clearAuthCookies(w)
		fail(w, http.StatusUnauthorized, "Session invalid. Please sign in again.")
	case errors.Is(err, tenancy.ErrRefreshTokenNotFound):
		clearAuthCookies(w)
		fail(w, http.StatusUnauthorized, "Refresh token expired. Please sign in again.")
	default:
		slog.Error("refresh: token lookup failed", "error", err)
		fail(w, http.StatusInternalServerError, "Failed to refresh session.")
	}
	return nil, "", false
}

// rotateRefreshToken issues the replacement refresh token and revokes the one
// it replaces in a single transaction: either both happen or neither, so a
// failure can neither strand the user without a valid token nor leave two live.
func rotateRefreshToken(ctx context.Context, cp *tenancy.ControlPlane, oldHash, identityID string) (string, time.Time, error) {
	raw, err := randomToken()
	if err != nil {
		return "", time.Time{}, fmt.Errorf("generate refresh token: %w", err)
	}
	rd, err := time.ParseDuration(config.AppConfig.RefreshTokenExpiresIn)
	if err != nil {
		rd = 24 * time.Hour
	}
	expiry := time.Now().Add(rd)
	if err := cp.RotateRefreshToken(ctx, oldHash, identityID, tenancy.HashRefreshToken(raw), expiry); err != nil {
		return "", time.Time{}, err
	}
	return raw, expiry, nil
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
