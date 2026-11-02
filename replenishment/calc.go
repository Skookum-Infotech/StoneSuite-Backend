package replenishment

import (
	"math"
	"sort"
)

// stockEpsilon absorbs the float noise of summing DECIMAL(14,3) quantities, so
// 0.0000001 of an item left over is not reported as a shortage.
const stockEpsilon = 0.0005

// monthlyRequirement is the average of the history months, rounded up to a
// whole unit: 50, 48 and 50 slabs average 49.33, so the requirement is 50. A
// month with no usage counts as zero. Rounding up is deliberate — a buyer
// short by a fraction cannot order a fraction of a slab.
func monthlyRequirement(usage []float64) float64 {
	if len(usage) == 0 {
		return 0
	}
	var sum float64
	for _, u := range usage {
		sum += u
	}
	avg := sum / float64(len(usage))
	if avg <= stockEpsilon {
		return 0
	}
	return math.Ceil(avg - stockEpsilon)
}

// computeItems applies the shortage rule to every item:
//
//	needed = monthly requirement - on hand - on order
//
// An item whose needed is not above zero is covered and left out. Slabs that
// are reserved for a customer stay counted in on hand: the usage average
// already includes the jobs they are for, so subtracting them again would
// count the same demand twice.
//
// The result is ordered by the largest shortfall first, then by name, so the
// list is stable between refreshes.
func computeItems(facts []itemFacts, months []string) []Item {
	out := make([]Item, 0, len(facts))
	for _, f := range facts {
		requirement := monthlyRequirement(f.usage)
		needed := requirement - f.onHand - f.onOrder
		if needed <= stockEpsilon {
			continue
		}
		usage := make([]MonthUsage, len(months))
		for i, m := range months {
			usage[i] = MonthUsage{Month: m}
			if i < len(f.usage) {
				usage[i].Used = f.usage[i]
			}
		}
		out = append(out, Item{
			InventoryItemID:    f.id,
			SKU:                f.sku,
			Name:               f.name,
			Tracking:           f.tracking,
			Unit:               f.unit,
			Usage:              usage,
			MonthlyRequirement: requirement,
			OnHand:             f.onHand,
			OnOrder:            f.onOrder,
			Needed:             roundQty(needed),
			OpenOrders:         f.orders,
		})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Needed != out[j].Needed {
			return out[i].Needed > out[j].Needed
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// roundQty trims float noise to the three decimals the ledger stores.
func roundQty(v float64) float64 { return math.Round(v*1000) / 1000 }

// lineRemaining is what a purchase order line still has to bring, in the
// item's reorder terms.
type lineRemaining struct {
	serialized  bool
	quantity    float64 // ordered, in the item's own unit
	received    float64 // already received, in the item's own unit
	expected    *int    // expected_slabs on the line
	sized       bool    // the line states a slab length and width
	mintedAll   int     // slabs created for this line, whatever their state
	mintedOpen  int     // of those, still on order
	avgSlabArea float64 // the item's average slab area; 0 when it has no slabs
}

// remaining is a line's outstanding amount: slabs for a slab-tracked item, the
// item's own unit for any other.
//
// A slab line that has been sent already has one on-order slab per ordered
// slab, so those are the truth. Before that (a draft or one awaiting approval)
// the line's expected slab count is used; a sized line with none states one
// slab; an unsized one falls back to its outstanding area over the item's
// average slab, and to a single slab when the item has none on record to
// measure against.
func (l lineRemaining) remaining() float64 {
	left := l.quantity - l.received
	if left < 0 {
		left = 0
	}
	if !l.serialized {
		return left
	}
	if l.mintedAll > 0 {
		return float64(l.mintedOpen)
	}
	if left <= stockEpsilon {
		return 0
	}
	switch {
	case l.expected != nil:
		// A line that has partly arrived keeps only the matching share of its
		// expected slabs outstanding (2 of 5 slabs' worth received leaves 3).
		// Slabs received against an unsized line are not linked back to it, so
		// the received area is the only trace of how much has come in.
		if l.quantity > stockEpsilon && l.received > stockEpsilon {
			return math.Ceil(float64(*l.expected)*left/l.quantity - stockEpsilon)
		}
		return float64(*l.expected)
	case l.sized:
		return 1
	case l.avgSlabArea > stockEpsilon:
		return math.Ceil(left/l.avgSlabArea - stockEpsilon)
	default:
		return 1
	}
}
