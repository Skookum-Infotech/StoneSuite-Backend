package salesorder

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateWriteInput(t *testing.T) {
	tests := []struct {
		name       string
		lines      int
		shipping   float64
		adjustment float64
		orderDate  string
		delivery   string
		wantErr    bool
	}{
		{"ok", 3, 10, -5, "2026-01-01", "2026-02-01", false},
		{"max lines ok", maxLineItems, 0, 0, "", "", false},
		{"too many lines", maxLineItems + 1, 0, 0, "", "", true},
		{"negative shipping", 1, -1, 0, "", "", true},
		{"huge shipping", 1, 1e14, 0, "", "", true},
		{"NaN shipping", 1, math.NaN(), 0, "", "", true},
		{"+Inf shipping", 1, math.Inf(1), 0, "", "", true},
		{"huge adjustment", 1, 0, -1e14, "", "", true},
		{"Inf adjustment", 1, 0, math.Inf(-1), "", "", true},
		{"delivery before order", 1, 0, 0, "2026-02-01", "2026-01-01", true},
		{"bad date format ignored", 1, 0, 0, "01/02/2026", "2026-01-01", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateWriteInput(tc.lines, tc.shipping, tc.adjustment, tc.orderDate, tc.delivery)
			if tc.wantErr {
				assert.True(t, IsClientError(err), "got %v", err)
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestCheckExpectedVersion(t *testing.T) {
	v3, v4 := 3, 4
	tests := []struct {
		name     string
		expected *int
		current  int
		wantErr  bool
	}{
		{"no version sent", nil, 4, false},
		{"match", &v4, 4, false},
		{"stale", &v3, 4, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := checkExpectedVersion(tc.expected, tc.current)
			if tc.wantErr {
				assert.ErrorIs(t, err, ErrVersionConflict)
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestDeletableStatus(t *testing.T) {
	tests := map[string]bool{"DRFT": true, "PAPV": true, "APPV": true, "OPEN": true, "CANC": true, "PART": false, "FILL": false}
	for code, want := range tests {
		t.Run(code, func(t *testing.T) { assert.Equal(t, want, deletableStatus(code)) })
	}
}
