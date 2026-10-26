// Package mfa holds the rules for two-step verification by emailed sign-in
// code: how codes are made, stored and checked, and the limits around them.
// Storage lives in tenancy (the mfa_challenges table); the HTTP flow lives in
// controllers (mfa.go, mfa_settings.go).
package mfa

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"
)

// Purposes a challenge can be opened for. They match the CHECK constraint on
// mfa_challenges.purpose.
const (
	// PurposeLogin is the second sign-in step after a correct password.
	PurposeLogin = "login"
	// PurposeEnable proves the inbox works before two-step verification turns on.
	PurposeEnable = "enable"
)

const (
	// CodeLength is how many digits a sign-in code has.
	CodeLength = 6
	// CodeTTL is how long an emailed code stays valid. A resend restarts it.
	CodeTTL = 10 * time.Minute
	// MaxAttempts is how many wrong codes kill a challenge. The caller then
	// starts over (signs in again, or sends a new enable code).
	MaxAttempts = 5
	// MaxSendsPerChallenge caps the first send plus resends on one challenge.
	MaxSendsPerChallenge = 5
	// ResendCooldown is the wait between two sends on one challenge.
	ResendCooldown = 60 * time.Second
	// MaxSendsPerWindow caps the codes one identity can be sent within
	// SendWindow, across every challenge, so a leaked password cannot be used
	// to flood the owner's inbox or to buy unlimited guesses.
	MaxSendsPerWindow = 10
	// SendWindow is the period MaxSendsPerWindow is counted over.
	SendWindow = time.Hour
	// ChallengeRetention is how long a challenge row is kept before it is
	// pruned (whenever the same identity opens a new one).
	ChallengeRetention = 24 * time.Hour
)

const (
	// codeHashCost is the bcrypt cost for stored codes, the same cost the
	// password hashes use.
	codeHashCost = 10
	// maskedLocalPart replaces all but the first character of an address's
	// local part when it is shown back to the person signing in.
	maskedLocalPart = "•••"
)

var (
	// ErrResendTooSoon means ResendCooldown has not passed since the last send.
	ErrResendTooSoon = errors.New("mfa: resend requested too soon")
	// ErrTooManySends means the challenge already used MaxSendsPerChallenge.
	ErrTooManySends = errors.New("mfa: too many codes sent for this challenge")
)

// GenerateCode returns a uniformly random CodeLength-digit code, leading
// zeros kept.
func GenerateCode() (string, error) {
	limit := new(big.Int).Exp(big.NewInt(10), big.NewInt(CodeLength), nil)
	n, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return "", fmt.Errorf("generate sign-in code: %w", err)
	}
	return fmt.Sprintf("%0*d", CodeLength, n.Int64()), nil
}

// HashCode returns the bcrypt hash stored in place of the code.
func HashCode(code string) (string, error) {
	h, err := bcrypt.GenerateFromPassword([]byte(code), codeHashCost)
	if err != nil {
		return "", fmt.Errorf("hash sign-in code: %w", err)
	}
	return string(h), nil
}

// CodeMatches reports whether code is the one hash was made from.
func CodeMatches(hash, code string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(code)) == nil
}

// NormalizeCode strips the spaces and hyphens people paste or type between
// digit groups ("123 456", "123-456").
func NormalizeCode(input string) string {
	return strings.Map(func(r rune) rune {
		if r == ' ' || r == '-' || r == '\t' {
			return -1
		}
		return r
	}, strings.TrimSpace(input))
}

// ValidCodeFormat reports whether code is exactly CodeLength ASCII digits.
func ValidCodeFormat(code string) bool {
	if len(code) != CodeLength {
		return false
	}
	for _, r := range code {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// HashChallengeToken returns the SHA-256 hex digest of a raw challenge token.
// Only the digest is stored; the raw token lives in the signing-in browser.
func HashChallengeToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// MaskEmail shows an address back without revealing it in full:
// "pat@example.com" becomes "p•••@example.com". The domain stays readable so
// the person can tell which inbox to check.
func MaskEmail(email string) string {
	at := strings.LastIndex(email, "@")
	if at <= 0 {
		return maskedLocalPart
	}
	first := []rune(email[:at])[0]
	return string(first) + maskedLocalPart + email[at:]
}

// CheckResend decides whether one more code may be sent on a challenge that
// has already sent sendCount codes, the last at lastSentAt. On
// ErrResendTooSoon, wait is how long until it may.
func CheckResend(sendCount int, lastSentAt, now time.Time) (wait time.Duration, err error) {
	if sendCount >= MaxSendsPerChallenge {
		return 0, ErrTooManySends
	}
	if remaining := lastSentAt.Add(ResendCooldown).Sub(now); remaining > 0 {
		return remaining, ErrResendTooSoon
	}
	return 0, nil
}

// AttemptsLeft is how many more wrong codes a challenge takes after attempts.
func AttemptsLeft(attempts int) int {
	if left := MaxAttempts - attempts; left > 0 {
		return left
	}
	return 0
}

// WaitSeconds rounds a wait up to whole seconds for a "try again in N
// seconds" message, so it never reads 0 while the wait is still running.
func WaitSeconds(wait time.Duration) int {
	if wait <= 0 {
		return 0
	}
	return int((wait + time.Second - 1) / time.Second)
}
