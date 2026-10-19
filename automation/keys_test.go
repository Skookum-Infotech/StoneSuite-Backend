package automation

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestKeys_OccurrenceKey(t *testing.T) {
	got := OccurrenceKey("r1", "invoice", "42", time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC), 2)
	assert.Equal(t, "r1:invoice:42:2026-11-10:2", got)
}

func TestKeys_DigestKey(t *testing.T) {
	date := time.Date(2026, 11, 10, 0, 0, 0, 0, time.UTC)
	assert.Equal(t, "r1:bob@example.com:2026-11-10", DigestKey("r1", "Bob@Example.COM", date))
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
