//go:build dbtest

package companylocation

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Inventory holds stock and bins at a company location (every warehouse_id is a
// foreign key to it), so deleting a location must be refused while it still
// does. These cover that guard.

// pkOf resolves a location's uuid to the numeric id inventory rows reference.
func pkOf(t *testing.T, pool *pgxpool.Pool, uuid string) int {
	t.Helper()
	var id int
	if err := pool.QueryRow(context.Background(),
		`SELECT company_location_id FROM company_location WHERE company_location_uuid = $1`, uuid).Scan(&id); err != nil {
		t.Fatalf("resolve location id: %v", err)
	}
	return id
}

// seedStock puts qty of a fresh quantity-tracked item at the location.
func seedStock(t *testing.T, pool *pgxpool.Pool, locationID int, qty float64) {
	t.Helper()
	ctx := context.Background()
	sku := fmt.Sprintf("DBTEST-LOC-%d", time.Now().UnixNano())
	var itemID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO inventory_item (inventory_item_sku, inventory_item_name,
			inventory_item_unit_id, inventory_item_tracking)
		SELECT $1, $1, u.unit_id, 'quantity' FROM lkp_unit u WHERE u.unit_code = 'EA'
		RETURNING inventory_item_id`, sku).Scan(&itemID); err != nil {
		t.Fatalf("seed item: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO inventory_stock (inventory_item_id, warehouse_id, quantity_on_hand)
		VALUES ($1, $2, $3)`, itemID, locationID, qty); err != nil {
		t.Fatalf("seed stock: %v", err)
	}
}

// seedBin adds a live bin at the location; system marks it as the auto-created
// staging bin an upgraded warehouse carries.
func seedBin(t *testing.T, pool *pgxpool.Pool, locationID int, system bool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO inventory_bin (warehouse_id, bin_code, bin_name, bin_type, bin_path, bin_is_system)
		VALUES ($1, 'BIN-A', 'Bin A', 'rack', 'BIN-A', $2)`, locationID, system); err != nil {
		t.Fatalf("seed bin: %v", err)
	}
}

// createYard makes a default "Main Office" first, so the returned second
// location ("Yard") is deletable as far as the default rule goes.
func createYard(t *testing.T, pool *pgxpool.Pool) *Location {
	t.Helper()
	ctx := context.Background()
	if _, err := Create(ctx, pool, Input{Name: "Main Office"}, 1); err != nil {
		t.Fatalf("first Create() error = %v", err)
	}
	yard, err := Create(ctx, pool, Input{Name: "Yard"}, 1)
	if err != nil {
		t.Fatalf("second Create() error = %v", err)
	}
	return yard
}

func TestDelete_LocationHoldingStock_BlockedUntilStockIsZero(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	yard := createYard(t, pool)
	seedStock(t, pool, pkOf(t, pool, yard.ID), 5)

	err := Delete(ctx, pool, yard.ID, 1)
	if !IsClientError(err) {
		t.Fatalf("Delete() error = %v, want a ClientError refusing a location that holds stock", err)
	}
	if _, err := Get(ctx, pool, yard.ID); err != nil {
		t.Errorf("Get() after blocked Delete error = %v, want nil (location must still exist)", err)
	}

	if _, err := pool.Exec(ctx, `UPDATE inventory_stock SET quantity_on_hand = 0 WHERE warehouse_id = $1`, pkOf(t, pool, yard.ID)); err != nil {
		t.Fatalf("zero the stock: %v", err)
	}
	if err := Delete(ctx, pool, yard.ID, 1); err != nil {
		t.Errorf("Delete() after the stock reached zero error = %v, want nil", err)
	}
}

func TestDelete_LocationWithUserBin_Blocked(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	yard := createYard(t, pool)
	seedBin(t, pool, pkOf(t, pool, yard.ID), false)

	if err := Delete(ctx, pool, yard.ID, 1); !IsClientError(err) {
		t.Fatalf("Delete() error = %v, want a ClientError refusing a location that still has bins", err)
	}
}

func TestDelete_LocationWithOnlySystemBin_Allowed(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	yard := createYard(t, pool)
	seedBin(t, pool, pkOf(t, pool, yard.ID), true)

	if err := Delete(ctx, pool, yard.ID, 1); err != nil {
		t.Errorf("Delete() error = %v, want nil: an upgraded warehouse's system staging bin must not pin the location", err)
	}
}
