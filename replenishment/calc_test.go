package replenishment

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/inventory"
)

func intPtr(n int) *int { return &n }

func TestMonthlyRequirement(t *testing.T) {
	tests := []struct {
		name  string
		usage []float64
		want  float64
	}{
		{"the worked example: 50, 48, 50 averages 49.33 and rounds up", []float64{50, 48, 50}, 50},
		{"an exact average stays put", []float64{30, 30, 30}, 30},
		{"a quiet month counts as zero", []float64{30, 0, 0}, 10},
		{"a single unit used in the window still needs one", []float64{1, 0, 0}, 1},
		{"no usage at all", []float64{0, 0, 0}, 0},
		{"no months", nil, 0},
		{"fractional bulk usage rounds up to a whole unit", []float64{10.5, 10.5, 10.5}, 11},
		{"float noise on an exact average does not round up", []float64{10.0000001, 10, 10}, 10},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, monthlyRequirement(tc.usage))
		})
	}
}

func TestComputeItems(t *testing.T) {
	months := []string{"2026-08", "2026-09", "2026-10"}
	slab := func(id, name string, usage []float64, onHand, onOrder float64) itemFacts {
		return itemFacts{id: id, name: name, tracking: inventory.TrackingSerialized, unit: SlabUnit,
			usage: usage, onHand: onHand, onOrder: onOrder}
	}

	t.Run("the worked example needs 40 more slabs", func(t *testing.T) {
		got := computeItems([]itemFacts{slab("a", "Item 1", []float64{50, 48, 50}, 10, 0)}, months)
		require.Len(t, got, 1)
		assert.Equal(t, 50.0, got[0].MonthlyRequirement)
		assert.Equal(t, 10.0, got[0].OnHand)
		assert.Equal(t, 40.0, got[0].Needed)
		assert.Equal(t, []MonthUsage{{"2026-08", 50}, {"2026-09", 48}, {"2026-10", 50}}, got[0].Usage)
	})

	tests := []struct {
		name  string
		facts itemFacts
		want  float64 // 0 = the item is left out
	}{
		{"open orders that cover the gap leave the item out", slab("a", "A", []float64{50, 50, 50}, 10, 40), 0},
		{"orders that cover it with room to spare leave it out", slab("a", "A", []float64{50, 50, 50}, 10, 90), 0},
		{"orders that cover only part leave the rest", slab("a", "A", []float64{50, 50, 50}, 10, 25), 15},
		{"stock alone that covers it leaves the item out", slab("a", "A", []float64{50, 50, 50}, 50, 0), 0},
		{"stock and orders together cover it", slab("a", "A", []float64{50, 50, 50}, 20, 30), 0},
		{"an item that was never used is never needed", slab("a", "A", []float64{0, 0, 0}, 0, 0), 0},
		{"an item used once, with nothing in stock, needs one", slab("a", "A", []float64{1, 0, 0}, 0, 0), 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := computeItems([]itemFacts{tc.facts}, months)
			if tc.want == 0 {
				assert.Empty(t, got)
				return
			}
			require.Len(t, got, 1)
			assert.Equal(t, tc.want, got[0].Needed)
		})
	}

	t.Run("largest shortfall first, then by name", func(t *testing.T) {
		got := computeItems([]itemFacts{
			slab("b", "Beta", []float64{10, 10, 10}, 0, 0),
			slab("a", "Alpha", []float64{10, 10, 10}, 0, 0),
			slab("c", "Gamma", []float64{20, 20, 20}, 0, 0),
		}, months)
		require.Len(t, got, 3)
		assert.Equal(t, []string{"Gamma", "Alpha", "Beta"}, []string{got[0].Name, got[1].Name, got[2].Name})
	})

	t.Run("a history shorter than the months leaves the missing months at zero", func(t *testing.T) {
		got := computeItems([]itemFacts{slab("a", "A", []float64{9}, 0, 0)}, months)
		require.Len(t, got, 1)
		assert.Equal(t, []MonthUsage{{"2026-08", 9}, {"2026-09", 0}, {"2026-10", 0}}, got[0].Usage)
	})
}

func TestLineRemaining(t *testing.T) {
	tests := []struct {
		name string
		line lineRemaining
		want float64
	}{
		{"bulk line: ordered less received", lineRemaining{quantity: 100, received: 30}, 70},
		{"bulk line: over-received never goes negative", lineRemaining{quantity: 10, received: 12}, 0},
		{"slab line already sent: the slabs still on order are the truth",
			lineRemaining{serialized: true, quantity: 300, mintedAll: 5, mintedOpen: 3, expected: intPtr(5)}, 3},
		{"slab line already sent and fully arrived", lineRemaining{serialized: true, quantity: 300, mintedAll: 5, mintedOpen: 0}, 0},
		{"slab line not sent yet: its expected slabs", lineRemaining{serialized: true, quantity: 300, expected: intPtr(4)}, 4},
		{"sized slab line stating no count is one slab", lineRemaining{serialized: true, quantity: 60, sized: true}, 1},
		{"unsized slab line: outstanding area over the average slab, rounded up",
			lineRemaining{serialized: true, quantity: 250, avgSlabArea: 60}, 5},
		{"unsized slab line with no slab on record to measure against is one slab",
			lineRemaining{serialized: true, quantity: 250}, 1},
		{"slab line not sent but fully received has nothing left", lineRemaining{serialized: true, quantity: 60, received: 60, expected: intPtr(1)}, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, tc.line.remaining())
		})
	}
}

func TestHistoryWindow(t *testing.T) {
	start, end, months := historyWindow(time.Date(2026, time.November, 2, 15, 30, 0, 0, time.UTC))
	assert.Equal(t, time.Date(2026, time.August, 1, 0, 0, 0, 0, time.UTC), start)
	assert.Equal(t, time.Date(2026, time.November, 1, 0, 0, 0, 0, time.UTC), end)
	assert.Equal(t, []string{"2026-08", "2026-09", "2026-10"}, months)

	// Across a year boundary.
	start, end, months = historyWindow(time.Date(2027, time.January, 31, 0, 0, 0, 0, time.UTC))
	assert.Equal(t, time.Date(2026, time.October, 1, 0, 0, 0, 0, time.UTC), start)
	assert.Equal(t, time.Date(2027, time.January, 1, 0, 0, 0, 0, time.UTC), end)
	assert.Equal(t, []string{"2026-10", "2026-11", "2026-12"}, months)
}
