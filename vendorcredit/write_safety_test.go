package vendorcredit

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateAmount(t *testing.T) {
	tests := []struct {
		name    string
		amount  float64
		wantErr bool
	}{
		{"ok", 10, false},
		{"max", maxMoneyAmount, false},
		{"zero", 0, true},
		{"negative", -1, true},
		{"too large", 1e14, true},
		{"NaN", math.NaN(), true},
		{"+Inf", math.Inf(1), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateAmount("amount", tc.amount)
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
