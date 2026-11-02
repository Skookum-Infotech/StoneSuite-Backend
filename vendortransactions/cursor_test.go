package vendortransactions

import (
	"encoding/base64"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const testUUID = "0f8fad5b-d9cb-469f-a165-70867728950e"

func TestCursor_RoundTrip(t *testing.T) {
	tests := []struct {
		name string
		at   time.Time
	}{
		{"whole second", time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC)},
		{"microsecond precision survives", time.Date(2026, 10, 31, 12, 0, 0, 123456000, time.UTC)},
		{"non-UTC is normalised", time.Date(2026, 10, 31, 12, 0, 0, 5000, time.FixedZone("x", 3600))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tok := cursor{createdAt: tt.at, id: testUUID}.encode()
			got, ok := decodeCursor(tok)
			require.True(t, ok)
			assert.True(t, got.createdAt.Equal(tt.at.UTC().Truncate(time.Microsecond)))
			assert.Equal(t, testUUID, got.id)
		})
	}
}

func TestDecodeCursor_Rejects(t *testing.T) {
	enc := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	tests := []struct {
		name string
		in   string
	}{
		{"empty", ""},
		{"not base64", "!!!"},
		{"no separator", enc("2026-10-31 12:00:00")},
		{"bad timestamp", enc("yesterday|" + testUUID)},
		{"bad uuid", enc("2026-10-31 12:00:00|not-a-uuid")},
		{"sql in uuid", enc("2026-10-31 12:00:00|" + testUUID + "'; DROP TABLE vendor;--")},
		{"extra separator", enc("2026-10-31 12:00:00|" + testUUID + "|x")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, ok := decodeCursor(tt.in)
			assert.False(t, ok)
		})
	}
}
