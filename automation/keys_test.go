package automation

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKeys_OccurrenceKey(t *testing.T) {
	got := OccurrenceKey("r1", "invoice", "42", time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC), 2)
	assert.Equal(t, "r1:invoice:42:2026-11-10:2", got)
}

func TestKeys_DigestKey(t *testing.T) {
	date := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	assert.Equal(t, "r1:bob@example.com:2026-11-10", DigestKey("r1", "Bob@Example.COM", date))
}

func TestKeys_DigestKeyFitsTheColumnForAnyValidAddress(t *testing.T) {
	const ruleID = "0b6c1b5e-9c47-4f0c-8a55-0d6a3a9c2f11" // 36-char UUID
	date := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	// The longest address RFC 5321 allows is 254 bytes.
	longA := strings.Repeat("a", 242) + "@example.com"
	longB := strings.Repeat("a", 241) + "b@example.com"
	require.Len(t, longA, 254)

	tests := []struct {
		name  string
		email string
	}{
		{"short", "bob@example.com"},
		{"just fits", strings.Repeat("a", 207-len("@example.com")) + "@example.com"},
		{"just overflows", strings.Repeat("a", 208-len("@example.com")) + "@example.com"},
		{"maximum length", longA},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.LessOrEqual(t, len(DigestKey(ruleID, tc.email, date)), maxDigestKeyLen)
		})
	}

	t.Run("a fitting address keeps its readable key", func(t *testing.T) {
		assert.Equal(t, ruleID+":bob@example.com:2026-11-10", DigestKey(ruleID, "bob@example.com", date))
	})
	t.Run("an overflowing key stays unique per address, rule and day", func(t *testing.T) {
		base := DigestKey(ruleID, longA, date)
		assert.Equal(t, base, DigestKey(ruleID, strings.ToUpper(longA), date), "case-insensitive like the short form")
		assert.NotEqual(t, base, DigestKey(ruleID, longB, date), "different address")
		assert.NotEqual(t, base, DigestKey("other-rule", longA, date), "different rule")
		assert.NotEqual(t, base, DigestKey(ruleID, longA, date.AddDate(0, 0, 1)), "different day")
	})
}

func TestKeys_NotifyKey(t *testing.T) {
	tests := []struct {
		name string
		a, b []string
		same bool
	}{
		{"order independent", []string{"a", "b", "c"}, []string{"c", "a", "b"}, true},
		{"different sets differ", []string{"a", "b"}, []string{"a", "c"}, false},
		{"empty is stable", nil, []string{}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ka, kb := NotifyKey(tc.a), NotifyKey(tc.b)
			assert.Len(t, ka, 64)
			assert.Len(t, kb, 64)
			assert.Equal(t, tc.same, ka == kb)
		})
	}
}

func TestKeys_NotifyKeyDoesNotMutateInput(t *testing.T) {
	in := []string{"c", "a", "b"}
	NotifyKey(in)
	assert.Equal(t, []string{"c", "a", "b"}, in)
}

func TestKeys_NotifyKeyEmptyIsHashOfEmptyString(t *testing.T) {
	assert.Equal(t, "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855", NotifyKey(nil))
}
