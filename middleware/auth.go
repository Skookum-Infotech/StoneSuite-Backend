package middleware

import (
	"context"
	"encoding/json"
	"errors"
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
type UserContextPayload struct {
	ID           string
	Email        string
	TenantID     string
	UserID       string
	ActiveRoleID string
}

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

		// Inject user context payload into request context
		ctxPayload := UserContextPayload{
			ID:           identityID,
			Email:        email,
			TenantID:     tenantID,
			UserID:       userID,
			ActiveRoleID: activeRoleID,
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
