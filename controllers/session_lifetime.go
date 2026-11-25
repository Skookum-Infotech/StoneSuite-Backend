package controllers

import (
	"fmt"
	"time"

	"stonesuite-backend/tenancy"
)

// Codes of a refresh refused because the session's time is up.
const (
	codeSessionIdle   = "session_idle"
	codeSessionMaxAge = "session_max_age"
)

// sessionLimits are the server's inactivity and total-length limits for a
// staff session (config SessionIdleTimeout / SessionMaxAge); 0 = off.
type sessionLimits struct {
	idle   time.Duration
	maxAge time.Duration
}

// lifetimeRefusal decides whether a session may be renewed at now. reported
// is the user's last activity the browser sent with this refresh (nil when it
// sent none); it can only move the stored activity forward, and never past
// now. Returns the refusal code, or "" when the session may continue.
func lifetimeRefusal(s *tenancy.AuthSession, reported *time.Time, l sessionLimits, now time.Time) string {
	if l.maxAge > 0 && now.Sub(s.CreatedAt) >= l.maxAge {
		return codeSessionMaxAge
	}
	if l.idle > 0 && now.Sub(lastActivity(s, reported, now)) >= l.idle {
		return codeSessionIdle
	}
	return ""
}

// lastActivity is the later of the stored and the reported activity, capped at now.
func lastActivity(s *tenancy.AuthSession, reported *time.Time, now time.Time) time.Time {
	last := s.LastActiveAt
	if reported != nil && reported.After(last) {
		last = *reported
	}
	if last.After(now) {
		last = now
	}
	return last
}

// cappedTokenLifetime is d, cut short so the access token expires no later
// than the session's maximum length (when that is set).
func cappedTokenLifetime(d time.Duration, s *tenancy.AuthSession, l sessionLimits, now time.Time) time.Duration {
	if l.maxAge <= 0 {
		return d
	}
	left := s.CreatedAt.Add(l.maxAge).Sub(now)
	if left < d {
		return max(left, 0)
	}
	return d
}

// lifetimeMessage is the user-facing text for a lifetime refusal.
func lifetimeMessage(code string, l sessionLimits) string {
	if code == codeSessionMaxAge {
		return fmt.Sprintf("For security, sessions last %s. Please sign in again.", humanDuration(l.maxAge))
	}
	return fmt.Sprintf("You were signed out after %s of inactivity.", humanDuration(l.idle))
}

// humanDuration renders whole hours as "12 hours", else minutes ("60 minutes").
func humanDuration(d time.Duration) string {
	if d >= time.Hour && d%time.Hour == 0 && d != time.Hour {
		return fmt.Sprintf("%d hours", int(d/time.Hour))
	}
	minutes := int(d / time.Minute)
	if minutes == 1 {
		return "1 minute"
	}
	return fmt.Sprintf("%d minutes", minutes)
}
