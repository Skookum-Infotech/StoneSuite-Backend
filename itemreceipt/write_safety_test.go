package itemreceipt

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateWriteInput(t *testing.T) {
	tests := []struct {
		name    string
		lines   int
		date    string
		wantErr bool
	}{
		{"ok", 3, "2026-01-01", false},
		{"blank date ok", 1, "", false},
		{"max lines ok", maxLineItems, "", false},
		{"too many lines", maxLineItems + 1, "", true},
		{"bad date", 1, "01/02/2026", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateWriteInput(tc.lines, tc.date)
			if tc.wantErr {
				assert.True(t, IsClientError(err))
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestValidateLineQuantities(t *testing.T) {
	tests := []struct {
		name               string
		received, rejected float64
		wantErr            bool
	}{
		{"ok", 10, 2, false},
		{"all rejected ok", 10, 10, false},
		{"zero received", 0, 0, true},
		{"NaN received", math.NaN(), 0, true},
		{"Inf received", math.Inf(1), 0, true},
		{"huge received", 1e12, 0, true},
		{"negative rejected", 5, -1, true},
		{"NaN rejected", 5, math.NaN(), true},
		{"rejected over received", 5, 6, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateLineQuantities(1, tc.received, tc.rejected)
			if tc.wantErr {
				assert.True(t, IsClientError(err))
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestCheckExpectedVersion(t *testing.T) {
	one, two := 1, 2
	assert.NoError(t, checkExpectedVersion(nil, 5))
	assert.NoError(t, checkExpectedVersion(&one, 1))
	assert.ErrorIs(t, checkExpectedVersion(&one, 2), ErrVersionConflict)
	assert.ErrorIs(t, checkExpectedVersion(&two, 1), ErrVersionConflict)
}
