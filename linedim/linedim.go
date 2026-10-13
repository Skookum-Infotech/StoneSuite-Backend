// Package linedim resolves the length / width / slabs a document line can carry.
//
// Stone is priced by area but sold and ordered by the slab, so a line may state
// the size of one slab (length and width) and how many slabs it covers. When it
// does, the line's quantity is NOT a separate input: it is the area, length x
// width x slabs, expressed in the item's own area unit. The server derives it
// here rather than trusting the figure the client sent, so a stale or
// hand-edited client can never store a quantity that disagrees with the
// dimensions printed next to it.
//
// Length and width are always measured in ONE unit — the line's dimension unit,
// copied from its inventory item when the line is added — so a slab can never be
// "120 in x 300 cm". The unit travels with the numbers: a saved line keeps the
// unit it was entered in, whatever its item is later changed to.
//
// The numbers are only descriptive otherwise — a line's money, stock holds,
// receiving and conversions all keep running off `quantity`, which is why this
// is validated once here instead of in each document module that stores it.
package linedim

import (
	"fmt"
	"math"
	"strings"
)

// Area units a line can be measured in. Anything else (EA, SLAB, a service) has
// no meaningful "length x width", so dimensions are refused on it.
const (
	UnitSquareFoot  = "SQFT"
	UnitSquareMeter = "SQM"
)

// Units a slab's length and width can be measured in.
const (
	UnitMillimeter = "mm"
	UnitCentimeter = "cm"
	UnitInch       = "in"
	UnitFoot       = "ft"
	UnitMeter      = "m"

	// DefaultUnit is what an item or line without a stated unit is measured in.
	DefaultUnit = UnitInch
)

// Units lists every dimension unit, smallest first, for messages and validation.
var Units = []string{UnitMillimeter, UnitCentimeter, UnitInch, UnitFoot, UnitMeter}

// cmPerUnit is how many centimetres one unit is. Exact by definition: an inch is
// 2.54 cm and a foot 30.48 cm.
var cmPerUnit = map[string]float64{
	UnitMillimeter: 0.1,
	UnitCentimeter: 1,
	UnitInch:       2.54,
	UnitFoot:       30.48,
	UnitMeter:      100,
}

// cm2PerSquareUnit is how many cm^2 one square unit is — cmPerUnit squared, kept
// as written-out constants so the web client mirrors the identical figures and
// the two round the same way.
var cm2PerSquareUnit = map[string]float64{
	UnitMillimeter: 0.01,
	UnitCentimeter: 1,
	UnitInch:       6.4516,
	UnitFoot:       929.0304,
	UnitMeter:      10000,
}

// Exact cm^2 in one unit of area.
const (
	cm2PerSquareFoot  = 929.0304
	cm2PerSquareMeter = 10000.0
)

// Bounds. They keep a typo from reaching DECIMAL(10,2) / DECIMAL(14,3) as an
// overflow (a 500) — an absurd value is a 400 instead.
const (
	// MaxDimensionCM is the longest length or width a line accepts (100 m),
	// measured after conversion to centimetres so it means the same in any unit.
	MaxDimensionCM = 10000.0
	// MaxSlabs is the largest slab count a line accepts. It matches the purchase
	// order's expected-slabs cap, which a sized PO line shares.
	MaxSlabs = 10000
)

// quantityScale is the DECIMAL(14,3) scale of a line's quantity.
const quantityScale = 1000.0

// NormalizeUnit validates a dimension unit. Blank means "not stated" and maps to
// DefaultUnit; case and surrounding space are ignored.
func NormalizeUnit(raw string) (string, bool) {
	u := strings.ToLower(strings.TrimSpace(raw))
	if u == "" {
		return DefaultUnit, true
	}
	if _, ok := cmPerUnit[u]; ok {
		return u, true
	}
	return "", false
}

// Input is what a client sent for one line. A nil field means "not provided".
type Input struct {
	Length *float64
	Width  *float64
	Slabs  *int
	// Unit is the line's dimension unit as sent, or the item's when the client
	// sent none. Blank resolves to DefaultUnit.
	Unit string
}

// IsZero reports whether the client sent none of length, width or slabs.
func (in Input) IsZero() bool {
	return in.Length == nil && in.Width == nil && in.Slabs == nil
}

// HasArea reports whether both dimensions were sent — the condition under which
// the line's quantity is derived from them.
func (in Input) HasArea() bool { return in.Length != nil && in.Width != nil }

// Dims is what a line persists. The zero value is "none".
type Dims struct {
	Length *float64
	Width  *float64
	Slabs  *int
	// Unit is the unit Length and Width are measured in; "" when the line has none.
	Unit string
}

// IsZero reports whether the line carries no dimensions.
func (d Dims) IsZero() bool {
	return d.Length == nil && d.Width == nil && d.Slabs == nil
}

// StoredUnit is the value to write to the unit column: the line's unit, or the
// default for a line without dimensions (the column is NOT NULL).
func (d Dims) StoredUnit() string {
	if d.Unit == "" {
		return DefaultUnit
	}
	return d.Unit
}

// Result is the outcome of resolving one line's dimensions.
type Result struct {
	Dims Dims
	// Quantity is the derived area; meaningful only when Derived is true.
	Quantity float64
	// Derived is true when length and width were both given, so Quantity replaces
	// whatever the client sent as the line's quantity.
	Derived bool
}

// Resolve validates one line's dimensions against its area unit and derives the
// quantity they imply. areaUnit is the line's snapshotted pricing unit ("" for a
// free-text line). A returned message is a client error worded to follow
// "Line N: ".
//
// Rules:
//   - nothing sent: no dimensions, quantity untouched.
//   - the unit must be one of Units (blank means DefaultUnit).
//   - length and width go together; each must be > 0 and within MaxDimensionCM
//     once converted to centimetres.
//   - slabs must be a whole number from 1 to MaxSlabs.
//   - any of the three needs an area unit (SQFT / SQM) on the line.
//   - length + width present: quantity = round3(length x width x slabs, in the
//     area unit), with slabs defaulting to 1 when blank. The assumed 1 is NOT
//     written back — a blank slab count stays blank.
func Resolve(in Input, areaUnit string) (Result, string) {
	if in.IsZero() {
		return Result{}, ""
	}
	unit, ok := NormalizeUnit(in.Unit)
	if !ok {
		return Result{}, fmt.Sprintf("the dimension unit must be one of %s.", strings.Join(Units, ", "))
	}
	if (in.Length == nil) != (in.Width == nil) {
		return Result{}, "enter both length and width, or neither."
	}
	divisor, ok := divisorFor(areaUnit)
	if !ok {
		return Result{}, "length, width and slabs can only be used on a line measured in SQFT or SQM."
	}
	if in.Slabs != nil && (*in.Slabs < 1 || *in.Slabs > MaxSlabs) {
		return Result{}, fmt.Sprintf("slabs must be a whole number from 1 to %d.", MaxSlabs)
	}
	if !in.HasArea() {
		// Slabs alone: a count to carry along, nothing to derive.
		return Result{Dims: Dims{Slabs: in.Slabs, Unit: unit}}, ""
	}

	length, width := round2(*in.Length), round2(*in.Width)
	if !(length > 0) || !(width > 0) {
		return Result{}, "length and width must both be greater than zero."
	}
	if length*cmPerUnit[unit] > MaxDimensionCM || width*cmPerUnit[unit] > MaxDimensionCM {
		return Result{}, "length and width cannot exceed 100 m."
	}

	count := 1
	if in.Slabs != nil {
		count = *in.Slabs
	}
	qty := Area(length, width, count, unit, divisor)
	if !(qty > 0) {
		return Result{}, "the area is too small to price."
	}
	return Result{
		Dims:     Dims{Length: &length, Width: &width, Slabs: in.Slabs, Unit: unit},
		Quantity: qty,
		Derived:  true,
	}, ""
}

// Area is length x width x slabs, measured in `unit`, expressed in the area unit
// whose cm^2 divisor is given, rounded to the quantity's three decimals. The
// expression order matches the web client's so both round the same way.
func Area(length, width float64, slabs int, unit string, divisor float64) float64 {
	return math.Round(length*width*float64(slabs)*cm2PerSquareUnit[unit]/divisor*quantityScale) / quantityScale
}

// divisorFor returns the cm^2-per-unit divisor for an area unit code.
func divisorFor(areaUnit string) (float64, bool) {
	switch strings.ToUpper(strings.TrimSpace(areaUnit)) {
	case UnitSquareFoot:
		return cm2PerSquareFoot, true
	case UnitSquareMeter:
		return cm2PerSquareMeter, true
	}
	return 0, false
}

// IsAreaUnit reports whether dimensions make sense on a line in this unit.
func IsAreaUnit(areaUnit string) bool {
	_, ok := divisorFor(areaUnit)
	return ok
}

func round2(x float64) float64 { return math.Round(x*100) / 100 }
