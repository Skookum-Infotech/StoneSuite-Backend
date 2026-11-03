package cashtransfer

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
		{"positive", 10.5, false},
		{"max", maxMoneyAmount, false},
		{"zero", 0, true},
		{"negative", -1, true},
		{"NaN", math.NaN(), true},
		{"+Inf", math.Inf(1), true},
		{"over bound", 1e13, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateAmount(tt.amount)
			if tt.wantErr {
				assert.ErrorAs(t, err, &ClientError{})
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestCheckExpectedVersion(t *testing.T) {
	one, two := 1, 2
	tests := []struct {
		name     string
		expected *int
		current  int
		wantErr  error
	}{
		{"absent", nil, 5, nil},
		{"match", &one, 1, nil},
		{"stale", &one, 2, ErrVersionConflict},
		{"ahead", &two, 1, ErrVersionConflict},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.wantErr, checkExpectedVersion(tt.expected, tt.current))
		})
	}
}
