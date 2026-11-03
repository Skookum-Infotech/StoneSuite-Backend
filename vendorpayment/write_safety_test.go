package vendorpayment

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateCreateAmounts(t *testing.T) {
	tests := []struct {
		name    string
		amount  float64
		apps    []ApplicationInput
		wantErr bool
	}{
		{"ok", 100, []ApplicationInput{{Amount: 40}}, false},
		{"no apps ok", 100, nil, false},
		{"zero amount", 0, nil, true},
		{"negative amount", -5, nil, true},
		{"NaN amount", math.NaN(), nil, true},
		{"huge amount", 1e14, nil, true},
		{"Inf amount", math.Inf(1), nil, true},
		{"NaN application", 100, []ApplicationInput{{Amount: math.NaN()}}, true},
		{"zero application", 100, []ApplicationInput{{Amount: 0}}, true},
		{"too many applications", 100, make([]ApplicationInput, maxInlineApplications+1), true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateCreateAmounts(tc.amount, tc.apps)
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
