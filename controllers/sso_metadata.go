package controllers

import (
	"errors"
	"log/slog"
	"net/http"

	"stonesuite-backend/saml"
)

// msgMetadataFetchFailed is the only detail a client gets when an IdP metadata
// fetch fails: echoing the underlying error would turn the endpoint into a
// probe that reports what internal hosts answer (status codes, dial errors).
const msgMetadataFetchFailed = "Could not fetch or parse identity provider metadata. Use the IdP's public https metadata URL (not one that redirects)."

// failMetadataFetch logs why an IdP metadata fetch failed and answers 502
// with a fixed message. A blocked internal address is a security event.
func failMetadataFetch(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, saml.ErrMetadataAddressBlocked) {
		logSecurityEvent(r, "sso_metadata_blocked", "err", err.Error())
	} else {
		slog.Warn("idp metadata fetch failed", "err", err)
	}
	fail(w, http.StatusBadGateway, msgMetadataFetchFailed)
}
