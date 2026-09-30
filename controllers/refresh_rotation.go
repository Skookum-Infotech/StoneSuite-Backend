package controllers

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"time"

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

// rotateRefreshToken issues the replacement refresh token and only then revokes
// the one it replaces. The order matters: revoking first would leave the user
// with no valid refresh token if issuing then failed. Revoke failure is not
// fatal — the old token is already inside its rotation grace and expires on its
// own.
func rotateRefreshToken(ctx context.Context, cp *tenancy.ControlPlane, oldHash, identityID string) (string, time.Time, error) {
	raw, expiry, err := issueRefreshToken(ctx, cp, identityID)
	if err != nil {
		return "", time.Time{}, err
	}
	if err := cp.RevokeRefreshToken(ctx, oldHash); err != nil {
		slog.Warn("refresh rotation: revoke old token", "error", err)
	}
	return raw, expiry, nil
}
