// invoice/store_convert_test.go
//go:build dbtest

package invoice

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/salesorder"
)

// seedSalesOrderWithLine creates a live sales order with one catalog-priced
// line, for convert tests. orderFields is unexported in the salesorder
// package, but its promoted Items/SalesTaxPercent fields are still settable
// on the embedding CreateOrderInput from outside the package.
func seedSalesOrderWithLine(t *testing.T, pool *pgxpool.Pool, custUUID, itemUUID string) *salesorder.Order {
	t.Helper()
	// An order for a catalogue item is checked against stock, so give it some.
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO inventory_stock (inventory_item_id, warehouse_id, quantity_on_hand)
		SELECT inventory_item_id, 1, 1000 FROM inventory_item WHERE inventory_item_uuid = $1
		ON CONFLICT (inventory_item_id, warehouse_id) DO UPDATE SET quantity_on_hand = 1000`, itemUUID); err != nil {
		t.Fatalf("give item stock: %v", err)
	}
	in := salesorder.CreateOrderInput{CustomerUUID: custUUID}
	in.SalesTaxPercent = 8
	in.Items = []salesorder.LineInput2{
		{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 2},
	}
	order, err := salesorder.Create(context.Background(), pool, in, 1)
	if err != nil {
		t.Fatalf("seed sales order: %v", err)
	}
	// Conversion requires a confirmed order; set the status directly, as the
	// approval flow itself is not under test here.
	setSalesOrderStatus(t, pool, order.ID, "OPEN")
	return order
}

// setSalesOrderStatus forces a sales order into the given status code.
func setSalesOrderStatus(t *testing.T, pool *pgxpool.Pool, soUUID, code string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		UPDATE sales_order SET sales_order_status = (
			SELECT record_status_id FROM lkp_record_status
			WHERE record_status_record_type = sales_order.record_type AND record_status_code = $2)
		WHERE sales_order_uuid = $1`, soUUID, code); err != nil {
		t.Fatalf("set sales order status %s: %v", code, err)
	}
}

// An unconfirmed (or cancelled) order can't be billed; a repeat conversion of an
// already-invoiced order still replays the existing invoice.
func TestConvertFromSalesOrder_RequiresConfirmedOrder(t *testing.T) {
	pool := testPool(t)
	custUUID, itemUUID := seedCustomerAndItem(t, pool)
	so := seedSalesOrderWithLine(t, pool, custUUID, itemUUID)

	for _, code := range []string{"DRFT", "PAPV", "CANC"} {
		setSalesOrderStatus(t, pool, so.ID, code)
		_, _, err := ConvertFromSalesOrder(context.Background(), pool, so.ID, 1)
		var ce ClientError
		if !errors.As(err, &ce) {
			t.Fatalf("status %s: expected ClientError, got %T: %v", code, err, err)
		}
	}

	setSalesOrderStatus(t, pool, so.ID, "OPEN")
	first, created, err := ConvertFromSalesOrder(context.Background(), pool, so.ID, 1)
	if err != nil || !created {
		t.Fatalf("convert OPEN order: created=%v err=%v", created, err)
	}
	setSalesOrderStatus(t, pool, so.ID, "CANC")
	again, created, err := ConvertFromSalesOrder(context.Background(), pool, so.ID, 1)
	if err != nil || created || again.ID != first.ID {
		t.Fatalf("replay after cancel: created=%v err=%v id=%v want %v", created, err, again, first.ID)
	}
}

func TestConvertFromSalesOrder_CopiesLinesAndTotals(t *testing.T) {
	pool := testPool(t)
	custUUID, itemUUID := seedCustomerAndItem(t, pool)
	so := seedSalesOrderWithLine(t, pool, custUUID, itemUUID)

	got, created, err := ConvertFromSalesOrder(context.Background(), pool, so.ID, 1)
	if err != nil {
		t.Fatalf("ConvertFromSalesOrder: %v", err)
	}
	if !created {
		t.Errorf("created = false on first conversion, want true")
	}
	if got.SalesOrder == nil || got.SalesOrder.ID != so.ID {
		t.Errorf("SalesOrder ref = %v, want %v", got.SalesOrder, so.ID)
	}
	if len(got.Items) != 1 {
		t.Fatalf("len(Items) = %d, want 1", len(got.Items))
	}
	// invoice's own seedCustomerAndItem (unlike quote/salesorder's) seeds the
	// catalog item at unit price 42.00, not 25.00 — assert against the
	// sales order's own snapshot rather than a hardcoded literal so this
	// doesn't silently drift from whichever package's helper is in scope.
	if got.Items[0].UnitPrice != so.Items[0].UnitPrice {
		t.Errorf("Items[0].UnitPrice = %v, want %v (sales order's line price)", got.Items[0].UnitPrice, so.Items[0].UnitPrice)
	}
	if got.GrandTotal != so.GrandTotal {
		t.Errorf("GrandTotal = %v, want %v (sales order's total)", got.GrandTotal, so.GrandTotal)
	}
	if got.BalanceDue != got.GrandTotal {
		t.Errorf("BalanceDue = %v, want %v (unpaid on creation)", got.BalanceDue, got.GrandTotal)
	}
	if got.StatusCode != "DRFT" {
		t.Errorf("StatusCode = %q, want DRFT", got.StatusCode)
	}
}

func TestConvertFromSalesOrder_IsIdempotent(t *testing.T) {
	pool := testPool(t)
	custUUID, itemUUID := seedCustomerAndItem(t, pool)
	so := seedSalesOrderWithLine(t, pool, custUUID, itemUUID)

	first, created1, err := ConvertFromSalesOrder(context.Background(), pool, so.ID, 1)
	if err != nil {
		t.Fatalf("first ConvertFromSalesOrder: %v", err)
	}
	if !created1 {
		t.Fatalf("created1 = false, want true")
	}

	second, created2, err := ConvertFromSalesOrder(context.Background(), pool, so.ID, 1)
	if err != nil {
		t.Fatalf("second ConvertFromSalesOrder: %v", err)
	}
	if created2 {
		t.Errorf("created2 = true, want false (replay should not create a duplicate)")
	}
	if second.ID != first.ID {
		t.Errorf("second.ID = %q, want %q (same invoice)", second.ID, first.ID)
	}
}

func TestConvertFromSalesOrder_UnknownSalesOrder(t *testing.T) {
	pool := testPool(t)
	_, _, err := ConvertFromSalesOrder(context.Background(), pool, "00000000-0000-0000-0000-000000000000", 1)
	if !errors.Is(err, ErrSalesOrderNotFound) {
		t.Fatalf("ConvertFromSalesOrder with unknown uuid: err = %v, want ErrSalesOrderNotFound", err)
	}
}
