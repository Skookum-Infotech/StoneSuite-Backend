package controllers

import (
	"log/slog"
	"net/http"

	"stonesuite-backend/middleware"
)

// logSecurityEvent emits a structured, alertable security-event log line at WARN
// level. These are the events a SOC/alerting pipeline watches: failed logins,
// permission denials, IDOR attempts, token abuse. Every line carries a stable
// "security_event" key plus the request correlation id and client IP so a
// reviewer can pivot from an alert to the full request.
//
// event is a stable machine-readable code (e.g. "login_failed",
// "permission_denied", "idor_denied"). kv are additional slog key/value pairs;
// never pass secrets, passwords, or raw tokens.
func logSecurityEvent(r *http.Request, event string, kv ...any) {
	attrs := []any{
		slog.String("security_event", event),
		slog.String("request_id", middleware.RequestIDFromContext(r.Context())),
		slog.String("ip", middleware.ClientIP(r)),
		slog.String("path", r.URL.Path),
	}
	attrs = append(attrs, kv...)
	slog.Warn("security event", attrs...)
}
