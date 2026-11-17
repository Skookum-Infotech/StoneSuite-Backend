package fabrication

import "testing"

func TestSlabFreeForJob(t *testing.T) {
	const orderID, otherOrderID = 7, 8
	order := func(id int) *int { return &id }
	tests := []struct {
		name         string
		status       string
		heldForOrder *int
		want         bool
	}{
		{name: "available slab is free", status: "available", want: true},
		{name: "held for this job's own order is free", status: "reserved", heldForOrder: order(orderID), want: true},
		{name: "held for a different order is not", status: "reserved", heldForOrder: order(otherOrderID), want: false},
		{name: "reserved with no order hold is somebody else's", status: "reserved", want: false},
		{name: "consumed is not", status: "consumed", want: false},
		{name: "scrapped is not", status: "scrapped", want: false},
		{name: "on order is not", status: "on_order", want: false},
		{name: "a stale order hold on a non-reserved slab is ignored", status: "available", heldForOrder: order(otherOrderID), want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := slabFreeForJob(tc.status, tc.heldForOrder, orderID); got != tc.want {
				t.Fatalf("slabFreeForJob(%q, %v, %d) = %v, want %v", tc.status, tc.heldForOrder, orderID, got, tc.want)
			}
		})
	}
}
