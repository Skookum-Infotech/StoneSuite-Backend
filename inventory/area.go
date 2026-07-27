package inventory

// area.go — dimension/area arithmetic for serialized units. Pure: no database,
// no context. This is the cheapest place in the module to be correct.
//
// Why this exists at all: inventory_slab stores millimetre dimensions plus a
// separately-unit'd slab_area, while inventory_ledger.quantity_delta is "in the
// item's own unit". NOTHING in the schema forces slab_area_unit_id to equal
// inventory_item_unit_id, so a slab measured in SQM ledgered against a SQFT
// item is off by 10.76x and no constraint catches it. Every area therefore gets
// computed here, from the millimetres, into the item's unit — a client-supplied
// area is never trusted.

import (
	"fmt"
	"math"
)

// Unit-of-measure categories, mirroring lkp_unit.unit_category.
const (
	UnitCategoryCount  = "count"
	UnitCategoryLength = "length"
	UnitCategoryArea   = "area"
	UnitCategoryVolume = "volume"
	UnitCategoryWeight = "weight"
)

// Seeded lkp_unit codes this package converts into.
const (
	UnitCodeSquareFoot = "SQFT"
	UnitCodeSquareM    = "SQM"
	UnitCodeLinearFoot = "LFT"
)

// Exact conversion constants. One international foot is 304.8 mm by definition,
// so a square foot is 304.8^2 mm^2 exactly — not an approximation.
const mmPerFoot = 304.8

// areaScale is the DECIMAL(14,3) scale of slab_area and quantity_delta. Values
// are rounded to it here so that what Go believes and what PostgreSQL stores
// are the same number — otherwise the on-hand-equals-sum-of-ledger invariant
// drifts by a rounding epsilon on every movement.
const areaScale = 3

// AreaFor returns the quantity that a single physical piece of the given
// millimetre dimensions contributes to stock, expressed in the item's own unit.
//
// For a count-category item (EA, SLAB, PLT...) the answer is exactly 1: the
// piece counts once regardless of its size, and returning an area there would
// silently ledger square feet against a "each" item.
func AreaFor(lengthMM, widthMM float64, unitCode, unitCategory string) (float64, error) {
	if unitCategory == UnitCategoryCount {
		return 1, nil
	}
	if lengthMM <= 0 || widthMM <= 0 {
		return 0, ClientError{Msg: "Length and width must both be greater than zero."}
	}
	switch unitCategory {
	case UnitCategoryArea:
		switch unitCode {
		case UnitCodeSquareFoot:
			return roundTo(lengthMM*widthMM/(mmPerFoot*mmPerFoot), areaScale), nil
		case UnitCodeSquareM:
			return roundTo(lengthMM*widthMM/1_000_000, areaScale), nil
		}
		return 0, ClientError{Msg: fmt.Sprintf("Unsupported area unit %q for a measured piece.", unitCode)}
	case UnitCategoryLength:
		if unitCode == UnitCodeLinearFoot {
			return roundTo(lengthMM/mmPerFoot, areaScale), nil
		}
		return 0, ClientError{Msg: fmt.Sprintf("Unsupported length unit %q for a measured piece.", unitCode)}
	}
	// Volume and weight cannot be derived from two dimensions, so refusing is
	// the only honest answer. Guessing here would put a fabricated number into
	// the ledger, which is worse than rejecting the write.
	return 0, ClientError{Msg: fmt.Sprintf("Cannot derive a %s quantity from length and width alone.", unitCategory)}
}

// CutOutcome is the result of cutting a parent piece into children.
type CutOutcome struct {
	// ParentArea is consumed from stock in full.
	ParentArea float64
	// RecoveredArea is the total area of the children that are kept.
	RecoveredArea float64
	// ScrappedArea is what the cut destroyed: saw kerf plus any offcut too
	// small to be worth keeping. Always >= 0.
	ScrappedArea float64
}

// PlanCut works out the stock movements for cutting a parent piece into the
// given children.
//
// Area is NOT conserved by a cut and pretending otherwise is the bug this
// function exists to prevent: a saw kerf removes 3-5mm on every pass and small
// offcuts get dropped. So the parent's full area is consumed, each retained
// child is recovered at its own measured area, and the difference is recorded
// as scrap. SUM(ledger deltas) therefore still reconciles with on-hand instead
// of silently drifting down by the kerf on every cut.
func PlanCut(parentArea float64, childAreas []float64) (CutOutcome, error) {
	if parentArea <= 0 {
		return CutOutcome{}, ClientError{Msg: "The piece being cut has no recorded area."}
	}
	var recovered float64
	for _, a := range childAreas {
		if a <= 0 {
			return CutOutcome{}, ClientError{Msg: "Every resulting piece must have a positive area."}
		}
		recovered += a
	}
	recovered = roundTo(recovered, areaScale)
	if recovered > parentArea {
		// Physically impossible, and if it were allowed it would manufacture
		// stock out of a saw cut.
		return CutOutcome{}, ClientError{
			Msg: "The resulting pieces total more area than the piece being cut.",
		}
	}
	return CutOutcome{
		ParentArea:    parentArea,
		RecoveredArea: recovered,
		ScrappedArea:  roundTo(parentArea-recovered, areaScale),
	}, nil
}

// IsUsableRemnant reports whether an offcut clears the shop's minimum useful
// rectangle and is therefore worth putting back into stock as a remnant.
//
// Evaluated at cut time and stored, never derived on read: deriving it would
// mean a later change to the threshold silently reclassifies last year's
// inventory. Below the threshold the piece is scrap and never enters available
// stock, which is what keeps the yard from accumulating thousands of worthless
// offcuts that inflate on-hand area and make the remnant picker unusable.
func IsUsableRemnant(lengthMM, widthMM, minLengthMM, minWidthMM float64) bool {
	if minLengthMM <= 0 && minWidthMM <= 0 {
		return true // no policy configured: keep everything, decide later
	}
	// Compare against the piece's own long and short edge rather than the
	// nominal length/width, so a rotated offcut is not misjudged.
	long, short := math.Max(lengthMM, widthMM), math.Min(lengthMM, widthMM)
	minLong, minShort := math.Max(minLengthMM, minWidthMM), math.Min(minLengthMM, minWidthMM)
	return long >= minLong && short >= minShort
}

// roundTo rounds to n decimal places, half away from zero — matching what
// PostgreSQL does when storing into a DECIMAL(_, n).
func roundTo(v float64, n int) float64 {
	p := math.Pow(10, float64(n))
	return math.Round(v*p) / p
}
