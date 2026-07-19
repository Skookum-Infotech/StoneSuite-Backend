package auditstore

import (
	"encoding/base64"
	"testing"
	"time"
)

func TestCursorRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		ts   time.Time
		id   string
	}{
		{"utc", time.Date(2026, 7, 19, 12, 30, 45, 123456789, time.UTC), "11111111-1111-1111-1111-111111111111"},
		{"zoned normalized to utc", time.Date(2026, 1, 2, 3, 4, 5, 0, time.FixedZone("x", 5*3600)), "22222222-2222-2222-2222-222222222222"},
		{"zero nanos", time.Date(2026, 12, 31, 23, 59, 59, 0, time.UTC), "abc"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cur := encodeCursor(tc.ts, tc.id)
			gotTS, gotID, err := decodeCursor(cur)
			if err != nil {
				t.Fatalf("decode: %v", err)
			}
			if !gotTS.Equal(tc.ts) {
				t.Errorf("ts = %v, want %v", gotTS, tc.ts)
			}
			if gotID != tc.id {
				t.Errorf("id = %q, want %q", gotID, tc.id)
			}
		})
	}
}

func TestDecodeCursorInvalid(t *testing.T) {
	enc := func(raw string) string { return base64.RawURLEncoding.EncodeToString([]byte(raw)) }
	tests := []string{
		"!!!not-base64!!!",
		"",                     // decodes to empty, no separator
		enc("no-separator"),    // missing "|"
		enc("bad-time|the-id"), // unparseable timestamp
	}
	for _, in := range tests {
		if _, _, err := decodeCursor(in); err == nil {
			t.Errorf("decodeCursor(%q) = nil error, want error", in)
		}
	}
}
