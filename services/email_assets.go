package services

import (
	_ "embed" // emailLogoPNG
	"net/http"
	"strings"

	"stonesuite-backend/config"
)

const (
	// EmailLogoPath is the public route serving the header logo. Mail clients
	// (Gmail, Outlook) strip inline SVG, so the header needs a hosted image.
	EmailLogoPath         = "/api/assets/email-logo.png"
	emailLogoCacheControl = "public, max-age=86400"
)

//go:embed assets/email-logo.png
var emailLogoPNG []byte

// EmailLogoURL is the absolute URL of the email header logo.
func EmailLogoURL() string {
	return strings.TrimRight(config.AppConfig.APIBaseURL, "/") + EmailLogoPath
}

// EmailLogoHandler serves the embedded email header logo without auth.
func EmailLogoHandler(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", emailLogoCacheControl)
	_, _ = w.Write(emailLogoPNG)
}
