package saml

import (
	"bytes"
	"compress/flate"
	"encoding/base64"
	"io"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var testNow = time.Date(2026, 11, 19, 12, 0, 0, 0, time.UTC)

func TestParseLogoutRequestPOST(t *testing.T) {
	idp, other := newTestIdP(t), newTestIdP(t)
	cfg := idp.config()

	t.Run("accepts a signed request and returns only what it asks for", func(t *testing.T) {
		got, err := ParseLogoutRequestPOST(cfg, idp.post(t, logoutSpec{id: "_abc", nameID: "alice@example.com"}, testNow, true), testNow)
		require.NoError(t, err)
		assert.Equal(t, "_abc", got.ID)
		assert.Equal(t, testIdPIssuer, got.Issuer)
		assert.Equal(t, "alice@example.com", got.NameID)
		assert.Equal(t, testNow.Add(MaxLogoutRequestAge), got.ExpiresAt)
	})

	tests := []struct {
		name string
		spec logoutSpec
		sign bool
		from *testIdP
		now  time.Time
		want error
	}{
		{"unsigned", logoutSpec{}, false, idp, testNow, ErrLogoutNotSigned},
		{"signed by a different key", logoutSpec{}, true, other, testNow, ErrLogoutBadSignature},
		{"wrong issuer", logoutSpec{issuer: "https://evil.example.com"}, true, idp, testNow, ErrLogoutWrongIssuer},
		{"addressed to another service", logoutSpec{destination: "https://other.example.com/slo"}, true, idp, testNow, ErrLogoutWrongTarget},
		{"no destination", logoutSpec{noDestination: true}, true, idp, testNow, ErrLogoutWrongTarget},
		{"issued too long ago", logoutSpec{issueInstant: testNow.Add(-MaxLogoutRequestAge - time.Minute)}, true, idp, testNow, ErrLogoutExpired},
		{"issued in the future", logoutSpec{issueInstant: testNow.Add(MaxLogoutClockSkew + time.Minute)}, true, idp, testNow, ErrLogoutExpired},
		{"names no one", logoutSpec{noNameID: true}, true, idp, testNow, ErrLogoutNoPrincipal},
	}
	for _, tc := range tests {
		t.Run("refuses a request that is "+tc.name, func(t *testing.T) {
			_, err := ParseLogoutRequestPOST(cfg, tc.from.post(t, tc.spec, tc.now, tc.sign), tc.now)
			require.Error(t, err)
			assert.ErrorIs(t, err, tc.want)
		})
	}

	t.Run("refuses a signed request whose principal was changed afterwards", func(t *testing.T) {
		encoded := idp.post(t, logoutSpec{nameID: "alice@example.com"}, testNow, true)
		raw, err := base64.StdEncoding.DecodeString(encoded)
		require.NoError(t, err)
		tampered := strings.Replace(string(raw), "alice@example.com", "bob@example.com", 1)
		_, err = ParseLogoutRequestPOST(cfg, base64.StdEncoding.EncodeToString([]byte(tampered)), testNow)
		assert.ErrorIs(t, err, ErrLogoutBadSignature)
	})

	t.Run("refuses input that is not a logout request", func(t *testing.T) {
		for _, in := range []string{"", "!!not base64!!", base64.StdEncoding.EncodeToString([]byte("<not-xml"))} {
			_, err := ParseLogoutRequestPOST(cfg, in, testNow)
			assert.Error(t, err, in)
		}
	})
}

func TestParseLogoutRequestRedirect(t *testing.T) {
	idp, other := newTestIdP(t), newTestIdP(t)
	cfg := idp.config()

	t.Run("accepts a signed request, with and without RelayState", func(t *testing.T) {
		for _, relay := range []string{"", "/dashboard?x=1&y=2"} {
			got, err := ParseLogoutRequestRedirect(cfg, idp.redirect(t, logoutSpec{id: "_r1"}, testNow, relay, sigAlgRSASHA256, true), testNow)
			require.NoError(t, err, relay)
			assert.Equal(t, "_r1", got.ID)
			assert.Equal(t, "user-123", got.NameID)
		}
	})

	t.Run("honours NotOnOrAfter", func(t *testing.T) {
		soon := testNow.Add(time.Minute).Format(time.RFC3339)
		got, err := ParseLogoutRequestRedirect(cfg, idp.redirect(t, logoutSpec{notOnOrAfter: soon}, testNow, "", sigAlgRSASHA256, true), testNow)
		require.NoError(t, err)
		assert.Equal(t, testNow.Add(time.Minute), got.ExpiresAt)

		past := testNow.Add(-time.Second).Format(time.RFC3339)
		_, err = ParseLogoutRequestRedirect(cfg, idp.redirect(t, logoutSpec{notOnOrAfter: past}, testNow, "", sigAlgRSASHA256, true), testNow)
		assert.ErrorIs(t, err, ErrLogoutExpired)
	})

	tests := []struct {
		name  string
		build func(t *testing.T) string
		want  error
	}{
		{"unsigned", func(t *testing.T) string { return idp.redirect(t, logoutSpec{}, testNow, "", sigAlgRSASHA256, false) }, ErrLogoutNotSigned},
		{"signed by a different key", func(t *testing.T) string { return other.redirect(t, logoutSpec{}, testNow, "", sigAlgRSASHA256, true) }, ErrLogoutBadSignature},
		{"signed with the weak SHA-1 algorithm", func(t *testing.T) string {
			return idp.redirect(t, logoutSpec{}, testNow, "", "http://www.w3.org/2000/09/xmldsig#rsa-sha1", true)
		}, errUnsupportedSigAlg},
		{"wrong issuer", func(t *testing.T) string {
			return idp.redirect(t, logoutSpec{issuer: "https://evil.example.com"}, testNow, "", sigAlgRSASHA256, true)
		}, ErrLogoutWrongIssuer},
		{"addressed to another service", func(t *testing.T) string {
			return idp.redirect(t, logoutSpec{destination: "https://other.example.com/slo"}, testNow, "", sigAlgRSASHA256, true)
		}, ErrLogoutWrongTarget},
		{"expired", func(t *testing.T) string {
			return idp.redirect(t, logoutSpec{issueInstant: testNow.Add(-time.Hour)}, testNow, "", sigAlgRSASHA256, true)
		}, ErrLogoutExpired},
		{"changed after signing (RelayState)", func(t *testing.T) string {
			q := idp.redirect(t, logoutSpec{}, testNow, "/safe", sigAlgRSASHA256, true)
			return strings.Replace(q, "RelayState="+url.QueryEscape("/safe"), "RelayState="+url.QueryEscape("https://evil.example.com"), 1)
		}, ErrLogoutBadSignature},
		{"carrying a second SAMLRequest", func(t *testing.T) string {
			q := idp.redirect(t, logoutSpec{}, testNow, "", sigAlgRSASHA256, true)
			return q + "&SAMLRequest=" + url.QueryEscape("AAAA")
		}, ErrLogoutMalformed},
		{"swapping in a different signed request's body", func(t *testing.T) string {
			a := idp.redirect(t, logoutSpec{nameID: "alice"}, testNow, "", sigAlgRSASHA256, true)
			b := idp.redirect(t, logoutSpec{nameID: "bob"}, testNow, "", sigAlgRSASHA256, true)
			aReq := strings.SplitN(a, "&", 2)[0]
			return aReq + "&" + strings.SplitN(b, "&", 2)[1]
		}, ErrLogoutBadSignature},
	}
	for _, tc := range tests {
		t.Run("refuses a request that is "+tc.name, func(t *testing.T) {
			_, err := ParseLogoutRequestRedirect(cfg, tc.build(t), testNow)
			require.Error(t, err)
			assert.ErrorIs(t, err, tc.want)
		})
	}

	t.Run("refuses a query with no SAMLRequest", func(t *testing.T) {
		for _, q := range []string{"", "RelayState=x", "SigAlg=a&Signature=b"} {
			_, err := ParseLogoutRequestRedirect(cfg, q, testNow)
			assert.Error(t, err, q)
		}
	})
}

func TestUnverifiedLogoutIssuer(t *testing.T) {
	idp := newTestIdP(t)

	t.Run("reads the issuer from either binding", func(t *testing.T) {
		got, err := UnverifiedLogoutIssuer(idp.post(t, logoutSpec{}, testNow, false), false)
		require.NoError(t, err)
		assert.Equal(t, testIdPIssuer, got)

		q := idp.redirect(t, logoutSpec{issuer: "https://other.example.com/entity"}, testNow, "", sigAlgRSASHA256, false)
		param, err := url.QueryUnescape(strings.TrimPrefix(strings.SplitN(q, "&", 2)[0], "SAMLRequest="))
		require.NoError(t, err)
		got, err = UnverifiedLogoutIssuer(param, true)
		require.NoError(t, err)
		assert.Equal(t, "https://other.example.com/entity", got)
	})

	t.Run("errors on garbage and on something that is not a LogoutRequest", func(t *testing.T) {
		for _, in := range []string{"", "!!", base64.StdEncoding.EncodeToString([]byte("<a/>"))} {
			_, err := UnverifiedLogoutIssuer(in, false)
			assert.Error(t, err, in)
		}
	})

	t.Run("a redirect request inflating past the cap is refused", func(t *testing.T) {
		var buf bytes.Buffer
		fw, err := flate.NewWriter(&buf, flate.BestCompression)
		require.NoError(t, err)
		_, err = fw.Write(bytes.Repeat([]byte("A"), maxLogoutRequestBytes+10))
		require.NoError(t, err)
		require.NoError(t, fw.Close())
		_, err = UnverifiedLogoutIssuer(base64.StdEncoding.EncodeToString(buf.Bytes()), true)
		assert.ErrorIs(t, err, ErrLogoutMalformed)
	})
}

func TestBuildLogoutResponseURL(t *testing.T) {
	cfg := newTestIdP(t).config()

	t.Run("answers the request with Success, echoing RelayState", func(t *testing.T) {
		raw, err := BuildLogoutResponseURL(cfg, "_req-1", "/after")
		require.NoError(t, err)
		u, err := url.Parse(raw)
		require.NoError(t, err)
		assert.Equal(t, "idp.example.com", u.Host)
		assert.Equal(t, "/after", u.Query().Get("RelayState"))
		assert.Empty(t, u.Query().Get("SAMLRequest"))

		b, err := base64.StdEncoding.DecodeString(u.Query().Get("SAMLResponse"))
		require.NoError(t, err)
		xmlText, err := io.ReadAll(flate.NewReader(bytes.NewReader(b)))
		require.NoError(t, err)
		assert.Contains(t, string(xmlText), `InResponseTo="_req-1"`)
		assert.Contains(t, string(xmlText), "urn:oasis:names:tc:SAML:2.0:status:Success")
		assert.Contains(t, string(xmlText), `Destination="https://idp.example.com/slo"`)
	})

	t.Run("is not possible when the IdP has no logout endpoint", func(t *testing.T) {
		cfg.IDPSLOURL = ""
		_, err := BuildLogoutResponseURL(cfg, "_req-1", "")
		assert.Error(t, err)
	})
}
