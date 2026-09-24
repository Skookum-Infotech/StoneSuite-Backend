package controllers

import (
	"bytes"
	"image/png"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/companyprofile"
	"stonesuite-backend/docpdf"
)

// maxClientLogoBytes keeps the embedded logo from bloating every PDF and email
// attachment it is drawn into.
const maxClientLogoBytes = 64 * 1024

func TestFallbackLogo(t *testing.T) {
	def := []byte("default-logo")
	tests := []struct {
		name    string
		profile *companyprofile.Profile
		def     []byte
		want    []byte
	}{
		{"a tenant with no logo of its own gets the default", &companyprofile.Profile{}, def, def},
		{"an uploaded logo is used instead, never the default", &companyprofile.Profile{LogoKey: "company/logo.png"}, def, nil},
		{"no default configured, no fallback", &companyprofile.Profile{}, nil, nil},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, fallbackLogo(tc.profile, tc.def))
		})
	}
}

func TestDocumentOps_WithDefaultLogo(t *testing.T) {
	h := NewDocumentOps(nil, nil, nil)
	assert.Nil(t, h.defaultLogo, "no fallback unless configured")
	assert.Same(t, h, h.WithDefaultLogo([]byte("logo")))
	assert.Equal(t, []byte("logo"), h.defaultLogo)
}

func TestClientLogoAsset(t *testing.T) {
	logo := ClientLogoPNG()
	require.NotEmpty(t, logo)
	assert.LessOrEqual(t, len(logo), maxClientLogoBytes, "the logo is drawn into every PDF")

	cfg, err := png.DecodeConfig(bytes.NewReader(logo))
	require.NoError(t, err, "the embedded client logo must be a valid PNG")
	ratio := float64(cfg.Width) / float64(cfg.Height)
	assert.Greater(t, ratio, 3.0, "a wide wordmark logo")
	assert.Less(t, ratio, 6.0, "the padding around the logo is trimmed")

	// The renderer accepts it and draws it as a second image in the masthead.
	doc := docpdf.PrintableDoc{Kind: "INVOICE", Number: "INV-1", Seller: docpdf.Seller{Name: "Acme"}}
	without, err := docpdf.Render(doc)
	require.NoError(t, err)
	doc.Seller.LogoPNG = logo
	with, err := docpdf.Render(doc)
	require.NoError(t, err)
	imageObjects := func(pdf []byte) int { return bytes.Count(pdf, []byte("/Subtype /Image")) }
	assert.Greater(t, imageObjects(with), imageObjects(without), "the client logo is drawn beside the StoneSuite logo")
}
