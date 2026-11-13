package controllers

import (
	"errors"
	"log/slog"
	"net/http"
	"time"

	"stonesuite-backend/middleware"
	"stonesuite-backend/tenancy"
)

// sessionStatusResponse builds the GET /api/auth/session body for s.
func sessionStatusResponse(s *tenancy.AuthSession, replacedBy *tenancy.AuthSession, now time.Time) map[string]any {
	out := map[string]any{"success": true, "status": s.Status(now)}
	if s.EndsAt != nil {
		out["endsAt"] = s.EndsAt.UnixMilli()
		// Relative too: the browser counts down from this, so a wrong clock
		// on the user's computer cannot cut the countdown short.
		out["endsInMs"] = max(int64(0), s.EndsAt.Sub(now).Milliseconds())
	}
	if s.ReplacedAt != nil {
		by := map[string]any{"at": s.ReplacedAt.UnixMilli(), "deviceLabel": ""}
		if replacedBy != nil {
			by["deviceLabel"] = replacedBy.DeviceLabel
		}
		out["replacedBy"] = by
	}
	return out
}

// SessionStatus tells a browser whether its session is still the live one, so
// a replaced session can show its countdown. Polled every few seconds; it
// also records that the session is in use.
// Path: GET /api/auth/session
func (h *TenantOps) SessionStatus(w http.ResponseWriter, r *http.Request) {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return
	}
	if payload.SessionID == "" {
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "status": tenancy.SessionActive})
		return
	}
	s, err := h.CP.SessionByID(r.Context(), payload.SessionID)
	if errors.Is(err, tenancy.ErrSessionNotFound) {
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "status": tenancy.SessionEnded})
		return
	}
	if err != nil {
		slog.Error("session status: load", "error", err)
		fail(w, http.StatusInternalServerError, "Failed to load session.")
		return
	}
	// Defense in depth: the sid comes from the caller's own signed token, but
	// a session row of another identity must never be described to them.
	if s.IdentityID != payload.ID {
		logSecurityEvent(r, "session_status_identity_mismatch", "identity", payload.ID, "session", s.ID)
		writeJSON(w, http.StatusOK, map[string]any{"success": true, "status": tenancy.SessionEnded})
		return
	}
	now := time.Now()
	var by *tenancy.AuthSession
	if s.ReplacedBy != "" {
		if b, bErr := h.CP.SessionByID(r.Context(), s.ReplacedBy); bErr == nil && b.IdentityID == payload.ID {
			by = b
		}
	}
	if s.Status(now) == tenancy.SessionActive {
		if tErr := h.CP.TouchSession(r.Context(), s.ID); tErr != nil {
			slog.Warn("session status: touch", "error", tErr)
		}
	}
	writeJSON(w, http.StatusOK, sessionStatusResponse(s, by, now))
}

// endSessionForRefreshToken marks the session behind a refresh token hash as
// logged out. Best-effort: logout must not fail because of it.
func endSessionForRefreshToken(r *http.Request, cp *tenancy.ControlPlane, guard *tenancy.SessionGuard, hash string) {
	rec, err := cp.RefreshTokenByHash(r.Context(), hash)
	if err != nil || rec.SessionID == "" {
		return
	}
	if err := cp.EndSession(r.Context(), rec.SessionID); err != nil {
		slog.Warn("logout: end session", "error", err)
		return
	}
	guard.Forget(rec.SessionID)
}
