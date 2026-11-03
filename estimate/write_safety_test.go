package estimate

import (
	"math"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateWriteInput(t *testing.T) {
	tests := []struct {
		name       string
		lines      int
		ship, adj  float64
		date, till string
		wantMsg    string
	}{
		{"ok", 1, 0, 0, "2026-01-01", "2026-02-01", ""},
		{"ok equal dates", 1, 0, 0, "2026-01-01", "2026-01-01", ""},
		{"too many lines", maxLineItems + 1, 0, 0, "", "", "at most"},
		{"negative shipping", 1, -1, 0, "", "", "shippingCharge"},
		{"NaN shipping", 1, math.NaN(), 0, "", "", "shippingCharge"},
		{"huge shipping", 1, 1e14, 0, "", "", "shippingCharge"},
		{"NaN adjustment", 1, 0, math.NaN(), "", "", "adjustment"},
		{"huge negative adjustment", 1, 0, -1e14, "", "", "adjustment"},
		{"expiry before date", 1, 0, 0, "2026-02-01", "2026-01-01", "validUntil"},
		{"blank expiry", 1, 0, 0, "2026-02-01", "", ""},
		{"unparseable left to db", 1, 0, 0, "bad", "2026-01-01", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateWriteInput(tc.lines, tc.ship, tc.adj, tc.date, tc.till)
			if tc.wantMsg == "" {
				assert.NoError(t, err)
				return
			}
			assert.True(t, IsClientError(err), "got %v", err)
			assert.True(t, strings.Contains(err.Error(), tc.wantMsg), "got %v", err)
		})
	}
}

func TestCheckExpectedVersion(t *testing.T) {
	three, four := 3, 4
	tests := []struct {
		name     string
		expected *int
		current  int
		wantErr  error
	}{
		{"none sent", nil, 3, nil},
		{"match", &three, 3, nil},
		{"stale", &four, 3, ErrVersionConflict},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.ErrorIs(t, checkExpectedVersion(tc.expected, tc.current), tc.wantErr)
		})
	}
}
