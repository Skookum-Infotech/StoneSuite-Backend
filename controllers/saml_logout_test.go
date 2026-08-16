package controllers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSAMLAuthOps_Logout_UnknownProviderIs404(t *testing.T) {
	h := &SAMLAuthOps{}
	// "okta" is now a valid custom SAML slug (sso.go's isValidSAMLProvider);
	// "INVALID" fails the slug pattern outright, which is what "unknown"
	// means here.
	req := httptest.NewRequest(http.MethodPost, "/api/auth/saml/INVALID/logout", nil)
	req.SetPathValue("provider", "INVALID")
	rr := httptest.NewRecorder()
	h.Logout(rr, req)
	assert.Equal(t, http.StatusNotFound, rr.Code)
}

// TestSAMLAuthOps_Logout_RequiresAuth confirms the auth check runs (and
// fails closed) before Logout ever touches h.cp, which is nil on this
// handler -- a bug that reordered the check after a control-plane call
// would panic this test instead of just failing an assertion.
func TestSAMLAuthOps_Logout_RequiresAuth(t *testing.T) {
	h := &SAMLAuthOps{}
	req := httptest.NewRequest(http.MethodPost, "/api/auth/saml/cognito/logout", nil)
	req.SetPathValue("provider", "cognito")
	rr := httptest.NewRecorder()
	assert.NotPanics(t, func() { h.Logout(rr, req) })
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}

func TestSAMLAuthOps_LogoutResponse(t *testing.T) {
	h := &SAMLAuthOps{}

	t.Run("unknown provider is 404", func(t *testing.T) {
		// See TestSAMLAuthOps_Logout_UnknownProviderIs404 for why "okta" no
		// longer fits.
		req := httptest.NewRequest(http.MethodGet, "/api/auth/saml/INVALID/logout-response", nil)
		req.SetPathValue("provider", "INVALID")
		rr := httptest.NewRecorder()
		h.LogoutResponse(rr, req)
		assert.Equal(t, http.StatusNotFound, rr.Code)
	})

	t.Run("known provider redirects to frontend regardless of query params", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/auth/saml/cognito/logout-response?SAMLResponse=anything", nil)
		req.SetPathValue("provider", "cognito")
		rr := httptest.NewRecorder()
		assert.NotPanics(t, func() { h.LogoutResponse(rr, req) })
		assert.Equal(t, http.StatusFound, rr.Code)
		assert.Contains(t, rr.Header().Get("Location"), "/auth/login")
	})
}
