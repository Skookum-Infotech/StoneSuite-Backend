package invoice

import "testing"

func TestComputeLine(t *testing.T) {
	tests := []struct {
		name string
		in   LineInput
		want LineMoney
	}{
		{"qty*price", LineInput{Quantity: 25.5, UnitPrice: 42.00}, LineMoney{1071.00, 0, 0, 1071.00}},
		{"5pct discount", LineInput{Quantity: 10, UnitPrice: 100, DiscountPercent: 5}, LineMoney{1000, 50, 0, 950}},
		{"discount+tax", LineInput{Quantity: 10, UnitPrice: 100, DiscountPercent: 5, TaxPercent: 8.25}, LineMoney{1000, 50, 78.38, 1028.38}},
		{"zero qty", LineInput{Quantity: 0, UnitPrice: 100}, LineMoney{0, 0, 0, 0}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ComputeLine(tt.in)
			if got != tt.want {
				t.Fatalf("ComputeLine(%+v) = %+v, want %+v", tt.in, got, tt.want)
			}
		})
	}
}

func TestComputeHeader(t *testing.T) {
	lines := []LineMoney{{1000, 50, 78.38, 1028.38}, {300, 0, 0, 300}}
	got := ComputeHeader(lines, 150.00, 0, 500.00)
	want := HeaderMoney{
		Subtotal:      1300,
		DiscountTotal: 50,
		TaxTotal:      78.38,
		GrandTotal:    1478.38,
		AmountPaid:    500.00,
		BalanceDue:    978.38,
	}
	if got != want {
		t.Fatalf("ComputeHeader = %+v, want %+v", got, want)
	}
}
