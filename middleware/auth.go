package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"

	"github.com/golang-jwt/jwt/v5"
	"stonesuite-backend/config"
	"stonesuite-backend/models"
)

type contextKey string

const UserContextKey contextKey = "userContext"

// UserContextPayload holds the authenticated user metadata stored in request context.
//
//	ID           - control-plane identity id (the login identity)
//	Email        - identity email
//	TenantID     - tenant the identity belongs to (drives DB routing)
//	UserID       - tenant-local users.id (profile within the tenant DB)
//	ActiveRoleID - tenant role id the caller switched to, if any (empty means
//	               all assigned roles apply — see /api/tenant/auth/switch-role)
//	Kind         - principal class: KindPortal for customer-portal sessions, empty
//	               for staff. Staff tokens carry no kind claim, so absence means
//	               staff and every token issued before the portal existed keeps
//	               working unchanged.
type UserContextPayload struct {
	ID           string
	Email        string
	TenantID     string
	UserID       string
	ActiveRoleID string
	Kind         string
}

const (
	// KindPortal is the `kind` JWT claim carried by customer-portal tokens.
	KindPortal = "portal"

	// PortalPathPrefix is the only route subtree a portal token may reach.
	PortalPathPrefix = "/api/portal/"
)

// RequireAuth is the HTTP middleware that verifies incoming JWT tokens and injects user context.
func RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		// Extract the JWT from the Authorization: Bearer header first.
		// If the header is absent, fall back to the httpOnly auth_token cookie
		// so clients that store the token in a cookie (safer against XSS) are
		// also supported. Both paths share the same validation logic below.
		var tokenString string
		authHeader := r.Header.Get("Authorization")
		if authHeader != "" {
			parts := strings.Split(authHeader, " ")
			if len(parts) != 2 || parts[0] != "Bearer" {
				w.WriteHeader(http.StatusUnauthorized)
				_ = json.NewEncoder(w).Encode(models.APIResponse{
					Success: false,
					Message: "Access denied. Authorization format must be: Bearer <token>",
				})
				return
			}
			tokenString = parts[1]
		} else if cookie, err := r.Cookie("auth_token"); err == nil && cookie.Value != "" {
			tokenString = cookie.Value
		} else {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(models.APIResponse{
				Success: false,
				Message: "Access denied. No authorization header provided.",
			})
			return
		}

		// Parse and verify token
		token, err := jwt.Parse(tokenString, func(t *jwt.Token) (interface{}, error) {
			// Ensure token signing method is HS256
			if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
				return nil, errors.New("unexpected signing method")
			}
			return []byte(config.AppConfig.JWTSecret), nil
		})

		if err != nil || !token.Valid {
			w.WriteHeader(http.StatusUnauthorized)
			message := "Authentication failed. Invalid or malformed token."
			if errors.Is(err, jwt.ErrTokenExpired) {
				message = "Authentication session expired. Please sign in again."
			}

			_ = json.NewEncoder(w).Encode(models.APIResponse{
				Success: false,
				Message: message,
			})
			return
		}

		// Extract claims
		claims, ok := token.Claims.(jwt.MapClaims)
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(models.APIResponse{
				Success: false,
				Message: "Authentication failed. Failed to parse claims.",
			})
			return
		}

		// Customer-portal tokens (principal_type=customer, see customer_auth.go)
		// carry a disjoint claim shape and must never authenticate a staff
		// route, even if the two token kinds ever share a signing key.
		if pt, _ := claims["principal_type"].(string); pt == CustomerPrincipalType {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(models.APIResponse{
				Success: false,
				Message: "Authentication failed. Invalid or malformed token.",
			})
			return
		}

		identityID, okID := claims["id"].(string)
		email, okEmail := claims["email"].(string)

		if !okID || !okEmail {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(models.APIResponse{
				Success: false,
				Message: "Authentication failed. Invalid token claims.",
			})
			return
		}

		// Multi-tenant claims (optional for legacy tokens; required for
		// tenant-scoped routes via TenantResolver).
		tenantID, _ := claims["tenant_id"].(string)
		userID, _ := claims["user_id"].(string)
		activeRoleID, _ := claims["active_role_id"].(string)
		kind, _ := claims["kind"].(string)

		// Structural containment of portal sessions.
		//
		// A customer-portal token is valid ONLY under /api/portal/. Enforcing it
		// here — at the single point every authenticated request passes through —
		// rather than per-route means a tenant or platform route added later
		// cannot forget the guard. Staff tokens carry no kind claim and are
		// unaffected; the portal chain applies RequirePortal for the converse.
		if kind == KindPortal && !strings.HasPrefix(r.URL.Path, PortalPathPrefix) {
			slog.Warn("security event",
				slog.String("security_event", "portal_token_outside_portal"),
				slog.String("request_id", RequestIDFromContext(r.Context())),
				slog.String("ip", ClientIP(r)),
				slog.String("identity", identityID),
				slog.String("path", r.URL.Path),
			)
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(models.APIResponse{
				Success: false,
				Message: "This session does not have access to this resource.",
			})
			return
		}

		// Double-submit CSRF check for state-changing requests. No-op unless
		// CookieSameSite is "none" — see csrf.go for why.
		if !csrfValid(r) {
			slog.Warn("security event",
				slog.String("security_event", "csrf_mismatch"),
				slog.String("request_id", RequestIDFromContext(r.Context())),
				slog.String("ip", ClientIP(r)),
				slog.String("path", r.URL.Path),
			)
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(models.APIResponse{
				Success: false,
				Message: "Request rejected: missing or invalid CSRF token.",
			})
			return
		}

		// Inject user context payload into request context
		ctxPayload := UserContextPayload{
			ID:           identityID,
			Email:        email,
			TenantID:     tenantID,
			UserID:       userID,
			ActiveRoleID: activeRoleID,
			Kind:         kind,
		}

		ctx := context.WithValue(r.Context(), UserContextKey, ctxPayload)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// GetUserFromContext extracts the authenticated UserContextPayload from a request context.
func GetUserFromContext(ctx context.Context) (UserContextPayload, error) {
	val := ctx.Value(UserContextKey)
	if val == nil {
		return UserContextPayload{}, errors.New("no user context found in request")
	}

	payload, ok := val.(UserContextPayload)
	if !ok {
		return UserContextPayload{}, errors.New("invalid user context payload type")
	}

	return payload, nil
}
