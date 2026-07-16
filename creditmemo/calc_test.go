package creditmemo

import "testing"

func TestComputeLine(t *testing.T) {
	tests := []struct {
		name string
		in   LineInput
		want LineMoney
	}{
		{
			name: "plain quantity times price",
			in:   LineInput{Quantity: 2, UnitPrice: 100},
			want: LineMoney{Subtotal: 200, Discount: 0, Tax: 0, Total: 200},
		},
		{
			name: "discount only",
			in:   LineInput{Quantity: 2, UnitPrice: 100, DiscountPercent: 10},
			want: LineMoney{Subtotal: 200, Discount: 20, Tax: 0, Total: 180},
		},
		{
			name: "tax applies after discount",
			in:   LineInput{Quantity: 2, UnitPrice: 100, DiscountPercent: 10, TaxPercent: 8.25},
			want: LineMoney{Subtotal: 200, Discount: 20, Tax: 14.85, Total: 194.85},
		},
		{
			name: "fractional quantity rounds to 2dp",
			in:   LineInput{Quantity: 1.333, UnitPrice: 9.99},
			want: LineMoney{Subtotal: 13.32, Discount: 0, Tax: 0, Total: 13.32},
		},
		{
			name: "full discount zeroes the line",
			in:   LineInput{Quantity: 5, UnitPrice: 20, DiscountPercent: 100, TaxPercent: 10},
			want: LineMoney{Subtotal: 100, Discount: 100, Tax: 0, Total: 0},
		},
		{
			name: "zero quantity",
			in:   LineInput{Quantity: 0, UnitPrice: 500, TaxPercent: 10},
			want: LineMoney{Subtotal: 0, Discount: 0, Tax: 0, Total: 0},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ComputeLine(tc.in)
			if got != tc.want {
				t.Errorf("ComputeLine(%+v) = %+v, want %+v", tc.in, got, tc.want)
			}
		})
	}
}

func TestComputeHeader(t *testing.T) {
	tests := []struct {
		name       string
		lines      []LineMoney
		adjustment float64
		applied    float64
		want       HeaderMoney
	}{
		{
			name:  "single line, nothing applied",
			lines: []LineMoney{{Subtotal: 200, Discount: 20, Tax: 14.85, Total: 194.85}},
			want: HeaderMoney{
				Subtotal: 200, DiscountTotal: 20, TaxTotal: 14.85,
				GrandTotal: 194.85, AppliedTotal: 0, UnappliedAmount: 194.85,
			},
		},
		{
			name: "multiple lines sum",
			lines: []LineMoney{
				{Subtotal: 100, Discount: 10, Tax: 9},
				{Subtotal: 50, Discount: 0, Tax: 5},
			},
			want: HeaderMoney{
				Subtotal: 150, DiscountTotal: 10, TaxTotal: 14,
				GrandTotal: 154, AppliedTotal: 0, UnappliedAmount: 154,
			},
		},
		{
			name:       "adjustment increases grand total",
			lines:      []LineMoney{{Subtotal: 100, Tax: 0}},
			adjustment: 25,
			want: HeaderMoney{
				Subtotal: 100, GrandTotal: 125, UnappliedAmount: 125,
			},
		},
		{
			name:    "partially applied",
			lines:   []LineMoney{{Subtotal: 100}},
			applied: 30,
			want: HeaderMoney{
				Subtotal: 100, GrandTotal: 100, AppliedTotal: 30, UnappliedAmount: 70,
			},
		},
		{
			name:    "fully applied leaves zero unapplied",
			lines:   []LineMoney{{Subtotal: 100}},
			applied: 100,
			want: HeaderMoney{
				Subtotal: 100, GrandTotal: 100, AppliedTotal: 100, UnappliedAmount: 0,
			},
		},
		{
			name:    "over-applied floors unapplied at zero, never negative",
			lines:   []LineMoney{{Subtotal: 100}},
			applied: 150,
			want: HeaderMoney{
				Subtotal: 100, GrandTotal: 100, AppliedTotal: 150, UnappliedAmount: 0,
			},
		},
		{
			name:  "no lines",
			lines: nil,
			want:  HeaderMoney{},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ComputeHeader(tc.lines, tc.adjustment, tc.applied)
			if got != tc.want {
				t.Errorf("ComputeHeader(%+v, %v, %v) = %+v, want %+v",
					tc.lines, tc.adjustment, tc.applied, got, tc.want)
			}
		})
	}
}

// round2 is byte-identical to invoice.round2 / payment.round2. These cases pin
// its real float64 behavior, which is not the same as decimal half-up: 1.005 is
// not exactly representable (it is ~1.00499999999999989), so x*100 lands just
// below the .5 boundary and math.Round takes it down. 2.675 is representable
// such that x*100 is exactly 267.5 and rounds up. Do not "fix" this here — the
// same helper backs invoice and payment money, and changing it would silently
// move existing AR balances.
func TestRound2(t *testing.T) {
	tests := []struct {
		name string
		in   float64
		want float64
	}{
		{"exact half rounds up", 2.675, 2.68},
		{"inexact half rounds down (1.005 < 1.005)", 1.005, 1.0},
		{"inexact half rounds down (1.015)", 1.015, 1.01},
		{"below half", 1.004, 1.0},
		{"classic float sum", 0.1 + 0.2, 0.3},
		{"negative inexact half", -1.005, -1.0},
		{"integer passthrough", 100, 100},
		{"already 2dp", 194.85, 194.85},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := round2(tc.in); got != tc.want {
				t.Errorf("round2(%v) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}
