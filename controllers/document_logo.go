package controllers

import (
	_ "embed" // defaultClientLogo

	"stonesuite-backend/companyprofile"
)

// defaultClientLogo is the client's logo (Elevation Stone): the one the app
// header and the frontend's exported PDFs already show, cropped to its visible
// bounds. It is a string, not a []byte, so it stays immutable.
//
//go:embed assets/client-logo.png
var defaultClientLogo string

// ClientLogoPNG returns the default client logo as PNG bytes, for
// DocumentOps.WithDefaultLogo.
func ClientLogoPNG() []byte { return []byte(defaultClientLogo) }

// fallbackLogo is the logo a letterhead carries before any upload is fetched:
// def, but only while the tenant has no logo of its own. A tenant whose logo is
// set but cannot be fetched (storage outage) gets none rather than def, so it
// never appears under someone else's brand.
func fallbackLogo(profile *companyprofile.Profile, def []byte) []byte {
	if profile.LogoKey != "" {
		return nil
	}
	return def
}
