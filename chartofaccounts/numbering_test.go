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
		// A new child goes to the bottom of the list, not into a gap an
		// earlier deletion left open -- that gap is only reused once .99 is
		// reached (see the exhaustion case below).
		{"does not backfill a gap while the ceiling is still open", "1103",
			[]string{"1103.01", "1103.03"}, "1103.04", ""},
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

// Once every suffix up to the ceiling is taken, a gap below it is the only
// thing left to hand out -- this is the fallback the "does not backfill"
// cases above are deliberately NOT exercising.
func TestNextChildCodeFallsBackToAGapOnceTheCeilingIsFull(t *testing.T) {
	var taken []string
	for i := 1; i <= MaxChildSuffix; i++ {
		if i == 50 {
			continue // leave exactly one gap
		}
		taken = append(taken, fmt.Sprintf("1103.%02d", i))
	}
	got, err := NextChildCode("1103", taken)
	require.NoError(t, err)
	assert.Equal(t, "1103.50", got)
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
		{"appends after the highest taken code", 1100, 1199, []string{"1100", "1101"}, "1102", false},
		// A new account goes to the bottom of the sub-category's list, not
		// into a gap an earlier deletion left open -- that gap is only reused
		// once the range is full at the top (see the fallback case below).
		{"does not backfill an interior gap while room remains at the top", 1100, 1199,
			[]string{"1100", "1102"}, "1103", false},
		{"ignores child codes in the range", 1100, 1199, []string{"1100", "1100.01"}, "1101", false},
		{"ignores codes outside the range", 1100, 1199, []string{"2100", "2101"}, "1100", false},
		{"uses the last slot", 1100, 1101, []string{"1100"}, "1101", false},
		{"falls back to an interior gap once the range is full at the top", 1100, 1102,
			[]string{"1100", "1102"}, "1101", false},
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

func TestCategoryDirectRange(t *testing.T) {
	tests := []struct {
		name           string
		lo, hi         int
		wantLo, wantHi int
	}{
		// The seeded categories: the first hundred-block only, so 1100 Current
		// Assets' own range can never overlap it.
		{"assets", 1000, 1999, 1000, 1099},
		{"revenue", 4000, 4999, 4000, 4099},
		{"system", 9000, 9999, 9000, 9099},
		{"tenant-created block", 10000, 10999, 10000, 10099},
		// A category narrower than one block keeps its own ceiling rather than
		// handing out codes past the end of its range.
		{"range shorter than a block", 1000, 1049, 1000, 1049},
		{"range exactly one block", 1000, 1099, 1000, 1099},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lo, hi := CategoryDirectRange(tt.lo, tt.hi)
			assert.Equal(t, tt.wantLo, lo)
			assert.Equal(t, tt.wantHi, hi)
		})
	}
}

func TestNextCategoryCode(t *testing.T) {
	seeded := []int{1000, 2000, 3000, 4000, 5000, 6000, 7000, 8000, 9000}

	tests := []struct {
		name    string
		taken   []int
		want    int
		wantErr bool
	}{
		{"empty chart", nil, 1000, false},
		{"the nine seeded categories", seeded, 10000, false},
		{"one tenant category already added", append(append([]int{}, seeded...), 10000), 11000, false},
		// A new category goes below the last one, not into a gap an earlier
		// deletion left open -- that gap is only reused once every block above
		// it is taken (see the fallback case below).
		{"does not backfill a gap while blocks remain above", []int{1000, 3000}, 4000, false},
		{"unaligned codes do not consume a block, or advance past one", []int{1500, 2500}, 1000, false},
		{"falls back to a gap once every block above it is taken",
			allCategoryBlocksExcept(50000), 50000, false},
		{"every block taken", allCategoryBlocks(), 0, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NextCategoryCode(tt.taken)
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

// allCategoryBlocks is every code NextCategoryCode can hand out, for the
// exhausted-range case.
func allCategoryBlocks() []int {
	var out []int
	for c := FirstCategoryCode; c <= MaxCategoryCode; c += CategoryBlockSize {
		out = append(out, c)
	}
	return out
}

// allCategoryBlocksExcept is every category code except one, leaving exactly
// one gap for the fallback-to-a-gap case to find.
func allCategoryBlocksExcept(gap int) []int {
	var out []int
	for _, c := range allCategoryBlocks() {
		if c != gap {
			out = append(out, c)
		}
	}
	return out
}

func TestNextSubCategoryCode(t *testing.T) {
	tests := []struct {
		name    string
		lo, hi  int
		taken   []int
		want    int
		wantErr string
	}{
		// Allocation starts one block above the category floor: the first block
		// belongs to CategoryDirectRange, so 1000 is never handed out here.
		{"first sub-category of an empty category", 1000, 1999, nil, 1100, ""},
		{"assets with its three seeded subs", 1000, 1999, []int{1100, 1200, 1300}, 1400, ""},
		{"operating expenses with five seeded subs", 6000, 6999,
			[]int{6100, 6200, 6300, 6400, 6500}, 6600, ""},
		// A new sub-category goes below the last one, not into a gap an
		// earlier deletion left open -- that gap is only reused once every
		// block above it is taken (see the fallback case below).
		{"does not backfill a gap while blocks remain above", 1000, 1999, []int{1100, 1300}, 1400, ""},
		{"unaligned codes do not consume a block, or advance past one", 1000, 1999, []int{1050, 1150}, 1100, ""},
		{"falls back to an interior gap once the range is full at the top", 1000, 1399,
			[]int{1100, 1300}, 1200, ""},
		// Codes are unique table-wide (uq_coa_subcategory_code), so a code taken
		// under another category is still taken -- but one outside this
		// category's range cannot block it.
		{"codes outside the range are irrelevant", 1000, 1999, []int{2100, 2200}, 1100, ""},
		{"exhausted range", 1000, 1199, []int{1100}, 0, "No sub-category codes remain"},
		{"range with no room for a sub-category", 1000, 1099, nil, 0, "No sub-category codes remain"},
		{"inverted range", 1999, 1000, nil, 0, "invalid category code range"},
		{"non-positive range", 0, 999, nil, 0, "invalid category code range"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NextSubCategoryCode(tt.lo, tt.hi, tt.taken)
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
