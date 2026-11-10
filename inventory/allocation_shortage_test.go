package inventory

import "testing"

func TestShortfall(t *testing.T) {
	base := Shortage{ItemID: "i", SKU: "GRN", Name: "Granite", UnitCode: "SQFT"}
	cases := []struct {
		name                 string
		need, free           float64
		wantShort            bool
		wantAvailable, wantS float64
	}{
		{"covered", 10, 12, false, 0, 0},
		{"exactly covered", 10, 10, false, 0, 0},
		{"rounding noise is not short", 10.0004, 10, false, 0, 0},
		{"partly covered", 156.25, 100, true, 100, 56.25},
		{"nothing free", 312.5, 0, true, 0, 312.5},
		{"older orders over-promised: free shown as zero", 5, -20, true, 0, 5},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, short := shortfall(base, tc.need, tc.free)
			if short != tc.wantShort {
				t.Fatalf("short = %v, want %v", short, tc.wantShort)
			}
			if !short {
				return
			}
			if got.SKU != "GRN" || got.Requested != tc.need || got.Available != tc.wantAvailable || got.Short != tc.wantS {
				t.Errorf("shortage = %+v, want requested %v available %v short %v", got, tc.need, tc.wantAvailable, tc.wantS)
			}
		})
	}
}
