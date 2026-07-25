package chartofaccounts

import (
	"fmt"
	"strings"

	"stonesuite-backend/secret"
)

// accountNumberLast4Key holds the only part of a bank account number the API
// ever returns. It is written alongside the ciphertext at encrypt time so
// reads never need the cipher at all.
const accountNumberLast4Key = "accountNumberLast4"

// last4Length is how much of an account number is safe to surface.
const last4Length = 4

// Last4 returns the final four characters of s, or all of s when it is
// shorter. Surrounding whitespace is ignored.
func Last4(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= last4Length {
		return s
	}
	return s[len(s)-last4Length:]
}

// EncryptAttributes replaces a plaintext bank account number with its
// ciphertext and records a last-4 hint beside it. Every other key passes
// through untouched.
//
// It fails closed with ErrCipherUnavailable when an account number is supplied
// but no cipher is configured (no SECRET_ENCRYPTION_KEY), mirroring how SSOOps
// refuses to store client secrets in plaintext. Callers map this to 503 (AD-10).
func EncryptAttributes(c *secret.Cipher, attrs map[string]any) (map[string]any, error) {
	out := make(map[string]any, len(attrs)+1)
	for k, v := range attrs {
		out[k] = v
	}

	raw, ok := out[BankAccountNumberKey].(string)
	if !ok || raw == "" {
		return out, nil
	}
	if c == nil {
		return nil, ErrCipherUnavailable
	}

	ct, err := c.Encrypt(raw)
	if err != nil {
		return nil, fmt.Errorf("encrypt bank account number: %w", err)
	}
	out[BankAccountNumberKey] = ct
	out[accountNumberLast4Key] = Last4(raw)
	return out, nil
}

// MaskAttributes strips the encrypted account number, leaving only the last-4
// hint. Every read path runs attributes through this before they reach a
// response, so the ciphertext never leaves the store layer and there is no
// unmask path to guard (AD-10).
func MaskAttributes(attrs map[string]any) map[string]any {
	out := make(map[string]any, len(attrs))
	for k, v := range attrs {
		if k == BankAccountNumberKey {
			continue
		}
		out[k] = v
	}
	return out
}
