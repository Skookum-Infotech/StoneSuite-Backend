package creditmemo

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateWriteInput(t *testing.T) {
	tests := []struct {
		name       string
		lines      int
		amount     float64
		adjustment float64
		date       string
		wantErr    bool
	}{
		{"ok", 1, 10, 0, "2026-01-02", false},
		{"blank date ok", 0, 10, -5, "", false},
		{"too many lines", maxLineItems + 1, 0, 0, "", true},
		{"negative amount", 0, -1, 0, "", true},
		{"nan amount", 0, math.NaN(), 0, "", true},
		{"huge amount", 0, 1e14, 0, "", true},
		{"huge adjustment", 0, 1, -1e14, "", true},
		{"nan adjustment", 0, 1, math.NaN(), "", true},
		{"bad date", 0, 1, 0, "01/02/2026", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateWriteInput(tc.lines, tc.amount, tc.adjustment, tc.date)
			if tc.wantErr {
				var ce ClientError
				assert.ErrorAs(t, err, &ce)
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestCheckExpectedVersion(t *testing.T) {
	three, four := 3, 4
	tests := []struct {
		name     string
		expected *int
		current  int
		want     error
	}{
		{"nil passes", nil, 4, nil},
		{"match", &four, 4, nil},
		{"stale", &three, 4, ErrVersionConflict},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, checkExpectedVersion(tc.expected, tc.current))
		})
	}
}
