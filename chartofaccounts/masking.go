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
// shorter. Surrounding whitespace is ignored. Operates on runes so multi-byte
// input is never sliced mid-character.
func Last4(s string) string {
	s = strings.TrimSpace(s)
	runes := []rune(s)
	if len(runes) <= last4Length {
		return s
	}
	return string(runes[len(runes)-last4Length:])
}

// EncryptAttributes replaces a plaintext bank account number with its
// ciphertext and records a server-derived last-4 hint beside it. Every other
// key passes through untouched. This is the sole boundary responsible for
// keeping a bank account number out of plaintext storage: it does not rely on
// any prior validation of attrs, and it owns accountNumberLast4Key entirely,
// stripping any caller-supplied value and only ever writing one it derived
// itself from a number it actually encrypted.
//
// Behaviour by the value found at BankAccountNumberKey:
//   - absent: returned unchanged (most accounts are not bank accounts).
//   - present but not a string: a ClientError, since a non-string here is
//     either a programming error or a hostile payload and must never reach
//     storage.
//   - present, a string, but blank/whitespace-only: treated as absent — the
//     key is removed from the output rather than left holding an empty string.
//   - present, a non-blank string: encrypted, failing closed with
//     ErrCipherUnavailable when no cipher is configured (no
//     SECRET_ENCRYPTION_KEY), mirroring how SSOOps refuses to store client
//     secrets in plaintext. Callers map this to 503 (AD-10).
func EncryptAttributes(c *secret.Cipher, attrs map[string]any) (map[string]any, error) {
	out := make(map[string]any, len(attrs)+1)
	for k, v := range attrs {
		out[k] = v
	}
	// The last-4 hint is server-derived only; never trust a caller's copy.
	delete(out, accountNumberLast4Key)

	v, present := out[BankAccountNumberKey]
	if !present {
		return out, nil
	}
	raw, ok := v.(string)
	if !ok {
		return nil, ClientError{Msg: fmt.Sprintf(
			"%s must be a string.", BankAccountNumberKey)}
	}
	if strings.TrimSpace(raw) == "" {
		delete(out, BankAccountNumberKey)
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
