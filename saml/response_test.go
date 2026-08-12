package saml

import (
	"encoding/base64"
	"fmt"
	"testing"
	"time"

	"github.com/beevik/etree"
	saml2 "github.com/russellhaering/gosaml2"
	"github.com/russellhaering/gosaml2/types"
	dsig "github.com/russellhaering/goxmldsig"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExtractEmail(t *testing.T) {
	tests := []struct {
		name         string
		nameID       string
		nameIDFormat string
		values       saml2.Values
		want         string
		wantErr      bool
	}{
		{
			name:   "nameid is an email",
			nameID: "user@example.com",
			want:   "user@example.com",
		},
		{
			name:   "nameid with format still used directly when it contains @",
			nameID: "user@example.com",
			values: buildValues(t, "mail", "other@example.com"),
			want:   "user@example.com",
		},
		{
			name:   "falls back to adfs/entra classic claim",
			nameID: "opaque-subject-id",
			values: buildValues(t, "http://schemas.xmlsoap.org/ws/2005/05/identity/claims/emailaddress", "claims@example.com"),
			want:   "claims@example.com",
		},
		{
			name:   "falls back to email attribute",
			nameID: "opaque-subject-id",
			values: buildValues(t, "email", "email-attr@example.com"),
			want:   "email-attr@example.com",
		},
		{
			name:   "falls back to mail attribute",
			nameID: "opaque-subject-id",
			values: buildValues(t, "mail", "mail-attr@example.com"),
			want:   "mail-attr@example.com",
		},
		{
			name:   "falls back to emailaddress attribute",
			nameID: "opaque-subject-id",
			values: buildValues(t, "emailaddress", "emailaddress-attr@example.com"),
			want:   "emailaddress-attr@example.com",
		},
		{
			name:   "falls back to legacy claims schema attribute",
			nameID: "opaque-subject-id",
			values: buildValues(t, "http://schemas.xmlsoap.org/claims/EmailAddress", "legacy-claim@example.com"),
			want:   "legacy-claim@example.com",
		},
		{
			name:    "no email found anywhere is an error",
			nameID:  "opaque-subject-id",
			values:  buildValues(t, "unrelated-attribute", "some-value"),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := extractEmail(tt.nameID, tt.nameIDFormat, tt.values)
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestParseAndValidateResponse(t *testing.T) {
	t.Run("malformed base64 is an error", func(t *testing.T) {
		cfg := testConfig(t)
		_, err := ParseAndValidateResponse(cfg, "not-valid-base64!!!")
		require.Error(t, err)
	})

	t.Run("malformed xml is an error", func(t *testing.T) {
		cfg := testConfig(t)
		encoded := base64.StdEncoding.EncodeToString([]byte("<not-xml"))
		_, err := ParseAndValidateResponse(cfg, encoded)
		require.Error(t, err)
	})

	t.Run("invalid idp certificate is an error", func(t *testing.T) {
		cfg := testConfig(t)
		cfg.IDPCertPEM = "not a certificate"
		encoded := base64.StdEncoding.EncodeToString([]byte("<anything/>"))
		_, err := ParseAndValidateResponse(cfg, encoded)
		require.Error(t, err)
	})

	t.Run("validly signed assertion within its time window is accepted", func(t *testing.T) {
		certPEM, ks := testIDPKeyPair(t)
		cfg := Config{
			SPEntityID:  "https://app.stonesuite.io/saml/cognito",
			ACSURL:      "https://app.stonesuite.io/api/auth/saml/cognito/acs",
			IDPEntityID: "https://idp.example.com/saml",
			IDPSSOURL:   "https://idp.example.com/saml/sso",
			IDPCertPEM:  certPEM,
		}

		encoded := signedResponseFixture(t, ks, cfg, "ada@example.com", "session-abc-123")

		got, err := ParseAndValidateResponse(cfg, encoded)
		require.NoError(t, err)
		require.NotNil(t, got)
		assert.Equal(t, "ada@example.com", got.Email)
		assert.Equal(t, "ada@example.com", got.NameID)
		assert.Equal(t, saml2.NameIdFormatEmailAddress, got.NameIDFormat)
		assert.Equal(t, "session-abc-123", got.SessionIndex)
		assert.Equal(t, "ada@example.com", got.Attributes["mail"])
	})
}

// buildValues constructs a saml2.Values map with a single named attribute
// holding a single string value, matching the shape RetrieveAssertionInfo
// itself produces.
func buildValues(t *testing.T, name, value string) saml2.Values {
	t.Helper()
	return saml2.Values{
		name: types.Attribute{
			Name:   name,
			Values: []types.AttributeValue{{Value: value}},
		},
	}
}

// rawResponseTemplate is a minimal, well-formed SAML Response mirroring the
// shape gosaml2 itself validates against (see the library's own
// saml_test.go), with placeholders for every value ParseAndValidateResponse
// checks: response/assertion IDs, timestamps, destination, issuer, subject,
// conditions (audience + validity window), and one attribute.
const rawResponseTemplate = `<?xml version="1.0" encoding="UTF-8"?><samlp:Response xmlns:samlp="urn:oasis:names:tc:SAML:2.0:protocol" xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion" ID="%s" Version="2.0" IssueInstant="%s" Destination="%s"><saml:Issuer>%s</saml:Issuer><samlp:Status><samlp:StatusCode Value="urn:oasis:names:tc:SAML:2.0:status:Success"/></samlp:Status><saml:Assertion ID="%s" Version="2.0" IssueInstant="%s"><saml:Issuer>%s</saml:Issuer><saml:Subject><saml:NameID Format="%s">%s</saml:NameID><saml:SubjectConfirmation Method="urn:oasis:names:tc:SAML:2.0:cm:bearer"><saml:SubjectConfirmationData NotOnOrAfter="%s" Recipient="%s"/></saml:SubjectConfirmation></saml:Subject><saml:Conditions NotBefore="%s" NotOnOrAfter="%s"><saml:AudienceRestriction><saml:Audience>%s</saml:Audience></saml:AudienceRestriction></saml:Conditions><saml:AuthnStatement AuthnInstant="%s" SessionIndex="%s"><saml:AuthnContext><saml:AuthnContextClassRef>urn:oasis:names:tc:SAML:2.0:ac:classes:PasswordProtectedTransport</saml:AuthnContextClassRef></saml:AuthnContext></saml:AuthnStatement><saml:AttributeStatement><saml:Attribute Name="mail"><saml:AttributeValue>%s</saml:AttributeValue></saml:Attribute></saml:AttributeStatement></saml:Assertion></samlp:Response>`

// signedResponseFixture builds a SAML Response for cfg + email + sessionIndex,
// enveloped-signs it with ks (simulating the IdP's private key -- ks's
// certificate is the one already installed as cfg.IDPCertPEM by the caller),
// and returns it base64-encoded as ParseAndValidateResponse expects.
func signedResponseFixture(t *testing.T, ks dsig.X509KeyStore, cfg Config, email, sessionIndex string) string {
	t.Helper()

	now := time.Now().UTC()
	rawXML := fmt.Sprintf(rawResponseTemplate,
		"_response-id-12345",
		now.Format(time.RFC3339),
		cfg.ACSURL,
		cfg.IDPEntityID,
		"_assertion-id-67890",
		now.Format(time.RFC3339),
		cfg.IDPEntityID,
		saml2.NameIdFormatEmailAddress,
		email,
		now.Add(5*time.Minute).Format(time.RFC3339),
		cfg.ACSURL,
		now.Add(-5*time.Minute).Format(time.RFC3339),
		now.Add(5*time.Minute).Format(time.RFC3339),
		cfg.SPEntityID,
		now.Format(time.RFC3339),
		sessionIndex,
		email,
	)

	doc := etree.NewDocument()
	require.NoError(t, doc.ReadFromString(rawXML))

	ctx := dsig.NewDefaultSigningContext(ks)
	signedRoot, err := ctx.SignEnveloped(doc.Root())
	require.NoError(t, err)

	signedDoc := etree.NewDocument()
	signedDoc.SetRoot(signedRoot)
	signedXML, err := signedDoc.WriteToString()
	require.NoError(t, err)

	return base64.StdEncoding.EncodeToString([]byte(signedXML))
}
