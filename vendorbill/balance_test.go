package vendorbill

import "testing"

func TestLocked_BalanceDue(t *testing.T) {
	tests := []struct {
		name       string
		grandTotal float64
		amountPaid float64
		want       float64
	}{
		{"unpaid", 100, 0, 100},
		{"partially paid", 100, 40, 60},
		{"fully paid", 100, 100, 0},
		{"overpaid floors at zero", 100, 150, 0},
		{"zero total", 0, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			l := Locked{GrandTotal: tt.grandTotal, AmountPaid: tt.amountPaid}
			if got := l.BalanceDue(); got != tt.want {
				t.Errorf("BalanceDue() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDeriveStatus(t *testing.T) {
	tests := []struct {
		name       string
		current    string
		amountPaid float64
		grandTotal float64
		want       string
	}{
		{"unpaid stays current", "APPV", 0, 100, "APPV"},
		{"partial payment", "APPV", 40, 100, "PART"},
		{"full payment", "APPV", 100, 100, "PAID"},
		{"full payment from PART", "PART", 100, 100, "PAID"},
		{"unapplied back to zero from PART", "PART", 0, 100, "APPV"},
		{"unapplied back to zero from PAID", "PAID", 0, 100, "APPV"},
		{"zero grand total counts as paid", "APPV", 0, 0, "PAID"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DeriveStatus(tt.current, tt.amountPaid, tt.grandTotal); got != tt.want {
				t.Errorf("DeriveStatus(%q, %v, %v) = %q, want %q", tt.current, tt.amountPaid, tt.grandTotal, got, tt.want)
			}
		})
	}
}
