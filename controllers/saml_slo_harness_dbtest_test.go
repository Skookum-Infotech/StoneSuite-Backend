//go:build dbtest

package controllers

import (
	"bytes"
	"compress/flate"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/beevik/etree"
	dsig "github.com/russellhaering/goxmldsig"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/config"
	"stonesuite-backend/tenancy"
)

const (
	sloProvider  = "cognito"
	sloAPIBase   = "https://api.example.test"
	sloIdPSLOURL = "https://idp.example.test/slo"
	sigAlgSHA256 = "http://www.w3.org/2001/04/xmldsig-more#rsa-sha256"
)

// sloIdP is a stand-in identity provider that signs logout requests.
type sloIdP struct {
	key     *rsa.PrivateKey
	cert    []byte
	certPEM string
	issuer  string
}

func (i *sloIdP) GetKeyPair() (*rsa.PrivateKey, []byte, error) { return i.key, i.cert, nil }

func newSLOIdP(t *testing.T) *sloIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()), Subject: pkix.Name{CommonName: "slo test idp"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(24 * time.Hour),
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)
	return &sloIdP{
		key: key, cert: der, issuer: fmt.Sprintf("https://idp-%d.example.test/entity", time.Now().UnixNano()),
		certPEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
	}
}

// sloMsg is a LogoutRequest to send; zero fields take the valid default.
type sloMsg struct {
	id, nameID, destination string
	issuer                  string
	issuedAt                time.Time
	unsigned                bool
	signer                  *sloIdP
}

func (m sloMsg) xml(idp *sloIdP) string {
	if m.id == "" {
		m.id = fmt.Sprintf("_slo-%d", time.Now().UnixNano())
	}
	if m.issuer == "" {
		m.issuer = idp.issuer
	}
	if m.destination == "" {
		m.destination = sloAPIBase + "/api/auth/saml/" + sloProvider + "/logout-response"
	}
	if m.issuedAt.IsZero() {
		m.issuedAt = time.Now()
	}
	return fmt.Sprintf(`<samlp:LogoutRequest xmlns:samlp="urn:oasis:names:tc:SAML:2.0:protocol" xmlns:saml="urn:oasis:names:tc:SAML:2.0:assertion" ID=%q Version="2.0" IssueInstant=%q Destination=%q><saml:Issuer>%s</saml:Issuer><saml:NameID>%s</saml:NameID></samlp:LogoutRequest>`,
		m.id, m.issuedAt.UTC().Format(time.RFC3339), m.destination, m.issuer, m.nameID)
}

// post builds the HTTP-POST binding request for the SLO endpoint.
func (m sloMsg) post(t *testing.T, idp *sloIdP) *http.Request {
	t.Helper()
	signer := idp
	if m.signer != nil {
		signer = m.signer
	}
	doc := etree.NewDocument()
	require.NoError(t, doc.ReadFromString(m.xml(idp)))
	if !m.unsigned {
		ctx := dsig.NewDefaultSigningContext(signer)
		require.NoError(t, ctx.SetSignatureMethod(dsig.RSASHA256SignatureMethod))
		signed, err := ctx.SignEnveloped(doc.Root())
		require.NoError(t, err)
		doc.SetRoot(signed)
	}
	raw, err := doc.WriteToBytes()
	require.NoError(t, err)
	form := url.Values{"SAMLRequest": {base64.StdEncoding.EncodeToString(raw)}, "RelayState": {"/home"}}
	req := httptest.NewRequest(http.MethodPost, "/api/auth/saml/"+sloProvider+"/logout-response", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.SetPathValue("provider", sloProvider)
	return req
}

// redirect builds the HTTP-Redirect binding request, signed over the query.
func (m sloMsg) redirect(t *testing.T, idp *sloIdP) *http.Request {
	t.Helper()
	signer := idp
	if m.signer != nil {
		signer = m.signer
	}
	var buf bytes.Buffer
	fw, err := flate.NewWriter(&buf, flate.DefaultCompression)
	require.NoError(t, err)
	_, err = fw.Write([]byte(m.xml(idp)))
	require.NoError(t, err)
	require.NoError(t, fw.Close())
	q := "SAMLRequest=" + url.QueryEscape(base64.StdEncoding.EncodeToString(buf.Bytes())) +
		"&RelayState=" + url.QueryEscape("/home") + "&SigAlg=" + url.QueryEscape(sigAlgSHA256)
	if !m.unsigned {
		digest := sha256.Sum256([]byte(q))
		sig, err := rsa.SignPKCS1v15(rand.Reader, signer.key, crypto.SHA256, digest[:])
		require.NoError(t, err)
		q += "&Signature=" + url.QueryEscape(base64.StdEncoding.EncodeToString(sig))
	}
	req := httptest.NewRequest(http.MethodGet, "/api/auth/saml/"+sloProvider+"/logout-response?"+q, nil)
	req.SetPathValue("provider", sloProvider)
	return req
}

// sloUser is an SSO identity with one live session. subject is the NameID the
// IdP would send for it; it is unique, as the identities table requires.
type sloUser struct {
	identity *tenancy.Identity
	session  string
	subject  string
}

// sloWorld is one tenant whose IdP is idp, plus the handler under test.
type sloWorld struct {
	t      *testing.T
	cp     *tenancy.ControlPlane
	h      *SAMLAuthOps
	tenant *tenancy.Tenant
	idp    *sloIdP
}

// newSLOWorld seeds a servable tenant with an enabled SAML config for a fresh IdP.
func newSLOWorld(t *testing.T, cp *tenancy.ControlPlane, withSLOURL bool, enabled bool) *sloWorld {
	t.Helper()
	orig := config.AppConfig.APIBaseURL
	config.AppConfig.APIBaseURL = sloAPIBase
	t.Cleanup(func() { config.AppConfig.APIBaseURL = orig })

	cipher := testSecretCipher(t)
	idp := newSLOIdP(t)
	tenant := seedServableSAMLTestTenant(t, cp)
	encCert, err := cipher.Encrypt(idp.certPEM)
	require.NoError(t, err)
	sloURL := ""
	if withSLOURL {
		sloURL = sloIdPSLOURL
	}
	_, err = cp.CreateSSOConfig(context.Background(), tenant.ID, tenancy.SSOConfigInput{
		Provider: sloProvider, Protocol: "saml", MetadataURL: "https://idp.example.test/metadata",
		IDPEntityID: idp.issuer, SSOURL: "https://idp.example.test/sso", SLOURL: sloURL,
		NameIDFormat: "urn:oasis:names:tc:SAML:1.1:nameid-format:emailAddress", Enabled: enabled,
	}, "", encCert, fmt.Sprintf("fp-%d", time.Now().UnixNano()))
	require.NoError(t, err)
	return &sloWorld{t: t, cp: cp, h: NewSAMLAuthOps(cp, nil, cipher), tenant: tenant, idp: idp}
}

// user creates an SSO identity in the tenant with a live session.
func (w *sloWorld) user(name string) sloUser {
	w.t.Helper()
	ctx := context.Background()
	subject := fmt.Sprintf("%s-%d", name, time.Now().UnixNano())
	id, err := w.cp.CreateSSOIdentity(ctx, w.tenant.ID, subject+"@example.test", name, sloProvider, subject)
	require.NoError(w.t, err)
	sess, err := w.cp.CreateSession(ctx, tenancy.NewSessionInput{IdentityID: id.ID, UserAgent: "test", IP: "203.0.113.9"})
	require.NoError(w.t, err)
	return sloUser{identity: id, session: sess, subject: subject}
}

func (w *sloWorld) send(req *http.Request) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	w.h.LogoutResponse(rr, req)
	return rr
}

func (w *sloWorld) ended(session string) bool {
	w.t.Helper()
	s, err := w.cp.SessionByID(context.Background(), session)
	require.NoError(w.t, err)
	return s.EndedAt != nil
}

// securityLog captures slog output so a test can assert on security events.
type securityLog struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (l *securityLog) text() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.buf.String()
}

func captureSecurityLog(t *testing.T) *securityLog {
	t.Helper()
	l := &securityLog{}
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&lockedWriter{l}, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return l
}

type lockedWriter struct{ l *securityLog }

func (w *lockedWriter) Write(p []byte) (int, error) {
	w.l.mu.Lock()
	defer w.l.mu.Unlock()
	return w.l.buf.Write(p)
}
