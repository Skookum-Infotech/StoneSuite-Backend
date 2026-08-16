package controllers

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"stonesuite-backend/config"
)

// withSAMLTestURLs sets config.AppConfig.SAMLSPEntityID/APIBaseURL for the
// duration of the test and restores the originals on cleanup, mirroring the
// save/restore pattern established in middleware/middleware_test.go.
func withSAMLTestURLs(t *testing.T, spEntityID, apiBaseURL string) {
	t.Helper()
	origEntity := config.AppConfig.SAMLSPEntityID
	origBase := config.AppConfig.APIBaseURL
	config.AppConfig.SAMLSPEntityID = spEntityID
	config.AppConfig.APIBaseURL = apiBaseURL
	t.Cleanup(func() {
		config.AppConfig.SAMLSPEntityID = origEntity
		config.AppConfig.APIBaseURL = origBase
	})
}

func TestSpConfig(t *testing.T) {
	tests := []struct {
		name       string
		spEntityID string
		apiBaseURL string
		provider   string
		wantEntity string
		wantACS    string
		wantSLO    string
	}{
		{
			name:       "no trailing slashes",
			spEntityID: "https://app.stonesuite.io/saml",
			apiBaseURL: "https://api.stonesuite.io",
			provider:   "cognito",
			wantEntity: "https://app.stonesuite.io/saml/cognito/metadata",
			wantACS:    "https://api.stonesuite.io/api/auth/saml/cognito/acs",
			wantSLO:    "https://api.stonesuite.io/api/auth/saml/cognito/logout-response",
		},
		{
			name:       "trailing slashes are trimmed",
			spEntityID: "https://app.stonesuite.io/saml/",
			apiBaseURL: "https://api.stonesuite.io/",
			provider:   "entra",
			wantEntity: "https://app.stonesuite.io/saml/entra/metadata",
			wantACS:    "https://api.stonesuite.io/api/auth/saml/entra/acs",
			wantSLO:    "https://api.stonesuite.io/api/auth/saml/entra/logout-response",
		},
		{
			name:       "local dev base url",
			spEntityID: "https://app.stonesuite.io/saml",
			apiBaseURL: "http://localhost:8080",
			provider:   "cognito",
			wantEntity: "https://app.stonesuite.io/saml/cognito/metadata",
			wantACS:    "http://localhost:8080/api/auth/saml/cognito/acs",
			wantSLO:    "http://localhost:8080/api/auth/saml/cognito/logout-response",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withSAMLTestURLs(t, tt.spEntityID, tt.apiBaseURL)
			gotEntity, gotACS, gotSLO := spConfig(tt.provider)
			assert.Equal(t, tt.wantEntity, gotEntity)
			assert.Equal(t, tt.wantACS, gotACS)
			assert.Equal(t, tt.wantSLO, gotSLO)
		})
	}
}

func TestSAMLAuthOps_Metadata(t *testing.T) {
	withSAMLTestURLs(t, "https://app.stonesuite.io/saml", "https://api.stonesuite.io")
	h := &SAMLAuthOps{}

	t.Run("unknown provider is 404", func(t *testing.T) {
		// "okta" is now a valid custom SAML slug (sso.go's isValidSAMLProvider);
		// "INVALID" fails the slug pattern outright, which is what "unknown"
		// means here -- a provider that can never resolve.
		req := httptest.NewRequest(http.MethodGet, "/api/auth/saml/INVALID/metadata", nil)
		req.SetPathValue("provider", "INVALID")
		rr := httptest.NewRecorder()
		h.Metadata(rr, req)
		assert.Equal(t, http.StatusNotFound, rr.Code)
	})

	t.Run("known provider returns sp metadata xml", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/auth/saml/cognito/metadata", nil)
		req.SetPathValue("provider", "cognito")
		rr := httptest.NewRecorder()
		h.Metadata(rr, req)
		assert.Equal(t, http.StatusOK, rr.Code)
		assert.Equal(t, "application/samlmetadata+xml", rr.Header().Get("Content-Type"))
		body := rr.Body.String()
		assert.Contains(t, body, "https://app.stonesuite.io/saml/cognito/metadata")
		assert.Contains(t, body, "https://api.stonesuite.io/api/auth/saml/cognito/acs")
		assert.True(t, strings.Contains(body, "EntityDescriptor"))
	})
}

func TestSAMLAuthOps_SPInfo(t *testing.T) {
	withSAMLTestURLs(t, "https://app.stonesuite.io/saml", "https://api.stonesuite.io")
	h := &SAMLAuthOps{}

	t.Run("unknown provider is 404", func(t *testing.T) {
		// See the equivalent Metadata case above for why "okta" no longer fits.
		req := httptest.NewRequest(http.MethodGet, "/api/auth/saml/INVALID/sp-info", nil)
		req.SetPathValue("provider", "INVALID")
		rr := httptest.NewRecorder()
		h.SPInfo(rr, req)
		assert.Equal(t, http.StatusNotFound, rr.Code)
	})

	t.Run("known provider returns sp info json", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/auth/saml/entra/sp-info", nil)
		req.SetPathValue("provider", "entra")
		rr := httptest.NewRecorder()
		h.SPInfo(rr, req)
		assert.Equal(t, http.StatusOK, rr.Code)
		assert.Equal(t, "application/json", rr.Header().Get("Content-Type"))
		body := rr.Body.String()
		assert.Contains(t, body, `"provider":"entra"`)
		assert.Contains(t, body, `"sp_entity_id":"https://app.stonesuite.io/saml/entra/metadata"`)
		assert.Contains(t, body, `"acs_url":"https://api.stonesuite.io/api/auth/saml/entra/acs"`)
		assert.Contains(t, body, `"slo_url":"https://api.stonesuite.io/api/auth/saml/entra/logout-response"`)
	})
}

func TestSafeReturnTo(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want bool
	}{
		{"empty is safe (no return_to supplied)", "", true},
		{"relative path is safe", "/crm/records/123", true},
		{"relative path with query is safe", "/dashboard?tab=recent", true},
		{"absolute url with scheme is unsafe", "https://evil.com/phish", false},
		{"scheme-relative url is unsafe", "//evil.com/phish", false},
		{"backslash-prefixed is unsafe", "\\\\evil.com", false},
		{"backslash embedded is unsafe", "/ok\\..\\evil", false},
		{"bare host without leading slash is unsafe", "evil.com", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, safeReturnTo(tt.in))
		})
	}
}

func TestSAMLAuthOps_Initiate_ValidatesProviderAndTenantParams(t *testing.T) {
	h := &SAMLAuthOps{}

	t.Run("unknown provider is 404", func(t *testing.T) {
		// "okta" is now a valid custom SAML slug, so it no longer short-circuits
		// before h.cp is touched -- "INVALID" fails the slug pattern and still
		// 404s at the very first check, matching this subtest's nil h.cp setup.
		req := httptest.NewRequest(http.MethodGet, "/api/auth/saml/INVALID/initiate?tenant_slug=acme", nil)
		req.SetPathValue("provider", "INVALID")
		rr := httptest.NewRecorder()
		h.Initiate(rr, req)
		assert.Equal(t, http.StatusNotFound, rr.Code)
	})

	t.Run("neither tenant_slug nor tenant_id is 400", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/auth/saml/cognito/initiate", nil)
		req.SetPathValue("provider", "cognito")
		rr := httptest.NewRecorder()
		h.Initiate(rr, req)
		assert.Equal(t, http.StatusBadRequest, rr.Code)
	})

	t.Run("both tenant_slug and tenant_id is 400", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/api/auth/saml/cognito/initiate?tenant_slug=acme&tenant_id=11111111-1111-1111-1111-111111111111", nil)
		req.SetPathValue("provider", "cognito")
		rr := httptest.NewRecorder()
		h.Initiate(rr, req)
		assert.Equal(t, http.StatusBadRequest, rr.Code)
	})
}
