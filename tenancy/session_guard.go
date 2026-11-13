package tenancy

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"time"

	"stonesuite-backend/cache"
	"stonesuite-backend/middleware"
	"stonesuite-backend/models"
)

// CodeSessionReplaced is the error code a replaced session's requests get, so
// the browser can show its "signed in on another computer" countdown.
const CodeSessionReplaced = "session_replaced"

// CodeSessionUnavailable marks a write refused because the session could not
// be checked; the client may retry.
const CodeSessionUnavailable = "session_unavailable"

// sessionGuardTTL is how stale a cached session state may be. It bounds how
// long a replaced session keeps writing after its handover window ends.
const sessionGuardTTL = 10 * time.Second

// sessionGuardMax caps the cached session states held in memory.
const sessionGuardMax = 10000

// SessionReplacedMessage is the user-facing text for a replaced session.
const SessionReplacedMessage = "Your account was signed in on another computer."

// sessionLoader loads a session by id; *ControlPlane satisfies it.
type sessionLoader interface {
	SessionByID(ctx context.Context, id string) (*AuthSession, error)
}

// SessionGuard refuses requests from sessions another sign-in took over: a
// session in its handover window may only read, and an ended one gets 401.
type SessionGuard struct {
	sessions sessionLoader
	cache    *cache.TTLCache[string, *AuthSession]
	now      func() time.Time
	// legacyCutoff: a staff token with no session id issued at or after this
	// instant is refused. Every sign-in path mints a session id since the
	// single-session release, so such a token can only be a pre-release one
	// kept alive some other way. Zero disables the check.
	legacyCutoff time.Time
}

// WithLegacyCutoff sets the instant after which staff tokens must carry a
// session id. Returns g for chaining.
func (g *SessionGuard) WithLegacyCutoff(t time.Time) *SessionGuard {
	g.legacyCutoff = t
	return g
}

// CodeSessionRefreshRequired marks a token that predates sessions: the client
// refreshes, which adopts it into a session, and retries.
const CodeSessionRefreshRequired = "session_refresh_required"

// legacyTokenAllowed reports whether a token without a session id may pass:
// portal tokens always (sessions are a staff concept); staff tokens only when
// issued before cutoff (zero cutoff = always).
func legacyTokenAllowed(kind string, issuedAt, cutoff time.Time) bool {
	return kind == middleware.KindPortal || cutoff.IsZero() || issuedAt.Before(cutoff)
}

// NewSessionGuard builds a guard over the control plane's auth sessions.
func NewSessionGuard(cp *ControlPlane) *SessionGuard {
	return newSessionGuard(cp)
}

func newSessionGuard(l sessionLoader) *SessionGuard {
	return &SessionGuard{
		sessions: l,
		cache:    cache.NewWithMax[string, *AuthSession](sessionGuardTTL, sessionGuardMax),
		now:      time.Now,
	}
}

// guardDecision is what the guard does with one request.
type guardDecision int

const (
	guardAllow guardDecision = iota
	guardRejectWrite
	guardRejectEnded
)

// decideSession maps a session status and request method to a decision.
func decideSession(status, method string) guardDecision {
	switch status {
	case SessionActive:
		return guardAllow
	case SessionReplacing:
		if isReadMethod(method) {
			return guardAllow
		}
		return guardRejectWrite
	default:
		return guardRejectEnded
	}
}

// isReadMethod reports whether method cannot change anything.
func isReadMethod(method string) bool {
	return method == http.MethodGet || method == http.MethodHead || method == http.MethodOptions
}

// check writes the refusal and returns false when the request must stop.
// Tokens without a session id (minted before sessions existed) pass. When
// the session cannot be looked up, reads pass and writes get 503: a replaced
// session must never be able to write just because the lookup failed, but an
// outage must not lock everyone out of reading either.
func (g *SessionGuard) check(w http.ResponseWriter, r *http.Request, p middleware.UserContextPayload) bool {
	if g == nil {
		return true
	}
	if p.SessionID == "" {
		if legacyTokenAllowed(p.Kind, p.IssuedAt, g.legacyCutoff) {
			return true
		}
		slog.Warn("security event",
			slog.String("security_event", "sessionless_token_refused"),
			slog.String("request_id", middleware.RequestIDFromContext(r.Context())),
			slog.String("identity", p.ID),
		)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(models.APIResponse{
			Success: false, Message: "Please sign in again.", Code: CodeSessionRefreshRequired,
		})
		return false
	}
	s, err := g.cache.GetOrLoad(p.SessionID, func() (*AuthSession, error) {
		return g.sessions.SessionByID(r.Context(), p.SessionID)
	})
	if errors.Is(err, ErrSessionNotFound) {
		s = &AuthSession{EndedAt: new(time.Time)}
	} else if err != nil {
		slog.Warn("security event",
			slog.String("security_event", "session_guard_unavailable"),
			slog.String("request_id", middleware.RequestIDFromContext(r.Context())),
			slog.String("identity", p.ID),
			slog.String("error", err.Error()),
		)
		if isReadMethod(r.Method) {
			return true
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(models.APIResponse{
			Success: false, Message: "Please try again in a moment.", Code: CodeSessionUnavailable,
		})
		return false
	}
	switch decideSession(s.Status(g.now()), r.Method) {
	case guardRejectWrite:
		writeSessionReplaced(w, http.StatusConflict)
		return false
	case guardRejectEnded:
		slog.Warn("security event",
			slog.String("security_event", "replaced_session_request"),
			slog.String("request_id", middleware.RequestIDFromContext(r.Context())),
			slog.String("ip", middleware.ClientIP(r)),
			slog.String("identity", p.ID),
			slog.String("session", p.SessionID),
		)
		writeSessionReplaced(w, http.StatusUnauthorized)
		return false
	}
	return true
}

// Require guards an authenticated route that sits outside the tenant
// resolver (e.g. /api/auth/change-password). It MUST run after RequireAuth.
func (g *SessionGuard) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, err := middleware.GetUserFromContext(r.Context())
		if err != nil {
			writeErr(w, http.StatusUnauthorized, "Authentication required.")
			return
		}
		if !g.check(w, r, p) {
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Forget drops a session's cached state so the next request reloads it.
func (g *SessionGuard) Forget(sessionID string) {
	if g != nil {
		g.cache.Delete(sessionID)
	}
}

// writeSessionReplaced writes the session_replaced error with status.
func writeSessionReplaced(w http.ResponseWriter, status int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(models.APIResponse{
		Success: false, Message: SessionReplacedMessage, Code: CodeSessionReplaced,
	})
}

// WithSessionGuard makes the resolver refuse requests from replaced sessions.
// Returns the resolver for chaining.
func (rs *Resolver) WithSessionGuard(g *SessionGuard) *Resolver {
	rs.sessions = g
	return rs
}
