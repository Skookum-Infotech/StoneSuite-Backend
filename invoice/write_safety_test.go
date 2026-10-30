package invoice

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
		invDate    string
		dueDate    string
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
		{"due before invoice", 1, 0, 0, "2026-02-01", "2026-01-01", true},
		{"bad date format ignored", 1, 0, 0, "01/02/2026", "2026-01-01", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateWriteInput(tc.lines, tc.shipping, tc.adjustment, tc.invDate, tc.dueDate)
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
