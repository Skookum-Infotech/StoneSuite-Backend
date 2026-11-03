package tenancy

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
)

// Single-use tokens (password reset/setup, tenant/user/portal invites) are
// stored as SHA-256(raw), never raw: a leaked database or backup must not hand
// out working reset and invite links. The raw token exists only in the email
// link and in the create/resend response that minted it.
//
// Lookups match the hash only. Raw tokens are 64 hex chars, the same shape as
// a hash, so a fallback that also matched the raw value would let anyone
// holding the stored hash (the leaked-backup case this exists for) present it
// as a working token. Links minted before hashing therefore stop working;
// users request a fresh reset or have the invite resent.

// ErrTokenAlreadyUsed is returned when a single-use token was consumed (or
// replaced) between lookup and use, e.g. by a concurrent request.
var ErrTokenAlreadyUsed = errors.New("token already used or expired")

// HashToken returns the stored form of a raw single-use token.
func HashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
