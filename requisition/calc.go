package requisition

import "math"

func round2(x float64) float64 { return math.Round(x*100) / 100 }

// CalcLineInput holds the raw per-line quantity and rate used to compute
// line money. Simplified vs. purchaseorder.CalcLineInput (AD-3): no
// discount, no per-line tax — a requisition is a rough ask, not a priced
// commitment.
type CalcLineInput struct {
	Quantity, EstimatedUnitPrice float64
}

// LineMoney holds a line's computed estimated amount (2-dp rounded).
type LineMoney struct{ EstimatedAmount float64 }

// ComputeLine derives a line's stored money (AD-9).
func ComputeLine(in CalcLineInput) LineMoney {
	return LineMoney{EstimatedAmount: round2(in.Quantity * in.EstimatedUnitPrice)}
}

// HeaderMoney holds a requisition's computed subtotal, tax total, and
// estimated grand total.
type HeaderMoney struct{ Subtotal, TaxTotal, EstimatedTotal float64 }

// ComputeHeader sums line money and applies the header's flat sales tax
// percent (AD-9: no shipping/adjustment terms — those belong to the PO).
func ComputeHeader(lines []LineMoney, salesTaxPercent float64) HeaderMoney {
	var h HeaderMoney
	for _, l := range lines {
		h.Subtotal += l.EstimatedAmount
	}
	h.Subtotal = round2(h.Subtotal)
	h.TaxTotal = round2(h.Subtotal * salesTaxPercent / 100)
	h.EstimatedTotal = round2(h.Subtotal + h.TaxTotal)
	return h
}
