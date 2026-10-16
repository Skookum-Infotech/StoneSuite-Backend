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

// DigestKey identifies one recipient's digest for a local day: "rule:email:YYYY-MM-DD".
func DigestKey(ruleID, recipientEmail string, localDate time.Time) string {
	return strings.Join([]string{ruleID, strings.ToLower(recipientEmail), localDate.Format(keyDateLayout)}, keySep)
}

// NotifyKey returns the 64-char hex SHA-256 of the sorted occurrence keys joined
// by "|". The input is not mutated; an empty input hashes the empty string.
func NotifyKey(occurrenceKeys []string) string {
	sorted := append([]string(nil), occurrenceKeys...)
	sort.Strings(sorted)
	sum := sha256.Sum256([]byte(strings.Join(sorted, notifyKeySep)))
	return hex.EncodeToString(sum[:])
}
