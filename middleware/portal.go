package middleware

import (
	"encoding/json"
	"log/slog"
	"net/http"

	"stonesuite-backend/models"
)

// RequirePortal admits only customer-portal sessions.
//
// It is the converse of the containment check in RequireAuth: that one keeps
// portal tokens out of the internal surface, this one keeps staff tokens out of
// the portal surface. Both are needed — a staff token reaching a portal handler
// would resolve no customer_portal_user row and fail, but it should be refused
// at the door rather than deep inside a handler.
//
// Fails closed: a token with no kind claim is staff (every token issued before
// the portal existed has none), so only an explicit KindPortal claim passes.
// Must be layered after RequireAuth, which parses the claim.
func RequirePortal(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload, err := GetUserFromContext(r.Context())
		if err != nil || payload.ID == "" {
			denyPortal(w, r, http.StatusUnauthorized, "Authentication required.", "")
			return
		}
		if payload.Kind != KindPortal {
			denyPortal(w, r, http.StatusForbidden,
				"This session does not have access to the customer portal.", payload.ID)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// denyPortal writes a JSON error and logs a security event with the stable
// `security_event` key, matching the convention in controllers/security_log.go.
func denyPortal(w http.ResponseWriter, r *http.Request, status int, msg, identityID string) {
	slog.Warn("security event",
		slog.String("security_event", "staff_token_on_portal"),
		slog.String("request_id", RequestIDFromContext(r.Context())),
		slog.String("ip", ClientIP(r)),
		slog.String("identity", identityID),
		slog.String("path", r.URL.Path),
	)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(models.APIResponse{Success: false, Message: msg})
}
