package services

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBuildMIME_IncludesHTMLAndAttachment(t *testing.T) {
	msg := buildMIME(
		"sender@acme.example",
		[]string{"bob@buyer.example"},
		[]string{"me@acme.example"},
		"Your Invoice INV-1001",
		"<p>See attached.</p>",
		[]EmailAttachment{{FileName: "INV-1001.pdf", ContentType: "application/pdf", Content: []byte("%PDF-1.4 fake")}},
	)
	s := string(msg)
	assert.Contains(t, s, "Subject: Your Invoice INV-1001")
	assert.Contains(t, s, "To: bob@buyer.example")
	assert.Contains(t, s, "Cc: me@acme.example")
	assert.Contains(t, s, "multipart/mixed")
	assert.Contains(t, s, "Content-Type: text/html")
	assert.Contains(t, s, `filename="INV-1001.pdf"`)
	assert.Contains(t, s, "Content-Transfer-Encoding: base64")
	// base64 of the fake bytes appears in the body
	assert.True(t, strings.Contains(s, "JVBERi0xLjQ"), "expected base64-encoded PDF bytes")
}

func TestBuildMIME_HTMLBodyIsQuotedPrintableEncoded(t *testing.T) {
	msg := buildMIME(
		"sender@acme.example",
		[]string{"bob@buyer.example"},
		nil,
		"Your Invoice INV-1001",
		`<a href="https://x.example/set?token=abc&doc=123">link</a>`,
		nil,
	)
	s := string(msg)
	assert.Contains(t, s, "Content-Transfer-Encoding: quoted-printable")
	// A literal '=' in the source body must appear as its quoted-printable
	// escape ("=3D"), proving the body is genuinely QP-encoded rather than a
	// raw passthrough that would corrupt on decode by a compliant client.
	assert.Contains(t, s, "=3D")
}
