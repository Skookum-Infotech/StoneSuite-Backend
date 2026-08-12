package vendorbill

import "math"

func round2(x float64) float64 { return math.Round(x*100) / 100 }

// CalcLineInput holds the raw per-line quantities and rates used to compute
// line money.
type CalcLineInput struct {
	Quantity, UnitPrice, DiscountPercent, TaxPercent float64
}

// LineMoney holds a line's computed subtotal, discount, tax, and total (2-dp rounded).
type LineMoney struct{ Subtotal, Discount, Tax, Total float64 }

// ComputeLine derives a line's stored money (AD-9).
func ComputeLine(in CalcLineInput) LineMoney {
	sub := round2(in.Quantity * in.UnitPrice)
	disc := round2(sub * in.DiscountPercent / 100)
	tax := round2((sub - disc) * in.TaxPercent / 100)
	return LineMoney{Subtotal: sub, Discount: disc, Tax: tax, Total: round2(sub - disc + tax)}
}

// HeaderMoney holds a vendor bill's computed totals and balance.
type HeaderMoney struct {
	Subtotal, DiscountTotal, TaxTotal, GrandTotal, AmountPaid, BalanceDue float64
}

// ComputeHeader sums line money and applies the flat adjustment, then
// computes balance due (AD-9). There is no shipping term -- a vendor bill has
// no separate shipping-charge field (AD-13).
func ComputeHeader(lines []LineMoney, adjustment, amountPaid float64) HeaderMoney {
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
	h.AmountPaid = round2(amountPaid)
	h.BalanceDue = round2(h.GrandTotal - h.AmountPaid)
	if h.BalanceDue < 0 {
		h.BalanceDue = 0
	}
	return h
}
