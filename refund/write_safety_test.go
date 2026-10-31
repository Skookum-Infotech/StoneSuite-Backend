package refund

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
		{"ok", 10.5, false},
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
		wantErr  bool
	}{
		{"absent", nil, 4, false},
		{"match", &four, 4, false},
		{"stale", &three, 4, true},
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
