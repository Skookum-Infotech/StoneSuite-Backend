package vendorbill

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateWriteInput(t *testing.T) {
	okLine := []LineInput{{LineNumber: 1, Quantity: 1, UnitPrice: 10}}
	tests := []struct {
		name       string
		items      []LineInput
		tax        float64
		adjustment float64
		billDate   string
		dueDate    string
		wantErr    bool
	}{
		{"ok", okLine, 5, -5, "2026-01-01", "2026-02-01", false},
		{"max lines ok", make([]LineInput, maxLineItems), 0, 0, "", "", false},
		{"too many lines", make([]LineInput, maxLineItems+1), 0, 0, "", "", true},
		{"negative tax", okLine, -1, 0, "", "", true},
		{"tax over 100", okLine, 101, 0, "", "", true},
		{"NaN tax", okLine, math.NaN(), 0, "", "", true},
		{"huge adjustment", okLine, 0, -1e14, "", "", true},
		{"NaN adjustment", okLine, 0, math.NaN(), "", "", true},
		{"Inf adjustment", okLine, 0, math.Inf(-1), "", "", true},
		{"NaN unit price", []LineInput{{LineNumber: 1, Quantity: 1, UnitPrice: math.NaN()}}, 0, 0, "", "", true},
		{"huge unit price", []LineInput{{LineNumber: 1, Quantity: 1, UnitPrice: 1e14}}, 0, 0, "", "", true},
		{"NaN quantity", []LineInput{{LineNumber: 1, Quantity: math.NaN(), UnitPrice: 1}}, 0, 0, "", "", true},
		{"NaN discount", []LineInput{{LineNumber: 1, Quantity: 1, DiscountPercent: math.NaN()}}, 0, 0, "", "", true},
		{"due before bill", okLine, 0, 0, "2026-02-01", "2026-01-01", true},
		{"bad date format ignored", okLine, 0, 0, "01/02/2026", "2026-01-01", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateWriteInput(tc.items, tc.tax, tc.adjustment, tc.billDate, tc.dueDate)
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
		wantErr  error
	}{
		{"nil skips check", nil, 3, nil},
		{"match", &three, 3, nil},
		{"mismatch", &four, 3, ErrVersionConflict},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.ErrorIs(t, checkExpectedVersion(tc.expected, tc.current), tc.wantErr)
		})
	}
}
