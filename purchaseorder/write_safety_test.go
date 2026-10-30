package purchaseorder

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
		tax        float64
		orderDate  string
		expected   string
		wantErr    bool
	}{
		{"ok", 3, 10, -5, 8, "2026-01-01", "2026-02-01", false},
		{"max lines ok", maxLineItems, 0, 0, 0, "", "", false},
		{"too many lines", maxLineItems + 1, 0, 0, 0, "", "", true},
		{"negative shipping", 1, -1, 0, 0, "", "", true},
		{"huge shipping", 1, 1e14, 0, 0, "", "", true},
		{"NaN shipping", 1, math.NaN(), 0, 0, "", "", true},
		{"+Inf shipping", 1, math.Inf(1), 0, 0, "", "", true},
		{"huge adjustment", 1, 0, -1e14, 0, "", "", true},
		{"Inf adjustment", 1, 0, math.Inf(-1), 0, "", "", true},
		{"NaN adjustment", 1, 0, math.NaN(), 0, "", "", true},
		{"tax over 100", 1, 0, 0, 101, "", "", true},
		{"NaN tax", 1, 0, 0, math.NaN(), "", "", true},
		{"expected before order", 1, 0, 0, 0, "2026-02-01", "2026-01-01", true},
		{"bad date format ignored", 1, 0, 0, 0, "01/02/2026", "2026-01-01", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateWriteInput(tc.lines, tc.shipping, tc.adjustment, tc.tax, tc.orderDate, tc.expected)
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
		name            string
		qty, price, dsc float64
		wantErr         bool
	}{
		{"ok", 2, 10, 5, false},
		{"zero price ok", 1, 0, 0, false},
		{"zero qty", 0, 10, 0, true},
		{"NaN qty", math.NaN(), 10, 0, true},
		{"huge qty", 1e12, 10, 0, true},
		{"negative price", 1, -1, 0, true},
		{"NaN price", 1, math.NaN(), 0, true},
		{"Inf price", 1, math.Inf(1), 0, true},
		{"discount over 100", 1, 1, 101, true},
		{"negative discount", 1, 1, -1, true},
		{"NaN discount", 1, 1, math.NaN(), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateLineAmounts(1, tc.qty, tc.price, tc.dsc)
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
	tests := map[string]bool{"DRFT": true, "CANC": true, "SENT": false, "PART": false, "RCVD": false, "CLSD": false, "BILD": false}
	for code, want := range tests {
		assert.Equal(t, want, deletableStatus(code), code)
	}
}
