package inventory

import "testing"

func TestUsedArea(t *testing.T) {
	tests := []struct {
		name      string
		status    string
		area      float64
		recovered float64
		want      float64
	}{
		{"consumed with offcuts is what did not come back", "consumed", 45.208, 14.855, 30.353},
		{"consumed with nothing recovered is the whole slab", "consumed", 45.208, 0, 45.208},
		{"consumed and fully recovered is zero, never negative", "consumed", 10, 10.0004, 0},
		{"rounds to the ledger's three decimals", "consumed", 10, 3.3333333, 6.667},
		{"available stone has not been used", "available", 45.208, 0, 0},
		{"reserved stone has not been used", "reserved", 45.208, 0, 0},
		{"scrapped stone is scrap, not use", "scrapped", 45.208, 0, 0},
		{"in transit stone has not been used", "in_transit", 45.208, 0, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := usedArea(tc.status, tc.area, tc.recovered); got != tc.want {
				t.Errorf("usedArea(%q, %v, %v) = %v, want %v", tc.status, tc.area, tc.recovered, got, tc.want)
			}
		})
	}
}
