// salesorder/stock_on_order_test.go
//go:build dbtest

package salesorder

import (
	"context"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// seedPurchaseOrder puts one line of qty of the item (received of it already
// received) on a new purchase order in the given status, and returns its number.
func seedPurchaseOrder(t *testing.T, pool *pgxpool.Pool, itemUUID, status string, qty, received float64) string {
	t.Helper()
	ctx := context.Background()
	suffix := uniqSuffix()
	var vendorID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO vendor (record_type, vendor_status, vendor_type, vendor_legal_name, vendor_created_by)
		SELECT rt.record_type_id, rs.record_status_id, 'Organization', $1, 1
		FROM lkp_record_type rt JOIN lkp_record_status rs ON rs.record_status_record_type = rt.record_type_id
		WHERE rt.record_type_code = 'VNDR' AND rs.record_status_code = 'ACT_'
		RETURNING vendor_id`, "On Order Vendor "+suffix).Scan(&vendorID); err != nil {
		t.Fatalf("seed vendor: %v", err)
	}
	var poID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO purchase_order (record_type, purchase_order_status, purchase_order_vendor_id, purchase_order_created_by)
		SELECT rt.record_type_id, rs.record_status_id, $2, 1
		FROM lkp_record_type rt JOIN lkp_record_status rs ON rs.record_status_record_type = rt.record_type_id
		WHERE rt.record_type_code = 'PORD' AND rs.record_status_code = $1
		RETURNING purchase_order_id`, status, vendorID).Scan(&poID); err != nil {
		t.Fatalf("seed %s purchase order: %v", status, err)
	}
	number := fmt.Sprintf("PORD-%06d", poID)
	if _, err := pool.Exec(ctx, `UPDATE purchase_order SET purchase_order_number = $1 WHERE purchase_order_id = $2`, number, poID); err != nil {
		t.Fatalf("number purchase order: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO purchase_order_item (purchase_order_id, line_number, inventory_item_id, item_name, quantity, qty_received, item_created_by)
		SELECT $1, 1, ii.inventory_item_id, ii.inventory_item_name, $3, $4, 1
		FROM inventory_item ii WHERE ii.inventory_item_uuid = $2`, poID, itemUUID, qty, received); err != nil {
		t.Fatalf("seed purchase order line: %v", err)
	}
	return number
}

func TestShortages_CountWhatSentPurchaseOrdersBring(t *testing.T) {
	pool := testPool(t)
	item := seedItem(t, pool, "quantity", true, 0)
	a := mustCreate(t, pool, seedCustomer(t, pool), lines(line{item, 10}))

	// A draft is not a promise yet.
	seedPurchaseOrder(t, pool, item, "DRFT", 10, 0)
	if short := shortagesOf(t, pool, a.ID); len(short) != 1 || short[0].OnOrder != 0 || short[0].Short != 10 {
		t.Fatalf("with only a draft PO: shortages = %+v, want on order 0 short 10", short)
	}

	// A sent order for 6, 2 of them already in: 2 in stock, 4 on the way, 4 uncovered.
	sent := seedPurchaseOrder(t, pool, item, "SENT", 6, 2)
	giveStock(t, pool, item, 2)
	short := shortagesOf(t, pool, a.ID)
	if len(short) != 1 {
		t.Fatalf("shortages = %+v, want one", short)
	}
	if s := short[0]; s.Requested != 10 || s.Available != 2 || s.OnOrder != 4 || s.Short != 4 ||
		len(s.PurchaseOrders) != 1 || s.PurchaseOrders[0] != sent {
		t.Errorf("shortage = %+v, want requested 10 available 2 on order 4 (%s) short 4", s, sent)
	}

	// A newer order gets nothing of it: the older order's gap takes it all first.
	b := mustCreate(t, pool, seedCustomer(t, pool), lines(line{item, 5}))
	if short := shortagesOf(t, pool, b.ID); len(short) != 1 || short[0].OnOrder != 0 || short[0].Short != 5 {
		t.Errorf("newer order shortages = %+v, want on order 0 short 5", short)
	}

	// Once enough is on its way, nothing is left uncovered for the first order.
	seedPurchaseOrder(t, pool, item, "PART", 10, 0)
	if short := shortagesOf(t, pool, a.ID); len(short) != 1 || short[0].OnOrder != 8 || short[0].Short != 0 || len(short[0].PurchaseOrders) != 2 {
		t.Errorf("fully ordered shortages = %+v, want on order 8 short 0 from both POs", short)
	}
	// ...and the newer order now draws on what is left over: 4 + 10 - 8 = 6 ≥ 5.
	if short := shortagesOf(t, pool, b.ID); len(short) != 1 || short[0].OnOrder != 5 || short[0].Short != 0 {
		t.Errorf("newer order shortages = %+v, want on order 5 short 0", short)
	}
}
