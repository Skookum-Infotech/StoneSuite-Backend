package saml

import (
	"fmt"

	saml2 "github.com/russellhaering/gosaml2"
)

// BuildLogoutResponseURL builds the HTTP-Redirect-binding LogoutResponse that
// answers an IdP-initiated LogoutRequest: Status Success, InResponseTo the
// request's ID, sent to the IdP's SLO endpoint with the IdP's RelayState echoed
// back. It is unsigned, like every message this package sends: the service
// provider has no signing key (see serviceProvider). An IdP configured to
// demand signed logout responses will not accept it; the user is still signed
// out here either way.
//
// Success is also the right answer for a user we have no session for: logout
// is idempotent, and anything else would tell whoever holds a valid request
// which names exist.
func BuildLogoutResponseURL(cfg Config, inResponseTo, relayState string) (string, error) {
	if cfg.IDPSLOURL == "" {
		return "", fmt.Errorf("saml: idp does not advertise a single logout url")
	}
	sp, err := serviceProvider(cfg)
	if err != nil {
		return "", err
	}
	doc, err := sp.BuildLogoutResponseDocumentNoSig(saml2.StatusCodeSuccess, inResponseTo)
	if err != nil {
		return "", fmt.Errorf("saml: building logout response document: %w", err)
	}
	u, err := deflateRedirectURL(cfg.IDPSLOURL, "SAMLResponse", relayState, doc)
	if err != nil {
		return "", fmt.Errorf("saml: building logout response url: %w", err)
	}
	return u, nil
}
