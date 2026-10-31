package requisition

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateWriteInput(t *testing.T) {
	tests := []struct {
		name    string
		lines   int
		tax     float64
		needBy  string
		wantErr bool
	}{
		{"ok", 3, 8, "2026-01-01", false},
		{"max lines ok", maxLineItems, 0, "", false},
		{"too many lines", maxLineItems + 1, 0, "", true},
		{"tax over 100", 1, 101, "", true},
		{"negative tax", 1, -1, "", true},
		{"NaN tax", 1, math.NaN(), "", true},
		{"bad date", 1, 0, "01/02/2026", true},
		{"blank date ok", 1, 0, "", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateWriteInput(tc.lines, tc.tax, tc.needBy)
			if tc.wantErr {
				var ce ClientError
				assert.ErrorAs(t, err, &ce)
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestValidateLineAmounts(t *testing.T) {
	tests := []struct {
		name       string
		qty, price float64
		wantErr    bool
	}{
		{"ok", 2, 10, false},
		{"zero price ok", 1, 0, false},
		{"zero qty", 0, 10, true},
		{"NaN qty", math.NaN(), 10, true},
		{"huge qty", 1e12, 10, true},
		{"negative price", 1, -1, true},
		{"NaN price", 1, math.NaN(), true},
		{"Inf price", 1, math.Inf(1), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateLineAmounts(1, tc.qty, tc.price)
			if tc.wantErr {
				var ce ClientError
				assert.ErrorAs(t, err, &ce)
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestValidateTotals(t *testing.T) {
	assert.NoError(t, validateTotals("total", 0))
	assert.Error(t, validateTotals("total", -0.01))
	assert.Error(t, validateTotals("total", math.NaN()))
	assert.Error(t, validateTotals("total", 1e14))
}

func TestCheckExpectedVersion(t *testing.T) {
	one, two := 1, 2
	assert.NoError(t, checkExpectedVersion(nil, 5))
	assert.NoError(t, checkExpectedVersion(&one, 1))
	assert.ErrorIs(t, checkExpectedVersion(&one, 2), ErrVersionConflict)
	assert.ErrorIs(t, checkExpectedVersion(&two, 1), ErrVersionConflict)
}

func TestDeletableStatus(t *testing.T) {
	tests := map[string]bool{"DRFT": true, "CANC": true, "PAPV": false, "APPV": false}
	for code, want := range tests {
		assert.Equal(t, want, deletableStatus(code), code)
	}
}
