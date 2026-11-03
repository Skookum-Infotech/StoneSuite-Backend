package inventorycount

import (
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
)

func fptr(v float64) *float64 { return &v }

func TestValidateCountedQty(t *testing.T) {
	tests := []struct {
		name    string
		q       float64
		wantErr bool
	}{
		{"zero", 0, false},
		{"positive", 12.5, false},
		{"at the bound", maxQuantity, false},
		{"negative", -1, true},
		{"NaN", math.NaN(), true},
		{"+Inf", math.Inf(1), true},
		{"huge", 1e12, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateCountedQty(tc.q)
			if tc.wantErr {
				assert.True(t, IsClientError(err))
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestValidateCountEntries(t *testing.T) {
	tests := []struct {
		name    string
		entries []CountEntry
		wantErr bool
	}{
		{"ok", []CountEntry{{CountedQty: fptr(3)}, {}}, false},
		{"too many", make([]CountEntry, maxCountEntries+1), true},
		{"NaN entry", []CountEntry{{CountedQty: fptr(math.NaN())}}, true},
		{"negative entry", []CountEntry{{CountedQty: fptr(-2)}}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateCountEntries(tc.entries)
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
