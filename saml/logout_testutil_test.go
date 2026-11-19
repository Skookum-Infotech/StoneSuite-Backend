package saml

import (
	"bytes"
	"compress/flate"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/url"
	"testing"
	"time"

	"github.com/beevik/etree"
	dsig "github.com/russellhaering/goxmldsig"
	"github.com/stretchr/testify/require"
)

const (
	testIdPIssuer = "https://idp.example.com/entity"
	testSLOURL    = "https://app.example.com/api/auth/saml/cognito/logout-response"
)

// testIdP is a stand-in identity provider: a key pair and certificate it signs
// logout requests with.
type testIdP struct {
	key     *rsa.PrivateKey
	cert    []byte
	certPEM string
}

// GetKeyPair makes testIdP a goxmldsig key store, for signing the POST binding.
func (i *testIdP) GetKeyPair() (*rsa.PrivateKey, []byte, error) { return i.key, i.cert, nil }

func newTestIdP(t *testing.T) *testIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "test idp"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	return &testIdP{key: key, cert: der, certPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))}
}

func (i *testIdP) config() Config {
	return Config{
		SPEntityID: "https://app.example.com/saml/cognito/metadata", SLOURL: testSLOURL,
		IDPEntityID: testIdPIssuer, IDPSLOURL: "https://idp.example.com/slo", IDPCertPEM: i.certPEM,
	}
}

// logoutSpec is a LogoutRequest to build; a zero field takes the valid default.
type logoutSpec struct {
	id, issuer, destination, nameID string
	issueInstant                    time.Time
	notOnOrAfter                    string
	noDestination, noNameID         bool
}

func (s logoutSpec) xml(now time.Time) string {
	if s.id == "" {
		s.id = fmt.Sprintf("_req-%d", now.UnixNano())
	}
	if s.issuer == "" {
		s.issuer = testIdPIssuer
	}
	if s.destination == "" && !s.noDestination {
		s.destination = testSLOURL
	}
	if s.nameID == "" && !s.noNameID {
		s.nameID = "user-123"
	}
	if s.issueInstant.IsZero() {
		s.issueInstant = now
	}
	attrs := fmt.Sprintf(`ID=%q Version="2.0" IssueInstant=%q`, s.id, s.issueInstant.UTC().Format(time.RFC3339))
	if !s.noDestination {
		attrs += fmt.Sprintf(` Destination=%q`, s.destination)
	}
	if s.notOnOrAfter != "" {
		attrs += fmt.Sprintf(` NotOnOrAfter=%q`, s.notOnOrAfter)
	}
	name := ""
	if !s.noNameID {
		name = fmt.Sprintf(`<saml:NameID>%s</saml:NameID>`, s.nameID)
	}
	return fmt.Sprintf(`<samlp:LogoutRequest xmlns:samlp="urn:oasis:names:tc:SAML:2.0:protocol" xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion" %s><saml:Issuer>%s</saml:Issuer>%s</samlp:LogoutRequest>`,
		attrs, s.issuer, name)
}

// post is the HTTP-POST binding: signed XML, base64 only.
func (i *testIdP) post(t *testing.T, s logoutSpec, now time.Time, sign bool) string {
	t.Helper()
	doc := etree.NewDocument()
	require.NoError(t, doc.ReadFromString(s.xml(now)))
	root := doc.Root()
	if sign {
		ctx := dsig.NewDefaultSigningContext(i)
		require.NoError(t, ctx.SetSignatureMethod(dsig.RSASHA256SignatureMethod))
		signed, err := ctx.SignEnveloped(root)
		require.NoError(t, err)
		doc.SetRoot(signed)
	}
	out, err := doc.WriteToBytes()
	require.NoError(t, err)
	return base64.StdEncoding.EncodeToString(out)
}

// redirect is the HTTP-Redirect binding: deflated XML, with the signature over
// the query string. It returns the raw query exactly as the IdP would send it.
func (i *testIdP) redirect(t *testing.T, s logoutSpec, now time.Time, relayState, sigAlg string, sign bool) string {
	t.Helper()
	var buf bytes.Buffer
	fw, err := flate.NewWriter(&buf, flate.DefaultCompression)
	require.NoError(t, err)
	_, err = fw.Write([]byte(s.xml(now)))
	require.NoError(t, err)
	require.NoError(t, fw.Close())

	q := "SAMLRequest=" + url.QueryEscape(base64.StdEncoding.EncodeToString(buf.Bytes()))
	if relayState != "" {
		q += "&RelayState=" + url.QueryEscape(relayState)
	}
	q += "&SigAlg=" + url.QueryEscape(sigAlg)
	if !sign {
		return q
	}
	digest := sha256.Sum256([]byte(q))
	sig, err := rsa.SignPKCS1v15(rand.Reader, i.key, crypto.SHA256, digest[:])
	require.NoError(t, err)
	return q + "&Signature=" + url.QueryEscape(base64.StdEncoding.EncodeToString(sig))
}
