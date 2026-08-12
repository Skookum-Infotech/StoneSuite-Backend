package saml

import (
	"context"
	"encoding/xml"
	"fmt"
	"strings"
	"testing"

	"github.com/russellhaering/gosaml2/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testCertificateBase64 is a real X.509 certificate's base64-encoded DER
// body (single line, as IdPs typically emit it), reused across metadata
// fixtures below. Only its DER structure needs to be valid for
// metadata-parsing tests -- parseIdPMetadata never checks certificate
// expiry, so it does not matter that this particular certificate may itself
// be expired by the time this test runs.
const testCertificateBase64 = `MIIDpDCCAoygAwIBAgIGAVLIBhAwMA0GCSqGSIb3DQEBBQUAMIGSMQswCQYDVQQGEwJVUzETMBEGA1UECAwKQ2FsaWZvcm5pYTEWMBQGA1UEBwwNU2FuIEZyYW5jaXNjbzENMAsGA1UECgwET2t0YTEUMBIGA1UECwwLU1NPUHJvdmlkZXIxEzARBgNVBAMMCmRldi0xMTY4MDcxHDAaBgkqhkiG9w0BCQEWDWluZm9Ab2t0YS5jb20wHhcNMTYwMjA5MjE1MjA2WhcNMjYwMjA5MjE1MzA2WjCBkjELMAkGA1UEBhMCVVMxEzARBgNVBAgMCkNhbGlmb3JuaWExFjAUBgNVBAcMDVNhbiBGcmFuY2lzY28xDTALBgNVBAoMBE9rdGExFDASBgNVBAsMC1NTT1Byb3ZpZGVyMRMwEQYDVQQDDApkZXYtMTE2ODA3MRwwGgYJKoZIhvcNAQkBFg1pbmZvQG9rdGEuY29tMIIBIjANBgkqhkiG9w0BAQEFAAOCAQ8AMIIBCgKCAQEAmtjBOZ8MmhUyi8cGk4dUY6Fj1MFDt/q3FFiaQpLzu3/q5lRVUNUBbAtqQWwY10dzfZguHOuvA5p5QyiVDvUhe+XkVwN2R2WfArQJRTPnIcOaHrxqQf3o5cCIG21ZtysFHJSo8clPSOe+0VsoRgcJ1aF42rODwgqRRZdO9Wh3502XlJ799DJQ23IC7XasKEsGKzJqhlRrfd/FyIuZT0sFHDKRz5snSJhm9gpNuQlCmk7ONZ1sXqtt+nBIfWIqeoYQubPW7pT5GTc7wouWq4TCjHJiK9k2HiyNxW0E3JX08swEZi2+LVDjgLzNc4lwjSYIj3AOtPZs8s606oBdIBni4wIDAQABMA0GCSqGSIb3DQEBBQUAA4IBAQBMxSkJTxkXxsoKNW0awJNpWRbU81QpheMFfENIzLam4Itc/5kSZAaSy/9e2QKfo4jBo/MMbCq2vM9TyeJQDJpRaioUTd2lGh4TLUxAxCxtUk/pascL+3Nn936LFmUCLxaxnbeGzPOXAhscCtU1H0nFsXRnKx5acPXYSKFZZZktieSkww2Oi8dg2DYaQhGQMSFMVqgVfwEu4bvCRBvdSiNXdWGCZQmFVzBZZ/9rOLzPpvTFTPnpkavJm81FLlUhiE/oFgKlCDLWDknSpXAI0uZGERcwPca6xvIMh86LjQKjbVci9FYDStXCqRnqQ+TccSu/B6uONFsDEngGcXSKfB+a`

// cognitoMetadataTemplate models AWS Cognito's typical SAML IdP metadata
// shape: a single EntityDescriptor/IDPSSODescriptor, a "ds:"-prefixed
// KeyInfo namespace, no SingleLogoutService, and matching HTTP-POST /
// HTTP-Redirect SingleSignOnService entries at the same location.
const cognitoMetadataTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<EntityDescriptor xmlns="urn:oasis:names:tc:SAML:2.0:metadata" xmlns:ds="http://www.w3.org/2000/09/xmldsig#" entityID="urn:amazon:cognito:sp:us-east-1_ExamplePool">
  <IDPSSODescriptor protocolSupportEnumeration="urn:oasis:names:tc:SAML:2.0:protocol" WantAuthnRequestsSigned="false">
    <KeyDescriptor use="signing">
      <ds:KeyInfo>
        <ds:X509Data>
          <ds:X509Certificate>%s</ds:X509Certificate>
        </ds:X509Data>
      </ds:KeyInfo>
    </KeyDescriptor>
    <NameIDFormat>urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress</NameIDFormat>
    <SingleSignOnService Binding="urn:oasis:names:tc:SAML:2.0:bindings:HTTP-POST" Location="https://cognito-idp.us-east-1.amazonaws.com/us-east-1_ExamplePool/saml2/idpresponse"/>
    <SingleSignOnService Binding="urn:oasis:names:tc:SAML:2.0:bindings:HTTP-Redirect" Location="https://cognito-idp.us-east-1.amazonaws.com/us-east-1_ExamplePool/saml2/idpresponse"/>
  </IDPSSODescriptor>
</EntityDescriptor>`

// entraMetadataTemplate models Microsoft Entra ID's typical per-application
// SAML metadata shape: default (unprefixed) metadata namespace, an
// IDPSSODescriptor advertising both SingleLogoutService and
// SingleSignOnService at the same tenant-scoped endpoint.
const entraMetadataTemplate = `<?xml version="1.0" encoding="utf-8"?>
<EntityDescriptor xmlns="urn:oasis:names:tc:SAML:2.0:metadata" entityID="https://sts.windows.net/72f988bf-86f1-41af-91ab-2d7cd011db47/">
  <IDPSSODescriptor xmlns:ds="http://www.w3.org/2000/09/xmldsig#" protocolSupportEnumeration="urn:oasis:names:tc:SAML:2.0:protocol">
    <KeyDescriptor use="signing">
      <ds:KeyInfo>
        <ds:X509Data>
          <ds:X509Certificate>%s</ds:X509Certificate>
        </ds:X509Data>
      </ds:KeyInfo>
    </KeyDescriptor>
    <SingleLogoutService Binding="urn:oasis:names:tc:SAML:2.0:bindings:HTTP-Redirect" Location="https://login.microsoftonline.com/72f988bf-86f1-41af-91ab-2d7cd011db47/saml2"/>
    <SingleLogoutService Binding="urn:oasis:names:tc:SAML:2.0:bindings:HTTP-POST" Location="https://login.microsoftonline.com/72f988bf-86f1-41af-91ab-2d7cd011db47/saml2"/>
    <SingleSignOnService Binding="urn:oasis:names:tc:SAML:2.0:bindings:HTTP-POST" Location="https://login.microsoftonline.com/72f988bf-86f1-41af-91ab-2d7cd011db47/saml2"/>
    <SingleSignOnService Binding="urn:oasis:names:tc:SAML:2.0:bindings:HTTP-Redirect" Location="https://login.microsoftonline.com/72f988bf-86f1-41af-91ab-2d7cd011db47/saml2"/>
  </IDPSSODescriptor>
</EntityDescriptor>`

// missingIDPSSODescriptorMetadata has an EntityDescriptor but no
// IDPSSODescriptor at all.
const missingIDPSSODescriptorMetadata = `<?xml version="1.0" encoding="UTF-8"?>
<EntityDescriptor xmlns="urn:oasis:names:tc:SAML:2.0:metadata" entityID="https://idp.example.com/metadata"></EntityDescriptor>`

// missingCertificateMetadataTemplate has an IDPSSODescriptor and an SSO
// endpoint but no KeyDescriptor at all.
const missingCertificateMetadata = `<?xml version="1.0" encoding="UTF-8"?>
<EntityDescriptor xmlns="urn:oasis:names:tc:SAML:2.0:metadata" entityID="https://idp.example.com/metadata">
  <IDPSSODescriptor protocolSupportEnumeration="urn:oasis:names:tc:SAML:2.0:protocol">
    <SingleSignOnService Binding="urn:oasis:names:tc:SAML:2.0:bindings:HTTP-Redirect" Location="https://idp.example.com/sso"/>
  </IDPSSODescriptor>
</EntityDescriptor>`

// multipleBindingsMetadataTemplate advertises HTTP-POST first, then
// HTTP-Redirect, at two different locations -- proving the Redirect binding
// is picked regardless of declaration order.
const multipleBindingsMetadataTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<EntityDescriptor xmlns="urn:oasis:names:tc:SAML:2.0:metadata" entityID="https://idp.example.com/metadata">
  <IDPSSODescriptor protocolSupportEnumeration="urn:oasis:names:tc:SAML:2.0:protocol">
    <KeyDescriptor use="signing">
      <KeyInfo xmlns="http://www.w3.org/2000/09/xmldsig#">
        <X509Data>
          <X509Certificate>%s</X509Certificate>
        </X509Data>
      </KeyInfo>
    </KeyDescriptor>
    <SingleSignOnService Binding="urn:oasis:names:tc:SAML:2.0:bindings:HTTP-POST" Location="https://idp.example.com/sso/post"/>
    <SingleSignOnService Binding="urn:oasis:names:tc:SAML:2.0:bindings:HTTP-Redirect" Location="https://idp.example.com/sso/redirect"/>
  </IDPSSODescriptor>
</EntityDescriptor>`

// noRedirectBindingMetadataTemplate advertises only non-Redirect bindings --
// proving the first entry is used as a fallback.
const noRedirectBindingMetadataTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<EntityDescriptor xmlns="urn:oasis:names:tc:SAML:2.0:metadata" entityID="https://idp.example.com/metadata">
  <IDPSSODescriptor protocolSupportEnumeration="urn:oasis:names:tc:SAML:2.0:protocol">
    <KeyDescriptor use="signing">
      <KeyInfo xmlns="http://www.w3.org/2000/09/xmldsig#">
        <X509Data>
          <X509Certificate>%s</X509Certificate>
        </X509Data>
      </KeyInfo>
    </KeyDescriptor>
    <SingleSignOnService Binding="urn:oasis:names:tc:SAML:2.0:bindings:HTTP-POST" Location="https://idp.example.com/sso/first-post"/>
    <SingleSignOnService Binding="urn:oasis:names:tc:SAML:2.0:bindings:HTTP-Artifact" Location="https://idp.example.com/sso/second-artifact"/>
  </IDPSSODescriptor>
</EntityDescriptor>`

func TestParseIdPMetadata(t *testing.T) {
	tests := []struct {
		name    string
		xml     string
		wantErr bool
		check   func(t *testing.T, md *IdPMetadata)
	}{
		{
			name: "cognito shaped metadata",
			xml:  fmt.Sprintf(cognitoMetadataTemplate, testCertificateBase64),
			check: func(t *testing.T, md *IdPMetadata) {
				assert.Equal(t, "urn:amazon:cognito:sp:us-east-1_ExamplePool", md.EntityID)
				assert.Equal(t, "https://cognito-idp.us-east-1.amazonaws.com/us-east-1_ExamplePool/saml2/idpresponse", md.SSOURL)
				assert.Empty(t, md.SLOURL)
				assert.Contains(t, md.CertificatePEM, "BEGIN CERTIFICATE")
				assert.NotEmpty(t, md.Fingerprint)
			},
		},
		{
			name: "entra id shaped metadata",
			xml:  fmt.Sprintf(entraMetadataTemplate, testCertificateBase64),
			check: func(t *testing.T, md *IdPMetadata) {
				assert.Equal(t, "https://sts.windows.net/72f988bf-86f1-41af-91ab-2d7cd011db47/", md.EntityID)
				assert.Equal(t, "https://login.microsoftonline.com/72f988bf-86f1-41af-91ab-2d7cd011db47/saml2", md.SSOURL)
				assert.Equal(t, "https://login.microsoftonline.com/72f988bf-86f1-41af-91ab-2d7cd011db47/saml2", md.SLOURL)
				assert.Contains(t, md.CertificatePEM, "BEGIN CERTIFICATE")
				assert.NotEmpty(t, md.Fingerprint)
			},
		},
		{
			name:    "missing idpssodescriptor",
			xml:     missingIDPSSODescriptorMetadata,
			wantErr: true,
		},
		{
			name:    "missing certificate",
			xml:     missingCertificateMetadata,
			wantErr: true,
		},
		{
			name: "multiple sso bindings picks http-redirect",
			xml:  fmt.Sprintf(multipleBindingsMetadataTemplate, testCertificateBase64),
			check: func(t *testing.T, md *IdPMetadata) {
				assert.Equal(t, "https://idp.example.com/sso/redirect", md.SSOURL)
			},
		},
		{
			name: "no redirect binding falls back to first entry",
			xml:  fmt.Sprintf(noRedirectBindingMetadataTemplate, testCertificateBase64),
			check: func(t *testing.T, md *IdPMetadata) {
				assert.Equal(t, "https://idp.example.com/sso/first-post", md.SSOURL)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			md, err := parseIdPMetadata([]byte(tt.xml))
			if tt.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, md)
			if tt.check != nil {
				tt.check(t, md)
			}
		})
	}
}

func TestFetchIdPMetadata(t *testing.T) {
	t.Run("rejects non-https urls without making a request", func(t *testing.T) {
		_, err := FetchIdPMetadata(context.Background(), "http://idp.example.com/metadata")
		require.Error(t, err)
		assert.Contains(t, err.Error(), "https")
	})

	t.Run("propagates a request failure", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel() // cancelled before the request is even sent

		_, err := FetchIdPMetadata(ctx, "https://idp.example.com/metadata")
		require.Error(t, err)
	})
}

func TestSPMetadataXML(t *testing.T) {
	t.Run("valid entity id, acs, and slo produce well-formed xml", func(t *testing.T) {
		body, err := SPMetadataXML("https://app.stonesuite.io/saml/cognito", "https://app.stonesuite.io/api/auth/saml/cognito/acs", "https://app.stonesuite.io/api/auth/saml/cognito/slo")
		require.NoError(t, err)
		require.NotEmpty(t, body)

		assert.True(t, strings.HasPrefix(string(body), `<?xml version="1.0" encoding="UTF-8"?>`))
		assert.Contains(t, string(body), `entityID="https://app.stonesuite.io/saml/cognito"`)
		assert.Contains(t, string(body), "SPSSODescriptor")
		assert.Contains(t, string(body), `Location="https://app.stonesuite.io/api/auth/saml/cognito/acs"`)
		assert.Contains(t, string(body), "SingleLogoutService")
		assert.Contains(t, string(body), `Location="https://app.stonesuite.io/api/auth/saml/cognito/slo"`)

		// Round-trip back through gosaml2's own EntityDescriptor type as a
		// structural well-formedness check independent of substring matching.
		var reparsed types.EntityDescriptor
		require.NoError(t, xml.Unmarshal(body, &reparsed))
		require.NotNil(t, reparsed.SPSSODescriptor)
		assert.False(t, reparsed.SPSSODescriptor.AuthnRequestsSigned)
		assert.True(t, reparsed.SPSSODescriptor.WantAssertionsSigned)
		require.Len(t, reparsed.SPSSODescriptor.AssertionConsumerServices, 1)
		assert.Equal(t, "https://app.stonesuite.io/api/auth/saml/cognito/acs", reparsed.SPSSODescriptor.AssertionConsumerServices[0].Location)
		require.Len(t, reparsed.SPSSODescriptor.SingleLogoutServices, 1)
		assert.Equal(t, "https://app.stonesuite.io/api/auth/saml/cognito/slo", reparsed.SPSSODescriptor.SingleLogoutServices[0].Location)
	})

	t.Run("empty slo url omits the SingleLogoutService element", func(t *testing.T) {
		body, err := SPMetadataXML("https://app.stonesuite.io/saml/cognito", "https://app.stonesuite.io/api/auth/saml/cognito/acs", "")
		require.NoError(t, err)
		assert.NotContains(t, string(body), "SingleLogoutService")

		var reparsed types.EntityDescriptor
		require.NoError(t, xml.Unmarshal(body, &reparsed))
		require.NotNil(t, reparsed.SPSSODescriptor)
		assert.Empty(t, reparsed.SPSSODescriptor.SingleLogoutServices)
	})
}
