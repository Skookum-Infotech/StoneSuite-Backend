package docextract

// Cents is a money amount in integer cents. Extraction never uses float64 for
// money: a parsed "1,234.56" becomes Cents(123456), so sums are exact and the
// only imprecision left is the document's own rounding.
type Cents int64

// CheckResult is the outcome of one arithmetic self-check.
type CheckResult struct {
	Name     string `json:"name"`
	Expected Cents  `json:"expected"`
	Actual   Cents  `json:"actual"`
	Passed   bool   `json:"passed"`
}

// toleranceFloorCents is the minimum rounding slack for any check.
const toleranceFloorCents Cents = 2

// Check names surfaced to the review UI.
const (
	CheckLinesVsSubtotal = "lines_vs_subtotal"
	CheckTotal           = "total"
)

// withinTolerance reports whether actual is close enough to expected that the
// difference is explained by the document's own rounding rather than by a
// misread number. lineCount is how many rounded amounts were summed to produce
// actual (each line amount on a printed document is rounded to the cent
// independently, so the error can grow by up to half a cent per line).
//
// A false here never "fixes" anything: the caller flags the totals amber and
// the user decides.
//
// The allowance is half a cent per summed line, rounded up, with a floor of
// toleranceFloorCents (covers a tax or shipping figure rounded separately).
// No relative (%) slack: a misread digit on a large order is exactly what this
// check exists to catch, and a percentage would hide it.
func withinTolerance(expected, actual Cents, lineCount int) bool {
	diff := expected - actual
	if diff < 0 {
		diff = -diff
	}
	allowed := Cents((lineCount + 1) / 2) // ceil(lineCount * 0.5 cent)
	if allowed < toleranceFloorCents {
		allowed = toleranceFloorCents
	}
	return diff <= allowed
}

// Reconcile runs the arithmetic self-checks: sum of line amounts vs the
// subtotal, and subtotal + tax + shipping - discount vs the total. A zero
// expected value means the document did not print that figure, so the check
// is skipped rather than failed.
func Reconcile(lineAmounts []Cents, subtotal, tax, shipping, discount, total Cents) []CheckResult {
	var sum Cents
	for _, a := range lineAmounts {
		sum += a
	}
	var out []CheckResult
	if subtotal != 0 {
		out = append(out, CheckResult{
			Name: CheckLinesVsSubtotal, Expected: subtotal, Actual: sum,
			Passed: withinTolerance(subtotal, sum, len(lineAmounts)),
		})
	}
	if total != 0 {
		base := subtotal
		if base == 0 {
			base = sum
		}
		computed := base + tax + shipping - discount
		out = append(out, CheckResult{
			Name: CheckTotal, Expected: total, Actual: computed,
			Passed: withinTolerance(total, computed, 1),
		})
	}
	return out
}
