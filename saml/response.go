package saml

import (
	"fmt"
	"strings"

	saml2 "github.com/russellhaering/gosaml2"
)

// emailAttributeCandidates lists SAML attribute names checked, in order, for
// an email address when an assertion's NameID is not itself an email
// address. Both AWS Cognito and Microsoft Entra ID can be configured to send
// email under any of these names depending on IdP attribute mapping.
var emailAttributeCandidates = []string{
	"http://schemas.xmlsoap.org/ws/2005/05/identity/claims/emailaddress",
	"email",
	"mail",
	"emailaddress",
	"http://schemas.xmlsoap.org/claims/EmailAddress",
}

// ParsedAssertion is the provider-agnostic result of a successfully
// validated SAML Response.
type ParsedAssertion struct {
	Email        string // resolved email (see extractEmail)
	NameID       string
	NameIDFormat string
	SessionIndex string
	Attributes   map[string]string // flattened single-value attributes, for audit/debug only
}

// ParseAndValidateResponse validates the signature, expiry, and audience of a
// base64-encoded SAML Response (the SAMLResponse form field POSTed to the ACS
// endpoint) and extracts assertion data. Returns an error for any validation
// failure -- signature, expiry, malformed XML, missing required fields, or a
// response whose signature was never actually validated.
func ParseAndValidateResponse(cfg Config, samlResponseBase64 string) (*ParsedAssertion, error) {
	sp, err := serviceProvider(cfg)
	if err != nil {
		return nil, err
	}

	assertionInfo, err := sp.RetrieveAssertionInfo(samlResponseBase64)
	if err != nil {
		return nil, fmt.Errorf("saml: retrieving assertion info: %w", err)
	}

	if !assertionInfo.ResponseSignatureValidated {
		return nil, fmt.Errorf("saml: response signature was not validated")
	}

	if warn := assertionInfo.WarningInfo; warn != nil {
		switch {
		case warn.InvalidTime:
			return nil, fmt.Errorf("saml: assertion is outside its valid time window")
		case warn.NotInAudience:
			return nil, fmt.Errorf("saml: assertion audience does not match this service provider")
		case warn.OneTimeUse:
			return nil, fmt.Errorf("saml: assertion is marked one-time-use and cannot be accepted")
		}
	}

	email, err := extractEmail(assertionInfo.NameID, assertionInfo.NameIDFormat, assertionInfo.Values)
	if err != nil {
		return nil, err
	}

	return &ParsedAssertion{
		Email:        email,
		NameID:       assertionInfo.NameID,
		NameIDFormat: assertionInfo.NameIDFormat,
		SessionIndex: assertionInfo.SessionIndex,
		Attributes:   flattenValues(assertionInfo.Values),
	}, nil
}

// extractEmail resolves an email address from a validated assertion's NameID
// or, if the NameID is not itself an email address, from a fixed set of
// common SAML email attribute names. nameIDFormat is accepted for API
// symmetry with the assertion but is not consulted: the "@" check is
// provider-agnostic and correct regardless of the declared format.
func extractEmail(nameID, nameIDFormat string, values saml2.Values) (string, error) {
	if strings.Contains(nameID, "@") {
		return nameID, nil
	}

	for _, candidate := range emailAttributeCandidates {
		if v := values.Get(candidate); v != "" {
			return v, nil
		}
	}

	return "", fmt.Errorf("SAML assertion did not contain an email address (checked NameID and common email attributes)")
}

// flattenValues collapses a gosaml2 Values map down to a single string per
// attribute name (its first value), for audit/debug only -- multi-valued
// attributes lose all but the first entry.
func flattenValues(values saml2.Values) map[string]string {
	out := make(map[string]string, len(values))
	for name := range values {
		out[name] = values.Get(name)
	}
	return out
}
