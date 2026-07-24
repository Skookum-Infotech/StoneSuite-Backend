package purchaseorder

import "math"

func round2(x float64) float64 { return math.Round(x*100) / 100 }

// CalcLineInput holds the raw per-line quantities and rates used to compute
// line money (kept distinct from types.go's LineInput, which is the full API
// request line shape — mirrors estimate.CalcLineInput).
type CalcLineInput struct {
	Quantity, UnitPrice, DiscountPercent, TaxPercent float64
}

// LineMoney holds a line's computed subtotal, discount, tax, and total (2-dp rounded).
type LineMoney struct{ Subtotal, Discount, Tax, Total float64 }

// ComputeLine derives a line's stored money (spec AD-7).
func ComputeLine(in CalcLineInput) LineMoney {
	sub := round2(in.Quantity * in.UnitPrice)
	disc := round2(sub * in.DiscountPercent / 100)
	tax := round2((sub - disc) * in.TaxPercent / 100)
	return LineMoney{Subtotal: sub, Discount: disc, Tax: tax, Total: round2(sub - disc + tax)}
}

// HeaderMoney holds a purchase order's computed subtotal, discount total, tax
// total, and grand total.
type HeaderMoney struct{ Subtotal, DiscountTotal, TaxTotal, GrandTotal float64 }

// ComputeHeader sums line money and applies shipping + adjustment (spec AD-7).
func ComputeHeader(lines []LineMoney, shipping, adjustment float64) HeaderMoney {
	var h HeaderMoney
	for _, l := range lines {
		h.Subtotal += l.Subtotal
		h.DiscountTotal += l.Discount
		h.TaxTotal += l.Tax
	}
	h.Subtotal = round2(h.Subtotal)
	h.DiscountTotal = round2(h.DiscountTotal)
	h.TaxTotal = round2(h.TaxTotal)
	h.GrandTotal = round2(h.Subtotal - h.DiscountTotal + h.TaxTotal + shipping + adjustment)
	return h
}
