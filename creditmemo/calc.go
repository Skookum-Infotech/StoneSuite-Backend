package creditmemo

import "math"

func round2(x float64) float64 { return math.Round(x*100) / 100 }

// LineInput holds the raw per-line quantities and rates used to compute line money.
type LineInput struct {
	Quantity, UnitPrice, DiscountPercent, TaxPercent float64
}

// LineMoney holds a line's computed subtotal, discount, tax, and total (2-dp rounded).
type LineMoney struct{ Subtotal, Discount, Tax, Total float64 }

// ComputeLine derives a credit memo line's stored money (spec §8).
func ComputeLine(in LineInput) LineMoney {
	sub := round2(in.Quantity * in.UnitPrice)
	disc := round2(sub * in.DiscountPercent / 100)
	tax := round2((sub - disc) * in.TaxPercent / 100)
	return LineMoney{Subtotal: sub, Discount: disc, Tax: tax, Total: round2(sub - disc + tax)}
}

// HeaderMoney holds a credit memo's computed totals and application rollup.
type HeaderMoney struct {
	Subtotal, DiscountTotal, TaxTotal, GrandTotal, AppliedTotal, UnappliedAmount float64
}

// ComputeHeader sums line money, applies the adjustment, and derives the
// unapplied balance. There is no shipping charge on a credit memo: crediting a
// customer for freight is an adjustment line, not a shipping charge.
func ComputeHeader(lines []LineMoney, adjustment, appliedTotal float64) HeaderMoney {
	var h HeaderMoney
	for _, l := range lines {
		h.Subtotal += l.Subtotal
		h.DiscountTotal += l.Discount
		h.TaxTotal += l.Tax
	}
	h.Subtotal = round2(h.Subtotal)
	h.DiscountTotal = round2(h.DiscountTotal)
	h.TaxTotal = round2(h.TaxTotal)
	h.GrandTotal = round2(h.Subtotal - h.DiscountTotal + h.TaxTotal + adjustment)
	h.AppliedTotal = round2(appliedTotal)
	h.UnappliedAmount = round2(h.GrandTotal - h.AppliedTotal)
	if h.UnappliedAmount < 0 {
		h.UnappliedAmount = 0
	}
	return h
}
