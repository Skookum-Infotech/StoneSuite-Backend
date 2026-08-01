package requisition

import "testing"

func TestComputeLine(t *testing.T) {
	tests := []struct {
		name string
		in   CalcLineInput
		want LineMoney
	}{
		{"basic", CalcLineInput{Quantity: 2, EstimatedUnitPrice: 25}, LineMoney{EstimatedAmount: 50}},
		{"fractional rounds", CalcLineInput{Quantity: 3, EstimatedUnitPrice: 10.005}, LineMoney{EstimatedAmount: 30.02}},
		{"zero quantity", CalcLineInput{Quantity: 0, EstimatedUnitPrice: 100}, LineMoney{EstimatedAmount: 0}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ComputeLine(tt.in); got != tt.want {
				t.Errorf("ComputeLine(%+v) = %+v, want %+v", tt.in, got, tt.want)
			}
		})
	}
}

func TestComputeHeader(t *testing.T) {
	lines := []LineMoney{{EstimatedAmount: 50}, {EstimatedAmount: 25}}
	got := ComputeHeader(lines, 8)
	want := HeaderMoney{Subtotal: 75, TaxTotal: 6, EstimatedTotal: 81}
	if got != want {
		t.Errorf("ComputeHeader = %+v, want %+v", got, want)
	}
}

func TestComputeHeader_NoLines(t *testing.T) {
	got := ComputeHeader(nil, 8)
	want := HeaderMoney{}
	if got != want {
		t.Errorf("ComputeHeader(nil) = %+v, want %+v", got, want)
	}
}

func TestComputeHeader_ZeroTax(t *testing.T) {
	lines := []LineMoney{{EstimatedAmount: 100}}
	got := ComputeHeader(lines, 0)
	want := HeaderMoney{Subtotal: 100, TaxTotal: 0, EstimatedTotal: 100}
	if got != want {
		t.Errorf("ComputeHeader = %+v, want %+v", got, want)
	}
}
