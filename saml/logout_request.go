package saml

import (
	"crypto/rsa"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	saml2 "github.com/russellhaering/gosaml2"
)

// IdP-initiated single logout: the IdP tells us a user's session ended
// elsewhere. Anyone who can forge that message can sign users out, so nothing
// here acts on a request until its signature has been checked against the
// tenant's own IdP certificate, and only values read from the verified bytes
// are ever returned.
const (
	// MaxLogoutRequestAge is how old a LogoutRequest's IssueInstant may be.
	MaxLogoutRequestAge = 5 * time.Minute
	// MaxLogoutClockSkew is how far in the future an IssueInstant may be.
	MaxLogoutClockSkew = 2 * time.Minute

	maxLogoutRequestBytes = 1 << 20 // inflated XML ceiling, against a zip bomb
	logoutProtocolNS      = "urn:oasis:names:tc:SAML:2.0:protocol"

	sigAlgRSASHA256 = "http://www.w3.org/2001/04/xmldsig-more#rsa-sha256"
	sigAlgRSASHA384 = "http://www.w3.org/2001/04/xmldsig-more#rsa-sha384"
	sigAlgRSASHA512 = "http://www.w3.org/2001/04/xmldsig-more#rsa-sha512"
)

// Errors a caller can tell apart; every one means "do not act on this request".
var (
	ErrLogoutNotSigned     = errors.New("saml: logout request is not signed")
	ErrLogoutBadSignature  = errors.New("saml: logout request signature is invalid")
	ErrLogoutMalformed     = errors.New("saml: logout request is malformed")
	ErrLogoutWrongIssuer   = errors.New("saml: logout request issuer is not the configured idp")
	ErrLogoutWrongTarget   = errors.New("saml: logout request is not addressed to this service")
	ErrLogoutExpired       = errors.New("saml: logout request is outside its validity window")
	ErrLogoutNoPrincipal   = errors.New("saml: logout request names no principal")
	errUnsupportedSigAlg   = errors.New("saml: unsupported logout signature algorithm")
	errMissingLogoutParams = errors.New("saml: missing SAMLRequest")
)

// LogoutRequest is what a verified IdP-initiated LogoutRequest asks for.
type LogoutRequest struct {
	ID           string
	Issuer       string
	NameID       string
	IssueInstant time.Time
	// ExpiresAt is when the request stops being acceptable, for replay tracking.
	ExpiresAt time.Time
}

// logoutRequestXML is the part of a LogoutRequest the redirect binding reads.
type logoutRequestXML struct {
	XMLName      xml.Name  `xml:"urn:oasis:names:tc:SAML:2.0:protocol LogoutRequest"`
	ID           string    `xml:"ID,attr"`
	Version      string    `xml:"Version,attr"`
	IssueInstant time.Time `xml:"IssueInstant,attr"`
	NotOnOrAfter string    `xml:"NotOnOrAfter,attr"`
	Destination  string    `xml:"Destination,attr"`
	Issuer       string    `xml:"urn:oasis:names:tc:SAML:2.0:assertion Issuer"`
	NameID       string    `xml:"urn:oasis:names:tc:SAML:2.0:assertion NameID"`
}

// UnverifiedLogoutIssuer reads the Issuer out of an encoded LogoutRequest
// WITHOUT verifying anything. It exists only to choose which tenant's
// certificate to verify with; the answer must never be trusted on its own.
func UnverifiedLogoutIssuer(encoded string, redirectBinding bool) (string, error) {
	raw, err := decodeLogoutRequest(encoded, redirectBinding)
	if err != nil {
		return "", err
	}
	req, err := unmarshalLogoutRequest(raw)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(req.Issuer), nil
}

// ParseLogoutRequestPOST verifies a LogoutRequest sent with the HTTP-POST
// binding (the XML carries its own signature) and returns what it asks for.
// An unsigned request is refused outright.
func ParseLogoutRequestPOST(cfg Config, encoded string, now time.Time) (*LogoutRequest, error) {
	sp, err := serviceProvider(cfg)
	if err != nil {
		return nil, err
	}
	got, err := sp.ValidateEncodedLogoutRequestPOST(encoded)
	if err != nil {
		return nil, classifyPostError(err)
	}
	if !got.SignatureValidated {
		return nil, ErrLogoutNotSigned
	}
	req := logoutRequestXML{ID: got.ID, Version: got.Version, IssueInstant: got.IssueInstant, Destination: got.Destination}
	if got.Issuer != nil {
		req.Issuer = got.Issuer.Value
	}
	if got.NameID != nil {
		req.NameID = got.NameID.Value
	}
	return acceptLogoutRequest(cfg, req, now)
}

// classifyPostError names why gosaml2 refused a POST-binding request, so the
// caller can log the real reason instead of calling everything a bad signature.
func classifyPostError(err error) error {
	var invalid saml2.ErrInvalidValue
	if errors.As(err, &invalid) {
		switch invalid.Key {
		case saml2.DestinationAttr:
			return ErrLogoutWrongTarget
		case saml2.IssuerTag:
			return ErrLogoutWrongIssuer
		}
		return ErrLogoutMalformed
	}
	if strings.Contains(err.Error(), "not signed") {
		return ErrLogoutNotSigned
	}
	return fmt.Errorf("%w: %v", ErrLogoutBadSignature, err)
}

// ParseLogoutRequestRedirect verifies a LogoutRequest sent with the
// HTTP-Redirect binding, where the signature covers the query string rather
// than the XML, and returns what it asks for. rawQuery is the request's
// undecoded query: the signature is over the exact bytes the IdP sent.
func ParseLogoutRequestRedirect(cfg Config, rawQuery string, now time.Time) (*LogoutRequest, error) {
	params, err := redirectParams(rawQuery)
	if err != nil {
		return nil, err
	}
	sigB64, algRaw := params["Signature"], params["SigAlg"]
	if sigB64 == "" || algRaw == "" {
		return nil, ErrLogoutNotSigned
	}
	alg, err := url.QueryUnescape(algRaw)
	if err != nil {
		return nil, ErrLogoutMalformed
	}
	hash, err := redirectHash(alg)
	if err != nil {
		return nil, err
	}
	sigText, err := url.QueryUnescape(sigB64)
	if err != nil {
		return nil, ErrLogoutMalformed
	}
	sig, err := base64.StdEncoding.DecodeString(sigText)
	if err != nil {
		return nil, ErrLogoutMalformed
	}

	// Verify first: nothing below is looked at until the IdP has vouched for it.
	signed := "SAMLRequest=" + params["SAMLRequest"]
	if rs, ok := params["RelayState"]; ok {
		signed += "&RelayState=" + rs
	}
	signed += "&SigAlg=" + algRaw
	pub, err := idpPublicKey(cfg.IDPCertPEM)
	if err != nil {
		return nil, err
	}
	digest := hashOf(hash, []byte(signed))
	if err := rsa.VerifyPKCS1v15(pub, hash, digest, sig); err != nil {
		return nil, ErrLogoutBadSignature
	}

	encoded, err := url.QueryUnescape(params["SAMLRequest"])
	if err != nil {
		return nil, ErrLogoutMalformed
	}
	raw, err := decodeLogoutRequest(encoded, true)
	if err != nil {
		return nil, err
	}
	req, err := unmarshalLogoutRequest(raw)
	if err != nil {
		return nil, err
	}
	return acceptLogoutRequest(cfg, *req, now)
}

// acceptLogoutRequest applies the checks both bindings share, once the
// signature is known good.
func acceptLogoutRequest(cfg Config, req logoutRequestXML, now time.Time) (*LogoutRequest, error) {
	if req.Version != "2.0" || strings.TrimSpace(req.ID) == "" {
		return nil, ErrLogoutMalformed
	}
	issuer := strings.TrimSpace(req.Issuer)
	if cfg.IDPEntityID == "" || issuer != cfg.IDPEntityID {
		return nil, ErrLogoutWrongIssuer
	}
	// Required, and must be this endpoint: a request meant for another service
	// provider that shares the IdP must not work here.
	if cfg.SLOURL == "" || req.Destination != cfg.SLOURL {
		return nil, ErrLogoutWrongTarget
	}
	age := now.Sub(req.IssueInstant)
	if req.IssueInstant.IsZero() || age > MaxLogoutRequestAge || age < -MaxLogoutClockSkew {
		return nil, ErrLogoutExpired
	}
	expires := req.IssueInstant.Add(MaxLogoutRequestAge)
	if req.NotOnOrAfter != "" {
		notAfter, err := time.Parse(time.RFC3339, req.NotOnOrAfter)
		if err != nil {
			return nil, ErrLogoutMalformed
		}
		if !now.Before(notAfter) {
			return nil, ErrLogoutExpired
		}
		if notAfter.Before(expires) {
			expires = notAfter
		}
	}
	nameID := strings.TrimSpace(req.NameID)
	if nameID == "" {
		return nil, ErrLogoutNoPrincipal
	}
	return &LogoutRequest{
		ID: req.ID, Issuer: issuer, NameID: nameID, IssueInstant: req.IssueInstant, ExpiresAt: expires,
	}, nil
}
