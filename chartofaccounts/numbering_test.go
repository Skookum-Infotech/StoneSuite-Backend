package chartofaccounts

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNextChildCode(t *testing.T) {
	tests := []struct {
		name       string
		parentCode string
		taken      []string
		want       string
		wantErr    string
	}{
		{"first child", "1103", nil, "1103.01", ""},
		{"second child", "1103", []string{"1103.01"}, "1103.02", ""},
		{"fills a gap left by a deleted child", "1103", []string{"1103.01", "1103.03"}, "1103.02", ""},
		{"ignores other parents' children", "1103", []string{"1104.01", "1105.01"}, "1103.01", ""},
		{"ignores the parent's own code", "1103", []string{"1103"}, "1103.01", ""},
		{"pads to two digits", "1103", []string{"1103.01", "1103.02", "1103.03", "1103.04",
			"1103.05", "1103.06", "1103.07", "1103.08"}, "1103.09", ""},
		{"crosses the ten boundary", "1103", func() []string {
			var s []string
			for i := 1; i <= 9; i++ {
				s = append(s, fmt.Sprintf("1103.%02d", i))
			}
			return s
		}(), "1103.10", ""},
		{"rejects a parent that is already a child", "1103.01", nil, "", "two levels"},
		{"rejects an empty parent code", "", nil, "", "parent"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NextChildCode(tt.parentCode, tt.taken)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestNextChildCodeExhausted(t *testing.T) {
	var taken []string
	for i := 1; i <= MaxChildSuffix; i++ {
		taken = append(taken, fmt.Sprintf("1103.%02d", i))
	}
	_, err := NextChildCode("1103", taken)
	require.Error(t, err)
	conflict, ok := IsConflict(err)
	require.True(t, ok, "want ConflictError, got %T", err)
	assert.Contains(t, conflict.Error(), "1103")
}

func TestNextTopLevelCode(t *testing.T) {
	tests := []struct {
		name    string
		lo, hi  int
		taken   []string
		want    string
		wantErr bool
	}{
		{"empty range starts at the low bound", 1100, 1199, nil, "1100", false},
		{"skips taken codes", 1100, 1199, []string{"1100", "1101"}, "1102", false},
		{"fills an interior gap", 1100, 1199, []string{"1100", "1102"}, "1101", false},
		{"ignores child codes in the range", 1100, 1199, []string{"1100", "1100.01"}, "1101", false},
		{"ignores codes outside the range", 1100, 1199, []string{"2100", "2101"}, "1100", false},
		{"uses the last slot", 1100, 1101, []string{"1100"}, "1101", false},
		{"exhausted range conflicts", 1100, 1101, []string{"1100", "1101"}, "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NextTopLevelCode(tt.lo, tt.hi, tt.taken)
			if tt.wantErr {
				require.Error(t, err)
				_, ok := IsConflict(err)
				assert.True(t, ok, "want ConflictError, got %T", err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
