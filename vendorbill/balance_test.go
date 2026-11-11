package vendorbill

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
		pending    float64
		want       string
	}{
		{"unpaid stays current", "APPV", 0, 100, 0, "APPV"},
		{"partial payment", "APPV", 40, 100, 0, "PART"},
		{"full payment", "APPV", 100, 100, 0, "PAID"},
		{"full payment from PART", "PART", 100, 100, 0, "PAID"},
		{"unapplied back to zero from PART", "PART", 0, 100, 0, "APPV"},
		{"unapplied back to zero from PAID", "PAID", 0, 100, 0, "APPV"},
		{"overdue with partial payment resolves to PART", "ODUE", 40, 100, 0, "PART"},
		{"overdue fully paid resolves to PAID", "ODUE", 100, 100, 0, "PAID"},
		{"overdue stays overdue while unpaid", "ODUE", 0, 100, 0, "ODUE"},
		// Money recorded through a payment that has not been completed yet is
		// in flight: the bill waits at PINI however much of it is covered.
		{"in-flight payment for the full balance", "APPV", 100, 100, 100, "PINI"},
		{"in-flight payment for part of the balance", "APPV", 40, 100, 40, "PINI"},
		{"in-flight remainder on a part-settled bill", "PART", 100, 100, 60, "PINI"},
		{"completed payment settles what was initiated", "PINI", 100, 100, 0, "PAID"},
		{"completed part payment leaves the rest owed", "PINI", 40, 100, 0, "PART"},
		{"unapplied back to zero from PINI", "PINI", 0, 100, 0, "APPV"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DeriveStatus(tt.current, tt.settled, tt.grandTotal, tt.pending); got != tt.want {
				t.Errorf("DeriveStatus(%q, %v, %v, %v) = %q, want %q", tt.current, tt.settled, tt.grandTotal, tt.pending, got, tt.want)
			}
		})
	}
}
