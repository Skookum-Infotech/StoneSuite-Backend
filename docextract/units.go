package docextract

import (
	"math"
	"strings"
)

// Unit categories; conversion is only defined inside one category.
const (
	catArea   = "area"
	catLength = "length"
	catMass   = "mass"
	catCount  = "count"
)

// maxConvertDriftCents is the largest accepted change in line amount.
const maxConvertDriftCents = 1

type unitDef struct {
	category string
	toBase   float64 // multiply a quantity in this unit to get the category base unit
}

// unitTable maps canonical unit names to their category and base factor.
// Bases: area sqft, length metre, mass kg, count ea.
var unitTable = map[string]unitDef{
	"sqft": {catArea, 1},
	"sqm":  {catArea, 10.7639104167},
	"in":   {catLength, 0.0254},
	"ft":   {catLength, 0.3048},
	"mm":   {catLength, 0.001},
	"cm":   {catLength, 0.01},
	"m":    {catLength, 1},
	"lb":   {catMass, 0.45359237},
	"kg":   {catMass, 1},
	"ea":   {catCount, 1},
}

// unitAliases maps normalised spellings to canonical unit names.
var unitAliases = map[string]string{
	"sqft": "sqft", "sf": "sqft", "ft2": "sqft", "sqfeet": "sqft", "squarefeet": "sqft", "squarefoot": "sqft", "sqfoot": "sqft",
	"sqm": "sqm", "m2": "sqm", "sqmeter": "sqm", "sqmeters": "sqm", "squaremeter": "sqm", "squaremeters": "sqm", "squaremetre": "sqm", "squaremetres": "sqm",
	"in": "in", "inch": "in", "inches": "in",
	"ft": "ft", "feet": "ft", "foot": "ft", "lf": "ft", "linft": "ft", "linearft": "ft", "linearfeet": "ft",
	"mm": "mm", "millimeter": "mm", "millimeters": "mm",
	"cm": "cm", "centimeter": "cm", "centimeters": "cm",
	"m": "m", "meter": "m", "meters": "m", "metre": "m", "metres": "m",
	"lb": "lb", "lbs": "lb", "pound": "lb", "pounds": "lb",
	"kg": "kg", "kgs": "kg", "kilogram": "kg", "kilograms": "kg",
	"ea": "ea", "each": "ea", "pc": "ea", "pcs": "ea", "piece": "ea", "pieces": "ea", "unit": "ea", "units": "ea",
}

// NormalizeUnit maps a document UoM spelling to a canonical unit and its
// category. ok is false for unknown text.
func NormalizeUnit(s string) (canon, category string, ok bool) {
	k := strings.ToLower(strings.TrimSpace(s))
	k = strings.NewReplacer("²", "2", ".", "", " ", "", "-", "", "_", "").Replace(k)
	canon, ok = unitAliases[k]
	if !ok {
		return "", "", false
	}
	return canon, unitTable[canon].category, true
}

// Convert converts a line from one unit to another of the same category,
// scaling the unit price inversely so the line amount is preserved. ok is
// false for unknown units, cross-category pairs, or when rounding the price to
// a cent drifts the amount by more than one cent.
func Convert(qtyMilli int64, unitPrice Cents, from, to string) (newQty int64, newPrice Cents, ok bool) {
	fc, fcat, fok := NormalizeUnit(from)
	tc, tcat, tok := NormalizeUnit(to)
	if !fok || !tok || fcat != tcat {
		return 0, 0, false
	}
	if fc == tc {
		return qtyMilli, unitPrice, true
	}
	factor := unitTable[fc].toBase / unitTable[tc].toBase
	newQty = int64(math.Round(float64(qtyMilli) * factor))
	newPrice = Cents(math.Round(float64(unitPrice) / factor))
	oldAmt := lineAmount(qtyMilli, unitPrice)
	newAmt := lineAmount(newQty, newPrice)
	diff := oldAmt - newAmt
	if diff < 0 {
		diff = -diff
	}
	if diff > maxConvertDriftCents {
		return 0, 0, false
	}
	return newQty, newPrice, true
}

// lineAmount is qty (milli) x price (cents), rounded to the cent.
func lineAmount(qtyMilli int64, price Cents) Cents {
	return Cents(math.Round(float64(qtyMilli) * float64(price) / milliPerUnit))
}
