package docextract

import (
	"stonesuite-backend/docextract/internal/pdftest"
)

// tcell is a text cell at X for building rows in tests.
type tcell struct {
	x float64
	s string
}

// trow builds a Row at height y; word widths come from Helvetica metrics.
func trow(y float64, cells ...tcell) Row {
	r := Row{Y: y}
	for _, c := range cells {
		r.Words = append(r.Words, Word{X: c.x, W: pdftest.Width(c.s, 10), Text: c.s})
	}
	return r
}

// trowSplit builds a row where each cell string is split into words at spaces.
func trowSplit(y float64, cells ...tcell) Row {
	r := Row{Y: y}
	for _, c := range cells {
		x := c.x
		for _, w := range splitSpaces(c.s) {
			wd := pdftest.Width(w, 10)
			r.Words = append(r.Words, Word{X: x, W: wd, Text: w})
			x += wd + pdftest.Width(" ", 10)
		}
	}
	return r
}

func splitSpaces(s string) []string {
	var out []string
	cur := ""
	for _, r := range s {
		if r == ' ' {
			if cur != "" {
				out = append(out, cur)
			}
			cur = ""
			continue
		}
		cur += string(r)
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

// stdHeader is a standard line-table header row at y.
func stdHeader(y float64) Row {
	return trow(y, tcell{50, "Item"}, tcell{110, "Description"}, tcell{330, "Qty"}, tcell{362, "UoM"}, tcell{420, "Unit Price"}, tcell{500, "Amount"})
}

// stdLine is a standard data row at y.
func stdLine(y float64, sku, desc, qty, uom, price, amount string) Row {
	var cells []tcell
	for _, c := range []tcell{{50, sku}, {110, desc}, {330, qty}, {362, uom}, {420, price}, {500, amount}} {
		if c.s != "" {
			cells = append(cells, c)
		}
	}
	return trow(y, cells...)
}

// noReconcile is a stub that skips arithmetic checks so tests never depend on
// the human-owned withinTolerance rule.
func noReconcile([]Cents, Cents, Cents, Cents, Cents, Cents) []CheckResult { return nil }

// exactReconcile compares sums exactly, independent of withinTolerance.
func exactReconcile(lines []Cents, subtotal, _, _, _, _ Cents) []CheckResult {
	var sum Cents
	for _, l := range lines {
		sum += l
	}
	if subtotal == 0 {
		return nil
	}
	return []CheckResult{{Name: CheckLinesVsSubtotal, Expected: subtotal, Actual: sum, Passed: sum == subtotal}}
}
