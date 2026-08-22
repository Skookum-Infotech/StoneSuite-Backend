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

const testJWTSecret = "portal-containment-test-secret"

// signToken mints a token with the given claims for the test secret.
func signToken(t *testing.T, claims jwt.MapClaims) string {
	t.Helper()
	claims["exp"] = time.Now().Add(time.Hour).Unix()
	claims["iat"] = time.Now().Unix()
	s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).
		SignedString([]byte(testJWTSecret))
	require.NoError(t, err)
	return s
}

func staffToken(t *testing.T) string {
	// Staff tokens carry NO kind claim — that is what "absent means staff"
	// relies on, and why every token minted before the portal existed keeps
	// working.
	return signToken(t, jwt.MapClaims{
		"id": "identity-staff", "email": "staff@example.com", "tenant_id": "tenant-1",
	})
}

func portalToken(t *testing.T) string {
	return signToken(t, jwt.MapClaims{
		"id": "identity-portal", "email": "buyer@example.com", "tenant_id": "tenant-1",
		"kind": KindPortal,
	})
}

// reached records whether the wrapped handler ran.
func reachedHandler(hit *bool) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*hit = true
		w.WriteHeader(http.StatusOK)
	})
}

func withSecret(t *testing.T) {
	t.Helper()
	prev := config.AppConfig.JWTSecret
	config.AppConfig.JWTSecret = testJWTSecret
	t.Cleanup(func() { config.AppConfig.JWTSecret = prev })
}

// A portal token must be inert on every route outside /api/portal/.
//
// The guard lives inside RequireAuth rather than on a per-route chain, so this
// covers every authenticated route in the application — including ones added
// later, which is the point. The paths below are a representative sample of
// each route family, not an exhaustive list.
func TestPortalTokenRejectedOutsidePortal(t *testing.T) {
	withSecret(t)

	internalPaths := []string{
		"/api/tenant/invoices",
		"/api/tenant/invoices/some-uuid",
		"/api/tenant/users",
		"/api/tenant/users/me/permissions",
		"/api/tenant/roles",
		"/api/tenant/auth/switch-role",
		"/api/tenant/crm/customer/records",
		"/api/tenant/sales-orders",
		"/api/tenant/me",
		"/api/platform/tenants",
		"/api/platform/invites",
		// A path that merely starts with the same letters must not slip through
		// a prefix check.
		"/api/portalX/invoices",
		"/api/portal",
	}

	for _, path := range internalPaths {
		t.Run(path, func(t *testing.T) {
			var hit bool
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Header.Set("Authorization", "Bearer "+portalToken(t))

			RequireAuth(reachedHandler(&hit)).ServeHTTP(rec, req)

			assert.Equal(t, http.StatusForbidden, rec.Code,
				"a portal token must not be accepted on %s", path)
			assert.False(t, hit, "handler for %s must never run for a portal token", path)
		})
	}
}

// The converse: the portal tree itself must accept portal tokens.
func TestPortalTokenAcceptedInsidePortal(t *testing.T) {
	withSecret(t)

	portalPaths := []string{
		"/api/portal/invoices",
		"/api/portal/invoices/some-uuid",
		"/api/portal/sales-orders",
		"/api/portal/me",
		"/api/portal/workspaces",
	}
	for _, path := range portalPaths {
		t.Run(path, func(t *testing.T) {
			var hit bool
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, path, nil)
			req.Header.Set("Authorization", "Bearer "+portalToken(t))

			RequireAuth(reachedHandler(&hit)).ServeHTTP(rec, req)

			assert.Equal(t, http.StatusOK, rec.Code)
			assert.True(t, hit, "portal handler for %s should run", path)
		})
	}
}

// Staff tokens are unaffected by the containment rule on internal routes.
func TestStaffTokenUnaffectedOnInternalRoutes(t *testing.T) {
	withSecret(t)

	var hit bool
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/tenant/invoices", nil)
	req.Header.Set("Authorization", "Bearer "+staffToken(t))

	RequireAuth(reachedHandler(&hit)).ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, hit)
}

// RequirePortal is the other half of the wall: staff tokens must not reach
// portal handlers either. It fails closed on a missing kind claim, so a staff
// token (which has none) is refused.
func TestRequirePortalRejectsStaffTokens(t *testing.T) {
	withSecret(t)

	var hit bool
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/portal/invoices", nil)
	req.Header.Set("Authorization", "Bearer "+staffToken(t))

	RequireAuth(RequirePortal(reachedHandler(&hit))).ServeHTTP(rec, req)

	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.False(t, hit, "a staff token must not reach a portal handler")
}

func TestRequirePortalAcceptsPortalTokens(t *testing.T) {
	withSecret(t)

	var hit bool
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/portal/invoices", nil)
	req.Header.Set("Authorization", "Bearer "+portalToken(t))

	RequireAuth(RequirePortal(reachedHandler(&hit))).ServeHTTP(rec, req)

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.True(t, hit)
}

// An unauthenticated request must not reach a portal handler.
func TestRequirePortalRejectsAnonymous(t *testing.T) {
	var hit bool
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/portal/invoices", nil)

	RequirePortal(reachedHandler(&hit)).ServeHTTP(rec, req)

	assert.Equal(t, http.StatusUnauthorized, rec.Code)
	assert.False(t, hit)
}

// The Kind claim must survive into the request context, since RequirePortal and
// the handlers read it from there rather than re-parsing the token.
func TestKindClaimReachesContext(t *testing.T) {
	withSecret(t)

	var got UserContextPayload
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, _ = GetUserFromContext(r.Context())
	})

	req := httptest.NewRequest(http.MethodGet, "/api/portal/me", nil)
	req.Header.Set("Authorization", "Bearer "+portalToken(t))
	RequireAuth(h).ServeHTTP(httptest.NewRecorder(), req)
	assert.Equal(t, KindPortal, got.Kind)
	assert.Equal(t, "tenant-1", got.TenantID)

	req = httptest.NewRequest(http.MethodGet, "/api/tenant/invoices", nil)
	req.Header.Set("Authorization", "Bearer "+staffToken(t))
	RequireAuth(h).ServeHTTP(httptest.NewRecorder(), req)
	assert.Empty(t, got.Kind, "staff tokens carry no kind claim")
}
