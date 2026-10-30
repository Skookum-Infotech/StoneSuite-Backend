package expense

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateLineAmount(t *testing.T) {
	tests := []struct {
		name    string
		amount  float64
		wantErr bool
	}{
		{"zero", 0, false},
		{"normal", 412.5, false},
		{"max", maxMoneyAmount, false},
		{"negative", -1, true},
		{"too large", 1e14, true},
		{"nan", math.NaN(), true},
		{"inf", math.Inf(1), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateLineAmount(1, tt.amount)
			assert.Equal(t, tt.wantErr, err != nil)
			if err != nil {
				assert.True(t, IsClientError(err))
			}
		})
	}
}

func TestValidateLineDate(t *testing.T) {
	tests := []struct {
		name    string
		date    string
		wantErr bool
	}{
		{"valid", "2026-08-10", false},
		{"bad month", "2026-13-01", true},
		{"wrong layout", "08/10/2026", true},
		{"garbage", "yesterday", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.wantErr, validateLineDate(1, tt.date) != nil)
		})
	}
}

func TestValidateLineCount(t *testing.T) {
	assert.NoError(t, validateLineCount(maxLineItems))
	assert.Error(t, validateLineCount(maxLineItems+1))
}

func TestCheckExpectedVersion(t *testing.T) {
	one, two := 1, 2
	tests := []struct {
		name     string
		expected *int
		current  int
		wantErr  error
	}{
		{"absent", nil, 2, nil},
		{"match", &two, 2, nil},
		{"stale", &one, 2, ErrVersionConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.ErrorIs(t, checkExpectedVersion(tt.expected, tt.current), tt.wantErr)
		})
	}
}
