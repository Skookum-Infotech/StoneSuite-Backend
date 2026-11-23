package tenancy

import (
	"encoding/base64"
	"errors"
	"testing"
	"time"
)

func TestTenantCursorRoundTrip(t *testing.T) {
	at := time.Date(2026, 11, 23, 10, 30, 15, 123456000, time.UTC)
	const id = "0b6c1b5e-9c47-4f0c-8a55-0d6a3a9c2f11"

	gotAt, gotID, err := decodeTenantCursor(encodeTenantCursor(at, id))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !gotAt.Equal(at) || gotID != id {
		t.Fatalf("round trip = (%v, %q), want (%v, %q)", gotAt, gotID, at, id)
	}
}

func TestTenantCursorNormalisesToUTC(t *testing.T) {
	loc := time.FixedZone("IST", 5*3600+1800)
	at := time.Date(2026, 11, 23, 16, 0, 0, 0, loc)

	gotAt, _, err := decodeTenantCursor(encodeTenantCursor(at, "id-1"))
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !gotAt.Equal(at) {
		t.Fatalf("instant changed: got %v, want %v", gotAt, at)
	}
}

func TestDecodeTenantCursorRejectsMalformed(t *testing.T) {
	enc := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	tests := []struct {
		name   string
		cursor string
	}{
		{"not base64", "!!!not-base64!!!"},
		{"no separator", enc("2026-11-23T10:30:15Z")},
		{"empty id", enc("2026-11-23T10:30:15Z|")},
		{"bad timestamp", enc("yesterday|abc")},
		{"empty", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := decodeTenantCursor(tc.cursor)
			if !errors.Is(err, ErrInvalidTenantCursor) {
				t.Fatalf("err = %v, want ErrInvalidTenantCursor", err)
			}
		})
	}
}
