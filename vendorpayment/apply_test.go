package vendorpayment

import "testing"

// TestCapForApply pins the cap-math for Apply (spec §8): the smaller of the
// payment's unapplied balance and the bill's outstanding balance.
func TestCapForApply(t *testing.T) {
	tests := []struct {
		name        string
		unapplied   float64
		billBalance float64
		want        float64
	}{
		{"unapplied is smaller", 100, 250, 100},
		{"bill balance is smaller", 500, 120.5, 120.5},
		{"equal caps at either", 75.25, 75.25, 75.25},
		{"zero unapplied caps at zero", 0, 500, 0},
		{"zero bill balance caps at zero", 500, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := capForApply(tt.unapplied, tt.billBalance)
			if got != tt.want {
				t.Errorf("capForApply(%v, %v) = %v, want %v", tt.unapplied, tt.billBalance, got, tt.want)
			}
		})
	}
}

// TestCapForRefund pins the cap-math for RecordRefund (spec §8, AD-5): the
// application amount minus what's already been refunded against it, floored
// at zero.
func TestCapForRefund(t *testing.T) {
	tests := []struct {
		name              string
		applicationAmount float64
		alreadyRefunded   float64
		want              float64
	}{
		{"nothing refunded yet", 500, 0, 500},
		{"partially refunded", 500, 200, 300},
		{"fully refunded", 500, 500, 0},
		{"over-refunded floors at zero", 500, 600, 0},
		{"rounds to two decimals", 1071.005, 0, 1071.01},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := capForRefund(tt.applicationAmount, tt.alreadyRefunded)
			if got != tt.want {
				t.Errorf("capForRefund(%v, %v) = %v, want %v", tt.applicationAmount, tt.alreadyRefunded, got, tt.want)
			}
		})
	}
}
