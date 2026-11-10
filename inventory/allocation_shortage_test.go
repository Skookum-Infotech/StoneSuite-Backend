package inventory

import "testing"

func TestShortfall(t *testing.T) {
	base := Shortage{ItemID: "i", SKU: "GRN", Name: "Granite", UnitCode: "SQFT"}
	cases := []struct {
		name                            string
		need, free, incoming            float64
		wantShort                       bool
		wantAvailable, wantOnOrd, wantS float64
	}{
		{"covered", 10, 12, 0, false, 0, 0, 0},
		{"exactly covered", 10, 10, 0, false, 0, 0, 0},
		{"rounding noise is not short", 10.0004, 10, 0, false, 0, 0, 0},
		{"partly covered", 156.25, 100, 0, true, 100, 0, 56.25},
		{"nothing free", 312.5, 0, 0, true, 0, 0, 312.5},
		{"older orders over-promised: free shown as zero", 5, -20, 0, true, 0, 0, 5},
		{"a sent PO covers the whole gap", 156.25, 0, 200, true, 0, 156.25, 0},
		{"a sent PO covers part of the gap", 312.5, 12.5, 100, true, 12.5, 100, 200},
		{"rounding noise left after a PO is not short", 100.0004, 0, 100, true, 0, 100.0004, 0},
		{"older orders took the incoming stock", 50, 0, -30, true, 0, 0, 50},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, short := shortfall(base, tc.need, tc.free, tc.incoming)
			if short != tc.wantShort {
				t.Fatalf("short = %v, want %v", short, tc.wantShort)
			}
			if !short {
				return
			}
			if got.SKU != "GRN" || got.Requested != roundTo(tc.need, areaScale) || got.Available != tc.wantAvailable ||
				got.OnOrder != roundTo(tc.wantOnOrd, areaScale) || got.Short != tc.wantS {
				t.Errorf("shortage = %+v, want requested %v available %v on order %v short %v",
					got, tc.need, tc.wantAvailable, tc.wantOnOrd, tc.wantS)
			}
		})
	}
}
