package services

import (
	"embed"
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

const (
	// EmailIconPathPrefix is the public route prefix serving banner and social
	// icons as PNG: mail clients strip inline SVG, so they must be hosted images.
	EmailIconPathPrefix = "/api/assets/email-icon/"
	// EmailIconRoute is the mux pattern for EmailIconHandler.
	EmailIconRoute = "GET " + EmailIconPathPrefix + "{name}"
	emailIconExt   = ".png"
	emailIconDir   = "assets/icons/"
	// defaultEmailIcon is drawn when an Email names no (or an unknown) icon.
	defaultEmailIcon = "doc-check"
)

//go:embed assets/icons/*.png
var emailIconFS embed.FS

// EmailIconURL is the absolute URL of the named email icon (e.g. "envelope",
// "social-x"); an unknown or empty name falls back to the default document icon.
func EmailIconURL(name string) string {
	if _, err := emailIconFS.ReadFile(emailIconDir + name + emailIconExt); err != nil {
		name = defaultEmailIcon
	}
	return strings.TrimRight(config.AppConfig.APIBaseURL, "/") + EmailIconPathPrefix + name + emailIconExt
}

// EmailIconHandler serves an embedded email icon without auth; only embedded
// file names resolve, so the path value never reaches the filesystem.
func EmailIconHandler(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSuffix(r.PathValue("name"), emailIconExt)
	data, err := emailIconFS.ReadFile(emailIconDir + name + emailIconExt)
	if err != nil || strings.ContainsAny(name, `/\.`) {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "image/png")
	w.Header().Set("Cache-Control", emailLogoCacheControl)
	_, _ = w.Write(data)
}

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
