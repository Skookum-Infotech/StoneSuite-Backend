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

// CustomerPrincipalType is the "principal_type" JWT claim value that marks a
// token as an external-customer portal login rather than a staff session.
// RequireAuth rejects any token carrying it; RequireCustomerAuth requires it.
const CustomerPrincipalType = "customer"

const customerContextKey contextKey = "customerContext"

// CustomerContextPayload holds the authenticated customer metadata stored in
// request context by RequireCustomerAuth.
//
//	CustomerIdentityID - customer_identities.id (the portal login itself)
//	CustomerID          - the backing CRM customer record's internal id
//	TenantID             - control-plane tenant id, for tenant/pool resolution
type CustomerContextPayload struct {
	CustomerIdentityID string
	CustomerID         string
	TenantID           string
}

// RequireCustomerAuth is the HTTP middleware for the customer portal. It is
// structurally distinct from RequireAuth: it accepts only tokens carrying
// principal_type=customer, so a staff session can never reach a customer
// route and a customer session can never reach a staff route.
func RequireCustomerAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

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
		} else if cookie, err := r.Cookie("customer_auth_token"); err == nil && cookie.Value != "" {
			tokenString = cookie.Value
		} else {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(models.APIResponse{
				Success: false,
				Message: "Access denied. No authorization header provided.",
			})
			return
		}

		token, err := jwt.Parse(tokenString, func(t *jwt.Token) (interface{}, error) {
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

		claims, ok := token.Claims.(jwt.MapClaims)
		if !ok {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(models.APIResponse{
				Success: false,
				Message: "Authentication failed. Failed to parse claims.",
			})
			return
		}

		// Reject anything that isn't unambiguously a customer-portal token —
		// including a staff token that happens to carry no principal_type at
		// all — so this middleware never accidentally authenticates a staff
		// session as a customer.
		principalType, _ := claims["principal_type"].(string)
		customerIdentityID, okSub := claims["sub"].(string)
		customerID, okCustomerID := claims["customer_id"].(string)
		tenantID, okTenant := claims["tenant_id"].(string)
		if principalType != CustomerPrincipalType || !okSub || !okCustomerID || !okTenant {
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(models.APIResponse{
				Success: false,
				Message: "Authentication failed. Invalid token claims.",
			})
			return
		}

		if !csrfValid(r) {
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(models.APIResponse{
				Success: false,
				Message: "Request rejected: missing or invalid CSRF token.",
			})
			return
		}

		ctxPayload := CustomerContextPayload{
			CustomerIdentityID: customerIdentityID,
			CustomerID:         customerID,
			TenantID:           tenantID,
		}
		ctx := context.WithValue(r.Context(), customerContextKey, ctxPayload)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// GetCustomerFromContext extracts the authenticated CustomerContextPayload
// from a request context populated by RequireCustomerAuth.
func GetCustomerFromContext(ctx context.Context) (CustomerContextPayload, error) {
	val := ctx.Value(customerContextKey)
	if val == nil {
		return CustomerContextPayload{}, errors.New("no customer context found in request")
	}
	payload, ok := val.(CustomerContextPayload)
	if !ok {
		return CustomerContextPayload{}, errors.New("invalid customer context payload type")
	}
	return payload, nil
}
