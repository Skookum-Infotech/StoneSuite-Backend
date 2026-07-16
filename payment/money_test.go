package payment

import "testing"

func TestRound2(t *testing.T) {
	tests := []struct {
		in   float64
		want float64
	}{
		{1071.005, 1071.01},
		{1071.004, 1071.00},
		{0, 0},
		{-5.005, -5.01}, // math.Round rounds half away from zero; not used for negative amounts in this module but pin the behavior
	}
	for _, tt := range tests {
		if got := round2(tt.in); got != tt.want {
			t.Errorf("round2(%v) = %v, want %v", tt.in, got, tt.want)
		}
	}
}
