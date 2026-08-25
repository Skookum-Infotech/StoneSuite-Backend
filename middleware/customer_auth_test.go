package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"stonesuite-backend/config"
)

func signTestToken(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(config.AppConfig.JWTSecret))
	require.NoError(t, err)
	return token
}

func withTestJWTSecret(t *testing.T) {
	t.Helper()
	orig := config.AppConfig.JWTSecret
	config.AppConfig.JWTSecret = "test-secret"
	t.Cleanup(func() { config.AppConfig.JWTSecret = orig })
}

func customerClaims(extra jwt.MapClaims) jwt.MapClaims {
	c := jwt.MapClaims{
		"principal_type": CustomerPrincipalType,
		"sub":            "identity-1",
		"customer_id":    "42",
		"tenant_id":      "tenant-1",
		"exp":            time.Now().Add(time.Hour).Unix(),
		"iat":            time.Now().Unix(),
	}
	for k, v := range extra {
		c[k] = v
	}
	return c
}

func staffClaims(extra jwt.MapClaims) jwt.MapClaims {
	c := jwt.MapClaims{
		"id": "identity-1", "email": "user@example.com", "tenant_id": "tenant-1",
		"exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Unix(),
	}
	for k, v := range extra {
		c[k] = v
	}
	return c
}

func TestRequireCustomerAuth_ValidTokenExtractsPayload(t *testing.T) {
	withTestJWTSecret(t)

	var captured CustomerContextPayload
	handler := RequireCustomerAuth(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		p, err := GetCustomerFromContext(r.Context())
		require.NoError(t, err)
		captured = p
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/customer/notes", nil)
	req.Header.Set("Authorization", "Bearer "+signTestToken(t, customerClaims(nil)))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, "identity-1", captured.CustomerIdentityID)
	assert.Equal(t, "42", captured.CustomerID)
	assert.Equal(t, "tenant-1", captured.TenantID)
}

// TestRequireCustomerAuth_RejectsStaffToken is the single most important
// regression test in the customer-note feature: a staff session must never
// be able to reach a customer-portal route, even though both token kinds are
// signed with the same JWTSecret.
func TestRequireCustomerAuth_RejectsStaffToken(t *testing.T) {
	withTestJWTSecret(t)

	called := false
	handler := RequireCustomerAuth(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/customer/notes", nil)
	req.Header.Set("Authorization", "Bearer "+signTestToken(t, staffClaims(nil)))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.False(t, called, "handler must not be reached with a staff token")
}

// TestRequireAuth_RejectsCustomerToken is the mirror image: a customer
// portal session must never authenticate a staff route.
func TestRequireAuth_RejectsCustomerToken(t *testing.T) {
	withTestJWTSecret(t)

	called := false
	handler := RequireAuth(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		called = true
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/tenant/me", nil)
	req.Header.Set("Authorization", "Bearer "+signTestToken(t, customerClaims(nil)))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.False(t, called, "handler must not be reached with a customer token")
}

func TestRequireCustomerAuth_MissingPrincipalTypeRejected(t *testing.T) {
	withTestJWTSecret(t)

	handler := RequireCustomerAuth(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler must not be reached")
	}))

	claims := customerClaims(nil)
	delete(claims, "principal_type")
	req := httptest.NewRequest(http.MethodGet, "/api/customer/notes", nil)
	req.Header.Set("Authorization", "Bearer "+signTestToken(t, claims))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestRequireCustomerAuth_ExpiredTokenRejected(t *testing.T) {
	withTestJWTSecret(t)

	handler := RequireCustomerAuth(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler must not be reached")
	}))

	claims := customerClaims(jwt.MapClaims{"exp": time.Now().Add(-time.Hour).Unix()})
	req := httptest.NewRequest(http.MethodGet, "/api/customer/notes", nil)
	req.Header.Set("Authorization", "Bearer "+signTestToken(t, claims))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}

func TestRequireCustomerAuth_NoTokenRejected(t *testing.T) {
	withTestJWTSecret(t)

	handler := RequireCustomerAuth(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler must not be reached")
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/customer/notes", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
}
