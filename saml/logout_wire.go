package saml

import (
	"bytes"
	"compress/flate"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

// Helpers for reading a LogoutRequest off the wire: the HTTP-Redirect query
// string, its signature algorithm and certificate, and the base64/DEFLATE/XML
// layers shared by both bindings.

// redirectParams splits a raw query into its values without decoding them,
// refusing a repeated parameter (which would let two readers pick different
// ones) and any query too large to be a logout request.
func redirectParams(rawQuery string) (map[string]string, error) {
	out := map[string]string{}
	for _, part := range strings.Split(rawQuery, "&") {
		name, value, _ := strings.Cut(part, "=")
		switch name {
		case "SAMLRequest", "RelayState", "SigAlg", "Signature":
			if _, dup := out[name]; dup {
				return nil, ErrLogoutMalformed
			}
			out[name] = value
		}
	}
	if out["SAMLRequest"] == "" {
		return nil, errMissingLogoutParams
	}
	return out, nil
}

func redirectHash(alg string) (crypto.Hash, error) {
	switch alg {
	case sigAlgRSASHA256:
		return crypto.SHA256, nil
	case sigAlgRSASHA384:
		return crypto.SHA384, nil
	case sigAlgRSASHA512:
		return crypto.SHA512, nil
	}
	return 0, errUnsupportedSigAlg // includes rsa-sha1 and anything unrecognised
}

func hashOf(h crypto.Hash, data []byte) []byte {
	switch h {
	case crypto.SHA384:
		s := sha512.Sum384(data)
		return s[:]
	case crypto.SHA512:
		s := sha512.Sum512(data)
		return s[:]
	default:
		s := sha256.Sum256(data)
		return s[:]
	}
}

func idpPublicKey(certPEM string) (*rsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(certPEM))
	if block == nil {
		return nil, fmt.Errorf("saml: no PEM block in idp certificate")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("saml: parsing idp certificate: %w", err)
	}
	pub, ok := cert.PublicKey.(*rsa.PublicKey)
	if !ok {
		return nil, errUnsupportedSigAlg
	}
	return pub, nil
}

// decodeLogoutRequest turns a SAMLRequest value into XML: base64, and for the
// redirect binding also DEFLATE, with the inflated size capped.
func decodeLogoutRequest(encoded string, redirectBinding bool) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, ErrLogoutMalformed
	}
	if !redirectBinding {
		if len(raw) > maxLogoutRequestBytes {
			return nil, ErrLogoutMalformed
		}
		return raw, nil
	}
	inflated, err := io.ReadAll(io.LimitReader(flate.NewReader(bytes.NewReader(raw)), maxLogoutRequestBytes+1))
	if err != nil || len(inflated) > maxLogoutRequestBytes {
		return nil, ErrLogoutMalformed
	}
	return inflated, nil
}

func unmarshalLogoutRequest(raw []byte) (*logoutRequestXML, error) {
	var req logoutRequestXML
	if err := xml.Unmarshal(raw, &req); err != nil || req.XMLName.Space != logoutProtocolNS {
		return nil, ErrLogoutMalformed
	}
	return &req, nil
}
