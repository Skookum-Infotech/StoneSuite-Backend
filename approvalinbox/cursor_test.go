package approvalinbox

import (
	"encoding/base64"
	"errors"
	"testing"
	"time"
)

const goodUUID = "6f0c1d2e-3a4b-4c5d-8e9f-0a1b2c3d4e5f"

func rawCursor(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }

func TestCursorRoundTrip(t *testing.T) {
	in := Cursor{EnteredAt: time.Date(2026, 10, 30, 15, 4, 5, 123456000, time.UTC), WorkflowKey: "invoice", UUID: goodUUID}
	out, err := DecodeCursor(in.Encode())
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !out.EnteredAt.Equal(in.EnteredAt) || out.WorkflowKey != in.WorkflowKey || out.UUID != in.UUID {
		t.Fatalf("round trip mismatch: %+v vs %+v", out, in)
	}
}

func TestDecodeCursorRejectsBadInput(t *testing.T) {
	ts := "2026-10-30T15:04:05Z"
	tests := []struct{ name, in string }{
		{"empty", ""},
		{"not base64", "!!!"},
		{"two parts", rawCursor(ts + "|invoice")},
		{"four parts", rawCursor(ts + "|invoice|" + goodUUID + "|x")},
		{"bad time", rawCursor("yesterday|invoice|" + goodUUID)},
		{"unknown module", rawCursor(ts + "|nonsense|" + goodUUID)},
		{"sql in module", rawCursor(ts + "|invoice;--|" + goodUUID)},
		{"bad uuid", rawCursor(ts + "|invoice|not-a-uuid")},
		{"upper-case uuid", rawCursor(ts + "|invoice|6F0C1D2E-3A4B-4C5D-8E9F-0A1B2C3D4E5F")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := DecodeCursor(tt.in); !errors.Is(err, ErrBadCursor) {
				t.Fatalf("err = %v, want ErrBadCursor", err)
			}
		})
	}
}
