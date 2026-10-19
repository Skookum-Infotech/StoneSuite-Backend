// Package recordemail keeps the email conversation of a record (today a Sales
// Order): the plain-text copy of what StoneSuite sent and the customer's replies
// as they arrive on the record's own inbound address. Replies reach it already
// fetched from Resend by stonesuite-notify; this package never talks to Resend.
//
// Replies are matched to a record by an address, not by guessing from the
// sender or subject: every email sent about a record carries a Reply-To of
// r-<token>@<inbound domain>, so a reply from any mail client (Gmail, Outlook,
// Zoho...) names the exact tenant and record it belongs to.
package recordemail

import (
	"crypto/rand"
	"encoding/base32"
	"fmt"
	"net/mail"
	"regexp"
	"strings"
)

const (
	// LocalPartPrefix starts every reply address's local part ("r-<token>").
	LocalPartPrefix = "r-"
	// tokenBytes of randomness per address: 96 bits, unguessable, and exactly
	// tokenLen characters once base32-encoded.
	tokenBytes = 12
	tokenLen   = 20
)

// tokenEncoding is lower-case unpadded base32. Case-insensitive on purpose:
// mail systems are free to lower-case a local part in transit.
var tokenEncoding = base32.StdEncoding.WithPadding(base32.NoPadding)

var tokenPattern = regexp.MustCompile(fmt.Sprintf("^[a-z2-7]{%d}$", tokenLen))

// NewToken returns a fresh random address token.
func NewToken() (string, error) {
	b := make([]byte, tokenBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate reply-address token: %w", err)
	}
	return strings.ToLower(tokenEncoding.EncodeToString(b)), nil
}

// Address builds the Reply-To address for a token on the inbound domain.
func Address(token, domain string) string {
	return LocalPartPrefix + token + "@" + domain
}

// TokenFromAddresses returns the token of the first address in addrs that is a
// reply address on domain. Each entry may be a bare address or "Name <addr>".
// A mail copied to a record's address and to the customer carries both in its
// recipients; only the one on our domain counts.
func TokenFromAddresses(addrs []string, domain string) (string, bool) {
	for _, raw := range addrs {
		parsed, err := mail.ParseAddress(strings.TrimSpace(raw))
		if err != nil {
			continue
		}
		local, host, ok := strings.Cut(strings.ToLower(parsed.Address), "@")
		if !ok || !strings.EqualFold(host, domain) || !strings.HasPrefix(local, LocalPartPrefix) {
			continue
		}
		token := strings.TrimPrefix(local, LocalPartPrefix)
		if tokenPattern.MatchString(token) {
			return token, true
		}
	}
	return "", false
}
