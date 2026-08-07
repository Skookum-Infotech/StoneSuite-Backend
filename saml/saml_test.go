package saml

import (
	"encoding/pem"
	"net/url"
	"strings"
	"testing"

	dsig "github.com/russellhaering/goxmldsig"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testIDPKeyPair generates a fresh, real-clock-valid RSA key pair and
// self-signed certificate, returning the certificate as PEM (suitable for
// Config.IDPCertPEM) alongside the dsig key store (suitable for signing a
// response fixture as if this were the IdP's private key). Shared by
// saml_test.go and response_test.go.
func testIDPKeyPair(t *testing.T) (certPEM string, ks dsig.X509KeyStore) {
	t.Helper()

	ks = dsig.RandomKeyStoreForTest()
	_, der, err := ks.GetKeyPair()
	require.NoError(t, err)

	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return string(pemBytes), ks
}

func testConfig(t *testing.T) Config {
	certPEM, _ := testIDPKeyPair(t)
	return Config{
		SPEntityID:  "https://app.stonesuite.io/saml/cognito",
		ACSURL:      "https://app.stonesuite.io/api/auth/saml/cognito/acs",
		SLOURL:      "https://app.stonesuite.io/api/auth/saml/cognito/slo",
		IDPEntityID: "https://idp.example.com/saml",
		IDPSSOURL:   "https://idp.example.com/saml/sso",
		IDPSLOURL:   "https://idp.example.com/saml/slo",
		IDPCertPEM:  certPEM,
	}
}

func TestBuildAuthnRequestURL(t *testing.T) {
	t.Run("valid config produces a redirect url with request id", func(t *testing.T) {
		cfg := testConfig(t)

		redirectURL, requestID, err := BuildAuthnRequestURL(cfg, "relay-state-123")
		require.NoError(t, err)
		assert.NotEmpty(t, requestID)
		assert.True(t, strings.HasPrefix(redirectURL, cfg.IDPSSOURL), "redirect url %q must start with idp sso url %q", redirectURL, cfg.IDPSSOURL)
		assert.Contains(t, redirectURL, "SAMLRequest=")
		assert.Contains(t, redirectURL, "RelayState=")

		parsed, err := url.Parse(redirectURL)
		require.NoError(t, err)
		assert.Equal(t, "relay-state-123", parsed.Query().Get("RelayState"))
		assert.NotEmpty(t, parsed.Query().Get("SAMLRequest"))
	})

	t.Run("invalid idp certificate is an error", func(t *testing.T) {
		cfg := testConfig(t)
		cfg.IDPCertPEM = "not a certificate"

		_, _, err := BuildAuthnRequestURL(cfg, "relay")
		require.Error(t, err)
	})
}

func TestBuildLogoutRequestURL(t *testing.T) {
	t.Run("valid config with slo url produces a redirect url", func(t *testing.T) {
		cfg := testConfig(t)

		redirectURL, err := BuildLogoutRequestURL(cfg, "user@example.com", "session-index-1", "relay")
		require.NoError(t, err)
		assert.True(t, strings.HasPrefix(redirectURL, cfg.IDPSLOURL), "redirect url %q must start with idp slo url %q", redirectURL, cfg.IDPSLOURL)
		assert.Contains(t, redirectURL, "SAMLRequest=")
	})

	t.Run("empty idp slo url is an error", func(t *testing.T) {
		cfg := testConfig(t)
		cfg.IDPSLOURL = ""

		_, err := BuildLogoutRequestURL(cfg, "user@example.com", "session-index-1", "relay")
		require.Error(t, err)
	})
}
