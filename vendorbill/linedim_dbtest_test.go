//go:build dbtest

package vendorbill

import (
	"context"
	"testing"
)

// sizePOLine stamps a slab size and count onto a seeded purchase order line.
func sizePOLine(t *testing.T, lineID int) {
	t.Helper()
	if _, err := testPool(t).Exec(context.Background(), `
		UPDATE purchase_order_item SET dim_length = 120, dim_width = 60, dim_unit = 'in', expected_slabs = 2
		WHERE purchase_order_item_id = $1`, lineID); err != nil {
		t.Fatalf("size purchase order line: %v", err)
	}
}

// A bill that bills the whole ordered quantity still IS length x width x slabs,
// so the line carries them over.
func TestConvertFromPurchaseOrder_FullBillCarriesTheSlabSize(t *testing.T) {
	pool := testPool(t)
	poUUID, lineIDs := seedPurchaseOrder(t, pool, "RCVD", []poLineSeed{{ordered: 100, received: 100, price: 25}})
	sizePOLine(t, lineIDs[0])

	bill, err := ConvertFromPurchaseOrder(context.Background(), pool, poUUID, 1)
	if err != nil {
		t.Fatalf("conversion: %v", err)
	}
	l := bill.Items[0]
	if l.Quantity != 100 {
		t.Fatalf("Quantity = %v, want 100", l.Quantity)
	}
	if l.Length == nil || *l.Length != 120 || l.Width == nil || *l.Width != 60 || l.Slabs == nil || *l.Slabs != 2 || l.DimensionUnit != "in" {
		t.Errorf("size = (%v, %v, %v) %q, want (120, 60, 2) in", l.Length, l.Width, l.Slabs, l.DimensionUnit)
	}
}

// A partial bill's quantity is no longer length x width x slabs, so it keeps the
// billed quantity and drops the size rather than print numbers that disagree.
func TestConvertFromPurchaseOrder_PartialBillDropsTheSlabSize(t *testing.T) {
	pool := testPool(t)
	poUUID, lineIDs := seedPurchaseOrder(t, pool, "PART", []poLineSeed{{ordered: 100, received: 40, price: 25}})
	sizePOLine(t, lineIDs[0])

	bill, err := ConvertFromPurchaseOrder(context.Background(), pool, poUUID, 1)
	if err != nil {
		t.Fatalf("conversion: %v", err)
	}
	l := bill.Items[0]
	if l.Quantity != 40 {
		t.Fatalf("Quantity = %v, want the 40 received", l.Quantity)
	}
	if l.Length != nil || l.Width != nil || l.Slabs != nil {
		t.Errorf("size = (%v, %v, %v), want none on a partial bill", l.Length, l.Width, l.Slabs)
	}
}
