// Package saml is a provider-agnostic, dependency-light wrapper around
// github.com/russellhaering/gosaml2 for building SAML 2.0 AuthnRequests and
// LogoutRequests and for validating SAML Responses. It has no knowledge of
// tenancy, identities, or the database -- callers (the controllers layer)
// assemble a Config per operation from their own storage, and this package
// performs only the SAML mechanics. Its one exception to "no I/O" is
// FetchIdPMetadata (metadata.go), which makes a single HTTPS call to fetch
// an IdP's metadata document.
package saml

import (
	"bytes"
	"compress/flate"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"net/url"

	"github.com/beevik/etree"
	saml2 "github.com/russellhaering/gosaml2"
	dsig "github.com/russellhaering/goxmldsig"
)

// defaultNameIDFormat is used for a Config whose NameIDFormat is empty.
const defaultNameIDFormat = saml2.NameIdFormatEmailAddress

// Config is the per-tenant, per-provider SAML configuration needed to
// perform one SAML operation (building a request, or validating a
// response). It is assembled by the caller (controllers layer) from a
// tenancy.SSOConfig row -- this package has no knowledge of tenancy,
// identities, or the database.
type Config struct {
	SPEntityID   string // our SP entity ID
	ACSURL       string // our ACS callback URL for this tenant+provider
	SLOURL       string // our SP-side SLO URL (optional, may be "")
	IDPEntityID  string
	IDPSSOURL    string
	IDPSLOURL    string // optional, may be ""
	IDPCertPEM   string // PEM-encoded IdP signing certificate (decrypted, plaintext by the time it reaches here)
	NameIDFormat string // defaults applied by caller; if empty here, defaults to emailAddress format
}

// serviceProvider builds a gosaml2 SAMLServiceProvider from cfg. AuthnRequests
// are never signed -- neither AWS Cognito nor Microsoft Entra ID requires it,
// and skipping it avoids an SP signing-keypair subsystem the MVP does not
// need. Response signature validation is never skipped: SkipSignatureValidation
// is always false, since that is the actual security boundary.
func serviceProvider(cfg Config) (*saml2.SAMLServiceProvider, error) {
	nameIDFormat := cfg.NameIDFormat
	if nameIDFormat == "" {
		nameIDFormat = defaultNameIDFormat
	}

	certStore, err := certificateStore(cfg.IDPCertPEM)
	if err != nil {
		return nil, fmt.Errorf("saml: building idp certificate store: %w", err)
	}

	return &saml2.SAMLServiceProvider{
		IdentityProviderSSOURL:      cfg.IDPSSOURL,
		IdentityProviderSLOURL:      cfg.IDPSLOURL,
		IdentityProviderIssuer:      cfg.IDPEntityID,
		AssertionConsumerServiceURL: cfg.ACSURL,
		ServiceProviderIssuer:       cfg.SPEntityID,
		ServiceProviderSLOURL:       cfg.SLOURL,
		SignAuthnRequests:           false,
		IDPCertificateStore:         certStore,
		NameIdFormat:                nameIDFormat,
		SkipSignatureValidation:     false,
		AllowMissingAttributes:      true,
		Clock:                       dsig.NewRealClock(),
		AudienceURI:                 cfg.SPEntityID,
	}, nil
}

// certificateStore parses a PEM-encoded X.509 certificate and wraps it in the
// in-memory certificate store gosaml2 uses to validate response signatures.
func certificateStore(certPEM string) (dsig.X509CertificateStore, error) {
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		return nil, fmt.Errorf("no PEM block found in idp certificate")
	}

	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parsing idp certificate: %w", err)
	}

	return &dsig.MemoryX509CertificateStore{Roots: []*x509.Certificate{cert}}, nil
}

// BuildAuthnRequestURL builds an HTTP-Redirect-binding AuthnRequest and returns
// the URL to redirect the browser to, plus the generated request ID (store this
// server-side, keyed to tenant+provider+expiry, to validate/consume on ACS).
func BuildAuthnRequestURL(cfg Config, relayState string) (redirectURL string, requestID string, err error) {
	sp, err := serviceProvider(cfg)
	if err != nil {
		return "", "", err
	}

	doc, err := sp.BuildAuthRequestDocumentNoSig()
	if err != nil {
		return "", "", fmt.Errorf("saml: building authn request document: %w", err)
	}

	id := doc.Root().SelectAttrValue("ID", "")
	if id == "" {
		return "", "", fmt.Errorf("saml: authn request document is missing its ID attribute")
	}

	redirectURL, err = sp.BuildAuthURLRedirect(relayState, doc)
	if err != nil {
		return "", "", fmt.Errorf("saml: building authn redirect url: %w", err)
	}

	return redirectURL, id, nil
}

// BuildLogoutRequestURL builds an HTTP-Redirect-binding LogoutRequest for
// SP-initiated SLO. Returns an error if cfg.IDPSLOURL is empty (caller should
// treat SLO as unavailable for that config rather than calling this).
//
// This builds the redirect URL itself (deflateRedirectURL) instead of
// calling gosaml2's own SAMLServiceProvider.BuildLogoutURLRedirect: that
// method unconditionally constructs a signing context and calls
// GetSignatureMethodIdentifier for the HTTP-Redirect binding, unlike its
// AuthnRequest equivalent (BuildAuthURLRedirect), which correctly gates
// signing on SignAuthnRequests. With no SP signing key configured -- this
// package never configures one, see serviceProvider -- that call
// dereferences a nil crypto.Signer and panics (verified against gosaml2
// v0.12.0's source). AuthnRequests are unaffected by this and still use
// gosaml2's own BuildAuthURLRedirect.
func BuildLogoutRequestURL(cfg Config, nameID, sessionIndex, relayState string) (redirectURL string, err error) {
	if cfg.IDPSLOURL == "" {
		return "", fmt.Errorf("saml: idp does not advertise a single logout url")
	}

	sp, err := serviceProvider(cfg)
	if err != nil {
		return "", err
	}

	doc, err := sp.BuildLogoutRequestDocumentNoSig(nameID, sessionIndex)
	if err != nil {
		return "", fmt.Errorf("saml: building logout request document: %w", err)
	}

	redirectURL, err = deflateRedirectURL(cfg.IDPSLOURL, relayState, doc)
	if err != nil {
		return "", fmt.Errorf("saml: building logout redirect url: %w", err)
	}

	return redirectURL, nil
}

// deflateRedirectURL DEFLATE-compresses and base64-encodes doc, then appends
// it (plus an optional RelayState) as query parameters on base, per the SAML
// HTTP-Redirect binding (saml-bindings-2.0-os section 3.4.4.1). It never
// signs the query string -- see the BuildLogoutRequestURL doc comment for
// why this exists instead of gosaml2's own redirect-URL builder.
func deflateRedirectURL(base, relayState string, doc *etree.Document) (string, error) {
	parsed, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("parsing redirect base url: %w", err)
	}

	raw, err := doc.WriteToString()
	if err != nil {
		return "", fmt.Errorf("serializing request document: %w", err)
	}

	var buf bytes.Buffer
	fw, err := flate.NewWriter(&buf, flate.DefaultCompression)
	if err != nil {
		return "", fmt.Errorf("creating deflate writer: %w", err)
	}
	if _, err := fw.Write([]byte(raw)); err != nil {
		return "", fmt.Errorf("deflating request document: %w", err)
	}
	if err := fw.Close(); err != nil {
		return "", fmt.Errorf("closing deflate writer: %w", err)
	}

	qs := parsed.Query()
	qs.Set("SAMLRequest", base64.StdEncoding.EncodeToString(buf.Bytes()))
	if relayState != "" {
		qs.Set("RelayState", relayState)
	}
	parsed.RawQuery = qs.Encode()

	return parsed.String(), nil
}
