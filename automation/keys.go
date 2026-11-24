package automation

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	keyDateLayout = "2006-01-02"
	keySep        = ":"
	notifyKeySep  = "|"
)

// OccurrenceKey identifies one rule occurrence: "rule:resource:record:YYYY-MM-DD:stage".
func OccurrenceKey(ruleID, resource, recordID string, anchor time.Time, stage int) string {
	return strings.Join([]string{ruleID, resource, recordID, anchor.Format(keyDateLayout), strconv.Itoa(stage)}, keySep)
}

// maxDigestKeyLen is the width of automation_deliveries.digest_key (VARCHAR(255)).
// An insert past it fails, and one failing row aborts the tenant's whole planning
// tick, so DigestKey must never return anything longer.
const maxDigestKeyLen = 255

// DigestKey identifies one recipient's digest for a local day: "rule:email:YYYY-MM-DD".
//
// A valid address can be up to 254 bytes, which with a 36-byte rule id and the
// date overflows the column. An address too long to fit is replaced by its
// SHA-256 hex, so the key stays unique per (rule, recipient, day) and fits.
// Addresses that fit keep their readable key, so existing rows and the
// idempotency of re-planning are unaffected.
func DigestKey(ruleID, recipientEmail string, localDate time.Time) string {
	email := strings.ToLower(recipientEmail)
	date := localDate.Format(keyDateLayout)
	if len(ruleID)+len(email)+len(date)+2*len(keySep) > maxDigestKeyLen {
		sum := sha256.Sum256([]byte(email))
		email = hex.EncodeToString(sum[:])
	}
	return strings.Join([]string{ruleID, email, date}, keySep)
}

// NotifyKey returns the 64-char hex SHA-256 of the sorted occurrence keys joined
// by "|". The input is not mutated; an empty input hashes the empty string.
func NotifyKey(occurrenceKeys []string) string {
	sorted := append([]string(nil), occurrenceKeys...)
	sort.Strings(sorted)
	sum := sha256.Sum256([]byte(strings.Join(sorted, notifyKeySep)))
	return hex.EncodeToString(sum[:])
}
