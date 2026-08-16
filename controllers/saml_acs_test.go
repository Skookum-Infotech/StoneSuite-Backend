package controllers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"stonesuite-backend/saml"
)

func TestBestEffortNameFromAssertion(t *testing.T) {
	tests := []struct {
		name      string
		assertion *saml.ParsedAssertion
		want      string
	}{
		{
			name:      "single name attribute wins",
			assertion: &saml.ParsedAssertion{Email: "ada@example.com", Attributes: map[string]string{"name": "Ada Lovelace"}},
			want:      "Ada Lovelace",
		},
		{
			name:      "displayName attribute",
			assertion: &saml.ParsedAssertion{Email: "ada@example.com", Attributes: map[string]string{"displayName": "Ada L."}},
			want:      "Ada L.",
		},
		{
			name: "entra classic name claim",
			assertion: &saml.ParsedAssertion{
				Email:      "ada@example.com",
				Attributes: map[string]string{"http://schemas.xmlsoap.org/ws/2005/05/identity/claims/name": "Ada Entra"},
			},
			want: "Ada Entra",
		},
		{
			name: "given name and surname joined",
			assertion: &saml.ParsedAssertion{
				Email: "ada@example.com",
				Attributes: map[string]string{
					"http://schemas.xmlsoap.org/ws/2005/05/identity/claims/givenname": "Ada",
					"http://schemas.xmlsoap.org/ws/2005/05/identity/claims/surname":   "Lovelace",
				},
			},
			want: "Ada Lovelace",
		},
		{
			name: "given name only, no surname",
			assertion: &saml.ParsedAssertion{
				Email:      "ada@example.com",
				Attributes: map[string]string{"http://schemas.xmlsoap.org/ws/2005/05/identity/claims/givenname": "Ada"},
			},
			want: "Ada",
		},
		{
			name:      "falls back to email local part",
			assertion: &saml.ParsedAssertion{Email: "ada.lovelace@example.com", Attributes: map[string]string{}},
			want:      "ada.lovelace",
		},
		{
			name:      "email with no @ falls back to whole email",
			assertion: &saml.ParsedAssertion{Email: "not-an-email", Attributes: map[string]string{}},
			want:      "not-an-email",
		},
		{
			name: "single name attribute takes priority over given+surname",
			assertion: &saml.ParsedAssertion{
				Email: "ada@example.com",
				Attributes: map[string]string{
					"name": "Preferred Name",
					"http://schemas.xmlsoap.org/ws/2005/05/identity/claims/givenname": "Ada",
					"http://schemas.xmlsoap.org/ws/2005/05/identity/claims/surname":   "Lovelace",
				},
			},
			want: "Preferred Name",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, bestEffortNameFromAssertion(tt.assertion))
		})
	}
}

// TestSAMLErrorPage_StaticContent guards samlErrorPage's core security
// property: the function takes no request- or assertion-derived string, so
// its output is byte-identical regardless of the failure that triggered it
// -- there is no interpolation point an attacker-controlled SAML response
// could ever reach.
func TestSAMLErrorPage_StaticContent(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusForbidden, http.StatusInternalServerError} {
		rr := httptest.NewRecorder()
		samlErrorPage(rr, status)
		assert.Equal(t, status, rr.Code)
		assert.Equal(t, "text/html; charset=utf-8", rr.Header().Get("Content-Type"))
		assert.Equal(t, samlErrorPageHTML, rr.Body.String())
		assert.Contains(t, rr.Body.String(), "Sign-in failed")
	}
}

func TestSAMLAuthOps_ACS_UnknownProviderIsJSON404(t *testing.T) {
	h := &SAMLAuthOps{}
	// "okta" is no longer a fitting fixture here -- isValidSAMLProvider
	// accepts it as a well-formed custom slug (sso.go). "INVALID" fails the
	// slug pattern outright (uppercase), which is what this test means to
	// exercise: a provider that can never resolve, not one that's merely
	// unconfigured.
	req := httptest.NewRequest(http.MethodPost, "/api/auth/saml/INVALID/acs", strings.NewReader(""))
	req.SetPathValue("provider", "INVALID")
	rr := httptest.NewRecorder()
	h.ACS(rr, req)
	assert.Equal(t, http.StatusNotFound, rr.Code)
	assert.Contains(t, rr.Header().Get("Content-Type"), "application/json")
}

// TestSAMLAuthOps_ACS_MissingOrMalformedResponse exercises the two failure
// steps that happen before ACS ever touches the control plane (h.cp is nil
// on this handler and must never be dereferenced for these inputs).
func TestSAMLAuthOps_ACS_MissingOrMalformedResponse(t *testing.T) {
	h := &SAMLAuthOps{}

	t.Run("missing SAMLResponse renders the generic error page", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/saml/cognito/acs", strings.NewReader(""))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.SetPathValue("provider", "cognito")
		rr := httptest.NewRecorder()

		assert.NotPanics(t, func() { h.ACS(rr, req) })
		assert.Equal(t, http.StatusBadRequest, rr.Code)
		assert.Equal(t, samlErrorPageHTML, rr.Body.String())
	})

	t.Run("malformed base64 SAMLResponse renders the generic error page", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/saml/cognito/acs", strings.NewReader("SAMLResponse=not-valid-base64%21%21%21"))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.SetPathValue("provider", "cognito")
		rr := httptest.NewRecorder()

		assert.NotPanics(t, func() { h.ACS(rr, req) })
		assert.Equal(t, http.StatusBadRequest, rr.Code)
		assert.Equal(t, samlErrorPageHTML, rr.Body.String())
	})

	t.Run("well-formed base64 but non-SAML xml renders the generic error page", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodPost, "/api/auth/saml/cognito/acs", strings.NewReader("SAMLResponse=PG5vdC1zYW1sLz4="))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.SetPathValue("provider", "cognito")
		rr := httptest.NewRecorder()

		assert.NotPanics(t, func() { h.ACS(rr, req) })
		assert.Equal(t, http.StatusBadRequest, rr.Code)
		assert.Equal(t, samlErrorPageHTML, rr.Body.String())
	})
}
