//go:build dbtest

package salesorder

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/inventory"
)

// seedUnstockedItem inserts a stock-checked item with nothing on hand.
func seedUnstockedItem(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	var uuid string
	err := pool.QueryRow(context.Background(), `
		INSERT INTO inventory_item (inventory_item_sku, inventory_item_name, inventory_item_unit_id,
		                            inventory_item_unit_price, inventory_item_created_by)
		VALUES ($1, $2, 1, 10.00, 1) RETURNING inventory_item_uuid`,
		"NOSTOCK-"+suffix, "No Stock "+suffix).Scan(&uuid)
	if err != nil {
		t.Fatalf("seed item: %v", err)
	}
	return uuid
}

func installIn(days int) string {
	return time.Now().UTC().AddDate(0, 0, days).Format("2006-01-02")
}

func TestCreateInTx_DefersTheStockCheckOnlyWhenAsked(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID, _ := seedCustomerAndItem(t, pool)
	itemUUID := seedUnstockedItem(t, pool)

	in := CreateOrderInput{CustomerUUID: custUUID}
	in.InstallDate = installIn(5)
	in.Items = []LineInput2{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}}

	// The staff path still refuses an order for stock that is not there.
	_, err := Create(ctx, pool, in, 1)
	var shortage *inventory.StockShortageError
	if !errors.As(err, &shortage) {
		t.Fatalf("Create with no stock: err = %v, want a *StockShortageError", err)
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	created, err := CreateInTx(ctx, tx, in, 0, CreateOptions{DeferStockReservation: true})
	if err != nil {
		t.Fatalf("CreateInTx deferred: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("commit: %v", err)
	}

	var held int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM inventory_allocation WHERE sales_order_id = $1`, created.InternalID).Scan(&held); err != nil {
		t.Fatalf("count allocations: %v", err)
	}
	if held != 0 {
		t.Errorf("%d allocations held for a deferred order, want none", held)
	}
}

func TestCreateInTx_RollsBackWithTheCallersTransaction(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID, itemUUID := seedCustomerAndItem(t, pool)
	in := CreateOrderInput{CustomerUUID: custUUID}
	in.InstallDate = installIn(5)
	in.Items = []LineInput2{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}}

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	created, err := CreateInTx(ctx, tx, in, 0, CreateOptions{})
	if err != nil {
		t.Fatalf("CreateInTx: %v", err)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback: %v", err)
	}

	var n int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM sales_order WHERE sales_order_uuid = $1`, created.UUID).Scan(&n); err != nil {
		t.Fatalf("count: %v", err)
	}
	if n != 0 {
		t.Errorf("order survived the caller's rollback")
	}
}

func TestCreateInTx_ShippingAsGivenDoesNotInheritTheCustomersShipTo(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID, itemUUID := seedCustomerAndItem(t, pool)
	if _, err := pool.Exec(ctx, `
		UPDATE customer SET customer_ship_addr_suitenum = 'Suite 200', customer_ship_addr_line2 = 'Dock 4'
		WHERE customer_uuid = $1`, custUUID); err != nil {
		t.Fatalf("seed ship-to: %v", err)
	}
	build := func() CreateOrderInput {
		in := CreateOrderInput{CustomerUUID: custUUID}
		in.InstallDate = installIn(5)
		in.Items = []LineInput2{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}}
		in.Shipping = AddressInput{AddrLine1: "741 Bridle Path", City: "Aubrey"}
		return in
	}
	shipTo := func(opts CreateOptions) (suite, line2 string) {
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		created, err := CreateInTx(ctx, tx, build(), 1, opts)
		if err != nil {
			t.Fatalf("CreateInTx: %v", err)
		}
		if err := tx.QueryRow(ctx, `
			SELECT sales_order_ship_addr_suitenum, sales_order_ship_addr_line2
			FROM sales_order WHERE sales_order_id = $1`, created.InternalID).Scan(&suite, &line2); err != nil {
			t.Fatalf("read ship-to: %v", err)
		}
		return suite, line2
	}

	if suite, line2 := shipTo(CreateOptions{}); suite != "Suite 200" || line2 != "Dock 4" {
		t.Errorf("default: ship-to = (%q, %q), want the customer's suite and line 2 layered under", suite, line2)
	}
	if suite, line2 := shipTo(CreateOptions{ShippingAsGiven: true}); suite != "" || line2 != "" {
		t.Errorf("as given: ship-to = (%q, %q), want nothing inherited", suite, line2)
	}
}
