package journal

import (
	"errors"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestToCents(t *testing.T) {
	a, b := 0.1, 0.2 // runtime sum, not a constant-folded exact 0.3
	tests := []struct {
		name   string
		in     float64
		cents  int64
		wantOK bool
	}{
		{"whole", 100, 10000, true},
		{"two decimals", 0.29, 29, true},
		{"one cent", 0.01, 1, true},
		{"three decimals rejected", 0.005, 0, false},
		{"float drift sum rejected", a + b, 0, false},
		{"large in range", 9999999999999.99, 999999999999999, true},
		{"over bound rejected", 1e13, 0, false},
		{"NaN rejected", math.NaN(), 0, false},
		{"+Inf rejected", math.Inf(1), 0, false},
		{"negative rejected", -1, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := toCents(tt.in)
			assert.Equal(t, tt.wantOK, ok)
			if tt.wantOK {
				assert.Equal(t, tt.cents, got)
			}
		})
	}
}

func TestValidateLinesCents(t *testing.T) {
	tests := []struct {
		name    string
		lines   []LineInput
		wantErr error
	}{
		{"0.1+0.2 vs 0.3 balanced", []LineInput{{AccountID: 1, Debit: 0.1}, {AccountID: 2, Debit: 0.2}, {AccountID: 3, Credit: 0.3}}, nil},
		{"sub-cent rejected", []LineInput{{AccountID: 1, Debit: 0.004}, {AccountID: 2, Credit: 0.004}}, ErrInvalidLine},
		{"three decimal pair rejected", []LineInput{{AccountID: 1, Debit: 10.005}, {AccountID: 2, Credit: 10.005}}, ErrInvalidLine},
		{"large balanced", []LineInput{{AccountID: 1, Debit: 9999999999999.99}, {AccountID: 2, Credit: 9999999999999.99}}, nil},
		{"over bound rejected", []LineInput{{AccountID: 1, Debit: 1e14}, {AccountID: 2, Credit: 1e14}}, ErrInvalidLine},
		{"NaN debit rejected", []LineInput{{AccountID: 1, Debit: math.NaN()}, {AccountID: 2, Credit: 1}}, ErrInvalidLine},
		{"Inf rejected", []LineInput{{AccountID: 1, Debit: math.Inf(1)}, {AccountID: 2, Credit: math.Inf(1)}}, ErrInvalidLine},
		{"one cent off", []LineInput{{AccountID: 1, Debit: 0.3}, {AccountID: 2, Credit: 0.29}}, ErrUnbalancedEntry},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateLines(tt.lines)
			if tt.wantErr == nil {
				assert.NoError(t, err)
				return
			}
			assert.True(t, errors.Is(err, tt.wantErr), "got %v", err)
		})
	}
}

func TestValidateLinesTooMany(t *testing.T) {
	lines := make([]LineInput, maxEntryLines+1)
	for i := range lines {
		lines[i] = LineInput{AccountID: 1, Debit: 1}
	}
	var ce ClientError
	assert.True(t, errors.As(validateLines(lines), &ce))
}
