package saml

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	saml2 "github.com/russellhaering/gosaml2"
	"github.com/russellhaering/gosaml2/types"
)

const (
	// metadataFetchTimeout bounds the single HTTP call FetchIdPMetadata makes.
	metadataFetchTimeout = 10 * time.Second
	// metadataMaxBodyBytes caps the size of a downloaded IdP metadata document.
	metadataMaxBodyBytes = 2 << 20 // 2MB
	// keyDescriptorUseSigning is the KeyDescriptor@use value identifying a
	// signing key. Some IdPs omit @use entirely to mean "any use", which
	// callers must also accept.
	keyDescriptorUseSigning = "signing"
	// spMetadataValidity is how long our generated SP metadata document
	// declares itself valid for.
	spMetadataValidity = 7 * 24 * time.Hour
)

// IdPMetadata is the subset of an IdP's SAML metadata document this app needs.
type IdPMetadata struct {
	EntityID       string
	SSOURL         string
	SLOURL         string // may be empty -- not all IdPs advertise SLO
	CertificatePEM string
	Fingerprint    string // hex-encoded SHA-256 of the raw DER certificate bytes
}

// FetchIdPMetadata downloads and parses an IdP SAML metadata XML document.
// The only network call in this package. Enforces https:// (reject http://
// and any other scheme), a reasonable timeout (10s), and a response size cap
// (2MB) to avoid abuse. ctx must carry the caller's timeout/cancellation too.
func FetchIdPMetadata(ctx context.Context, metadataURL string) (*IdPMetadata, error) {
	if !strings.HasPrefix(metadataURL, "https://") {
		return nil, fmt.Errorf("saml: idp metadata url must use https")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, metadataURL, nil)
	if err != nil {
		return nil, fmt.Errorf("saml: building idp metadata request: %w", err)
	}

	client := &http.Client{Timeout: metadataFetchTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("saml: fetching idp metadata: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("saml: idp metadata request returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, metadataMaxBodyBytes))
	if err != nil {
		return nil, fmt.Errorf("saml: reading idp metadata response: %w", err)
	}

	return parseIdPMetadata(body)
}

// parseIdPMetadata extracts the SSO/SLO URLs and signing certificate from a
// raw IdP SAML metadata XML document.
func parseIdPMetadata(data []byte) (*IdPMetadata, error) {
	var ed types.EntityDescriptor
	if err := xml.Unmarshal(data, &ed); err != nil {
		return nil, fmt.Errorf("saml: parsing idp metadata xml: %w", err)
	}

	if ed.IDPSSODescriptor == nil {
		return nil, fmt.Errorf("saml: metadata document has no IDPSSODescriptor")
	}

	ssoURL := singleSignOnURL(ed.IDPSSODescriptor.SingleSignOnServices)
	if ssoURL == "" {
		return nil, fmt.Errorf("saml: metadata document has no SingleSignOnService")
	}

	certPEM, err := signingCertificatePEM(ed.IDPSSODescriptor.KeyDescriptors)
	if err != nil {
		return nil, err
	}

	fingerprint, err := certificateFingerprint(certPEM)
	if err != nil {
		return nil, err
	}

	return &IdPMetadata{
		EntityID:       ed.EntityID,
		SSOURL:         ssoURL,
		SLOURL:         singleLogoutURL(ed.IDPSSODescriptor.SingleLogoutServices),
		CertificatePEM: certPEM,
		Fingerprint:    fingerprint,
	}, nil
}

// singleSignOnURL picks the HTTP-Redirect-binding SSO endpoint, falling back
// to the first advertised endpoint if no HTTP-Redirect binding is present.
func singleSignOnURL(services []types.SingleSignOnService) string {
	if len(services) == 0 {
		return ""
	}
	for _, s := range services {
		if s.Binding == saml2.BindingHttpRedirect {
			return s.Location
		}
	}
	return services[0].Location
}

// singleLogoutURL picks the HTTP-Redirect-binding SLO endpoint, falling back
// to the first advertised endpoint. Returns "" if the IdP advertises no SLO
// endpoint at all -- not every IdP supports single logout.
func singleLogoutURL(services []types.SingleLogoutService) string {
	if len(services) == 0 {
		return ""
	}
	for _, s := range services {
		if s.Binding == saml2.BindingHttpRedirect {
			return s.Location
		}
	}
	return services[0].Location
}

// signingCertificatePEM extracts and PEM-encodes the first signing
// certificate (or a certificate with no declared @use, meaning "any") from
// an IdP's KeyDescriptor list.
func signingCertificatePEM(descriptors []types.KeyDescriptor) (string, error) {
	for _, kd := range descriptors {
		if kd.Use != keyDescriptorUseSigning && kd.Use != "" {
			continue
		}
		if len(kd.KeyInfo.X509Data.X509Certificates) == 0 {
			continue
		}

		raw := strings.TrimSpace(kd.KeyInfo.X509Data.X509Certificates[0].Data)
		der, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			return "", fmt.Errorf("saml: decoding idp certificate: %w", err)
		}

		return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})), nil
	}

	return "", fmt.Errorf("saml: metadata document has no signing certificate")
}

// certificateFingerprint returns the hex-encoded SHA-256 digest of a
// PEM-encoded certificate's raw DER bytes.
func certificateFingerprint(certPEM string) (string, error) {
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		return "", fmt.Errorf("saml: no PEM block found in certificate")
	}

	sum := sha256.Sum256(block.Bytes)
	return hex.EncodeToString(sum[:]), nil
}

// SPMetadataXML generates this SP's own metadata document (served at the
// public GET /api/auth/saml/{provider}/metadata endpoint so IdPs / admins
// can configure the SP side). No SP signing/encryption certificate is
// included since we don't sign requests (see the design rationale in
// saml.go): the descriptor declares AuthnRequestsSigned=false,
// WantAssertionsSigned=true.
func SPMetadataXML(spEntityID, acsURL, sloURL string) ([]byte, error) {
	descriptor := types.SPSSODescriptor{
		AuthnRequestsSigned:        false,
		WantAssertionsSigned:       true,
		ProtocolSupportEnumeration: saml2.SAMLProtocolNamespace,
		NameIDFormats:              []string{defaultNameIDFormat},
		AssertionConsumerServices: []types.IndexedEndpoint{
			{
				Binding:  saml2.BindingHttpPost,
				Location: acsURL,
				Index:    1,
			},
		},
	}

	if sloURL != "" {
		descriptor.SingleLogoutServices = []types.Endpoint{
			{
				Binding:  saml2.BindingHttpRedirect,
				Location: sloURL,
			},
		}
	}

	ed := types.EntityDescriptor{
		ValidUntil:      time.Now().UTC().Add(spMetadataValidity),
		EntityID:        spEntityID,
		SPSSODescriptor: &descriptor,
	}

	body, err := xml.MarshalIndent(ed, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("saml: marshaling sp metadata: %w", err)
	}

	var out bytes.Buffer
	out.WriteString(xml.Header)
	out.Write(body)

	return out.Bytes(), nil
}
