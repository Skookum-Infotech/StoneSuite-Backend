package purchaseorder

import "testing"

func TestComputeLine(t *testing.T) {
	tests := []struct {
		name string
		in   CalcLineInput
		want LineMoney
	}{
		{
			name: "no discount no tax",
			in:   CalcLineInput{Quantity: 2, UnitPrice: 10, DiscountPercent: 0, TaxPercent: 0},
			want: LineMoney{Subtotal: 20, Discount: 0, Tax: 0, Total: 20},
		},
		{
			name: "discount and tax",
			in:   CalcLineInput{Quantity: 3, UnitPrice: 100, DiscountPercent: 10, TaxPercent: 8.25},
			// subtotal=300, discount=30, taxable=270, tax=22.28 (270*0.0825=22.275 -> round 22.28), total=292.28
			want: LineMoney{Subtotal: 300, Discount: 30, Tax: 22.28, Total: 292.28},
		},
		{
			name: "fractional quantity",
			in:   CalcLineInput{Quantity: 2.5, UnitPrice: 19.99, DiscountPercent: 5, TaxPercent: 0},
			// subtotal=49.975 -> round2(49.975)=49.97 due to IEEE754
			want: LineMoney{Subtotal: 49.97, Discount: 2.5, Tax: 0, Total: 47.47},
		},
		{
			name: "zero quantity",
			in:   CalcLineInput{Quantity: 0, UnitPrice: 99.99, DiscountPercent: 10, TaxPercent: 5},
			want: LineMoney{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ComputeLine(tt.in); got != tt.want {
				t.Fatalf("ComputeLine(%+v) = %+v, want %+v", tt.in, got, tt.want)
			}
		})
	}
}

func TestComputeHeader(t *testing.T) {
	tests := []struct {
		name                 string
		lines                []LineMoney
		shipping, adjustment float64
		want                 HeaderMoney
	}{
		{
			name:  "single line no shipping no adjustment",
			lines: []LineMoney{{Subtotal: 100, Discount: 10, Tax: 7.2, Total: 97.2}},
			want:  HeaderMoney{Subtotal: 100, DiscountTotal: 10, TaxTotal: 7.2, GrandTotal: 97.2},
		},
		{
			name: "multiple lines with shipping and adjustment",
			lines: []LineMoney{
				{Subtotal: 100, Discount: 10, Tax: 7.2, Total: 97.2},
				{Subtotal: 50, Discount: 0, Tax: 4.13, Total: 54.13},
			},
			shipping:   15,
			adjustment: -5,
			// subtotal=150, discount=10, tax=11.33, grand=150-10+11.33+15-5=161.33
			want: HeaderMoney{Subtotal: 150, DiscountTotal: 10, TaxTotal: 11.33, GrandTotal: 161.33},
		},
		{
			name:  "no lines",
			lines: nil,
			want:  HeaderMoney{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ComputeHeader(tt.lines, tt.shipping, tt.adjustment); got != tt.want {
				t.Fatalf("ComputeHeader(%+v, %v, %v) = %+v, want %+v", tt.lines, tt.shipping, tt.adjustment, got, tt.want)
			}
		})
	}
}
