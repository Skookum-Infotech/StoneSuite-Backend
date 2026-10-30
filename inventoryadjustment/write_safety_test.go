package inventoryadjustment

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateWriteInput(t *testing.T) {
	tooMany := make([]LineInput, maxLineItems+1)
	tests := []struct {
		name    string
		lines   []LineInput
		wantErr bool
	}{
		{"ok", []LineInput{{QtyDelta: -5}, {QtyDelta: 7}}, false},
		{"empty left to resolveLines", nil, false},
		{"too many lines", tooMany, true},
		{"NaN", []LineInput{{QtyDelta: math.NaN()}}, true},
		{"+Inf", []LineInput{{QtyDelta: math.Inf(1)}}, true},
		{"-Inf", []LineInput{{QtyDelta: math.Inf(-1)}}, true},
		{"huge positive", []LineInput{{QtyDelta: 1e12}}, true},
		{"huge negative", []LineInput{{QtyDelta: -1e12}}, true},
		{"at the bound", []LineInput{{QtyDelta: -maxQuantity}}, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateWriteInput(tc.lines)
			if tc.wantErr {
				assert.True(t, IsClientError(err))
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
