package invoice

import "testing"

func TestLockedBalanceDue(t *testing.T) {
	tests := []struct {
		name string
		l    Locked
		want float64
	}{
		{"unpaid", Locked{GrandTotal: 100, AmountPaid: 0}, 100},
		{"partially paid", Locked{GrandTotal: 100, AmountPaid: 40}, 60},
		{"fully paid", Locked{GrandTotal: 100, AmountPaid: 100}, 0},
		{"overpaid never negative", Locked{GrandTotal: 100, AmountPaid: 150}, 0},
		{"credit memo only settles balance", Locked{GrandTotal: 100, CreditTotal: 40}, 60},
		{"cash and credit combine", Locked{GrandTotal: 100, AmountPaid: 30, CreditTotal: 30}, 40},
		{"cash and credit together fully settle", Locked{GrandTotal: 100, AmountPaid: 60, CreditTotal: 40}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.l.BalanceDue(); got != tt.want {
				t.Errorf("BalanceDue() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDeriveStatus(t *testing.T) {
	tests := []struct {
		name       string
		current    string
		settled    float64
		grandTotal float64
		want       string
	}{
		{"unpaid stays current", "SENT", 0, 100, "SENT"},
		{"partial payment", "SENT", 40, 100, "PART"},
		{"full payment", "SENT", 100, 100, "PAID"},
		{"full payment from PART", "PART", 100, 100, "PAID"},
		{"unapplied back to zero from PART", "PART", 0, 100, "SENT"},
		{"unapplied back to zero from PAID", "PAID", 0, 100, "SENT"},
		{"overdue with partial payment resolves to PART", "ODUE", 40, 100, "PART"},
		{"overdue fully paid resolves to PAID", "ODUE", 100, 100, "PAID"},
		{"overdue stays overdue while unpaid", "ODUE", 0, 100, "ODUE"},
		{"rounding epsilon treated as fully paid", "SENT", 99.996, 100, "PAID"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DeriveStatus(tt.current, tt.settled, tt.grandTotal); got != tt.want {
				t.Errorf("DeriveStatus(%q, %v, %v) = %q, want %q", tt.current, tt.settled, tt.grandTotal, got, tt.want)
			}
		})
	}
}
