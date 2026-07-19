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
	in := salesorder.CreateOrderInput{CustomerUUID: custUUID}
	in.SalesTaxPercent = 8
	in.Items = []salesorder.LineInput2{
		{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 2},
	}
	order, err := salesorder.Create(context.Background(), pool, in, 1)
	if err != nil {
		t.Fatalf("seed sales order: %v", err)
	}
	return order
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
