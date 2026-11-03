//go:build dbtest

package inventory

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// seedStockItem creates a quantity item that the test removes again, since the
// stock read model is exercised against the shared dev database.
func seedStockItem(t *testing.T, pool *pgxpool.Pool) (itemID int, itemUUID string) {
	t.Helper()
	ctx := context.Background()
	sku := uniq("DBTEST-STOCK")
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO inventory_item (inventory_item_sku, inventory_item_name, inventory_item_unit_id, inventory_item_tracking)
		SELECT $1, $1, u.unit_id, 'quantity' FROM lkp_unit u WHERE u.unit_code = 'EA'
		RETURNING inventory_item_id, inventory_item_uuid`, sku).Scan(&itemID, &itemUUID))
	t.Cleanup(func() {
		// inventory_stock and inventory_allocation cascade from the item and its orders.
		_, _ = pool.Exec(ctx, `DELETE FROM inventory_item WHERE inventory_item_id = $1`, itemID)
	})
	return itemID, itemUUID
}

// seedSecondLocation adds a company location other than the test's id 1.
func seedSecondLocation(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	ctx := context.Background()
	var id int
	require.NoError(t, pool.QueryRow(ctx,
		`INSERT INTO company_location (name) VALUES ($1) RETURNING company_location_id`, uniq("DBTEST-LOC")).Scan(&id))
	t.Cleanup(func() { _, _ = pool.Exec(ctx, `DELETE FROM company_location WHERE company_location_id = $1`, id) })
	return id
}

func seedStockRow(t *testing.T, pool *pgxpool.Pool, itemID, warehouseID int, onHand, reorder float64) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO inventory_stock (inventory_item_id, warehouse_id, quantity_on_hand, reorder_point)
		VALUES ($1, $2, $3, $4)`, itemID, warehouseID, onHand, reorder)
	require.NoError(t, err)
}

// shortOrderNumber is unique per call and fits sales_order_number (VARCHAR(20)),
// which uniq()'s nanosecond suffix does not.
func shortOrderNumber() string {
	return fmt.Sprintf("DBT-%d", time.Now().UnixNano()%1_000_000_000_000)
}

// seedAllocation holds qty of the item against a throwaway sales order, with
// the given allocation status. The order and customer are removed afterwards.
func seedAllocation(t *testing.T, pool *pgxpool.Pool, itemID, warehouseID int, qty float64, status string) {
	t.Helper()
	ctx := context.Background()
	var customerID, orderID, lineID int
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO customer (record_type, customer_name)
		VALUES ((SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'CUST'), $1)
		RETURNING customer_id`, uniq("DBTEST-CUST")).Scan(&customerID))
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO sales_order (record_type, sales_order_status, sales_order_customer_id, sales_order_number)
		VALUES ((SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'SORD'),
		        (SELECT rs.record_status_id FROM lkp_record_status rs
		         JOIN lkp_record_type rt ON rt.record_type_id = rs.record_status_record_type
		         WHERE rt.record_type_code = 'SORD' AND rs.record_status_code = 'DRFT'),
		        $1, $2)
		RETURNING sales_order_id`, customerID, shortOrderNumber()).Scan(&orderID))
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO sales_order_item (sales_order_id, line_number, discount_percent, unit_price, inventory_item_id)
		VALUES ($1, 1, 0, 10, $2) RETURNING sales_order_item_id`, orderID, itemID).Scan(&lineID))
	fulfilled := 0.0
	if status == "fulfilled" {
		fulfilled = qty
	}
	_, err := pool.Exec(ctx, `
		INSERT INTO inventory_allocation (inventory_item_id, warehouse_id, sales_order_id, sales_order_item_id,
		                                  allocated_quantity, fulfilled_quantity, allocation_status)
		VALUES ($1, $2, $3, $4, $5, $6, $7)`, itemID, warehouseID, orderID, lineID, qty, fulfilled, status)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `DELETE FROM sales_order WHERE sales_order_id = $1`, orderID)
		_, _ = pool.Exec(ctx, `DELETE FROM customer WHERE customer_id = $1`, customerID)
	})
}

func TestStockForItem_PerLocation(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	itemID, itemUUID := seedStockItem(t, pool)
	second := seedSecondLocation(t, pool)
	seedStockRow(t, pool, itemID, 1, 4, 0)
	seedStockRow(t, pool, itemID, second, 0, 5)
	seedAllocation(t, pool, itemID, 1, 10, "reserved")

	rows, err := StockForItem(ctx, pool, itemUUID)

	require.NoError(t, err)
	require.Len(t, rows, 2)
	byWarehouse := map[int]StockRow{}
	for _, r := range rows {
		byWarehouse[r.WarehouseID] = r
	}
	require.Equal(t, StockRow{
		ItemID: itemUUID, SKU: byWarehouse[1].SKU, ItemName: byWarehouse[1].ItemName, Tracking: "quantity", UnitCode: "EA",
		WarehouseID: 1, WarehouseName: byWarehouse[1].WarehouseName, OnHand: 4, Allocated: 10, Available: 0, ReorderPoint: 0,
	}, byWarehouse[1], "available is floored at zero when more is held than is on hand")
	require.Equal(t, 0.0, byWarehouse[second].OnHand)
	require.Equal(t, 5.0, byWarehouse[second].ReorderPoint, "a location with nothing on hand still shows its reorder point")
}

func TestStockForItem_NotHeldAnywhere(t *testing.T) {
	pool := testPool(t)
	_, itemUUID := seedStockItem(t, pool)

	rows, err := StockForItem(context.Background(), pool, itemUUID)

	require.NoError(t, err)
	require.Empty(t, rows)
}

func TestStockForItem_UnknownItem(t *testing.T) {
	pool := testPool(t)
	for _, id := range []string{"00000000-0000-0000-0000-000000000000", "not-a-uuid", ""} {
		_, err := StockForItem(context.Background(), pool, id)
		require.ErrorIs(t, err, ErrNotFound, id)
	}
}

func TestStockTotals_SumsLocationsAndFlagsBelowReorder(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	aID, aUUID := seedStockItem(t, pool)
	_, bUUID := seedStockItem(t, pool)
	second := seedSecondLocation(t, pool)
	seedStockRow(t, pool, aID, 1, 3, 5)
	seedStockRow(t, pool, aID, second, 1, 0)

	totals, err := StockTotals(ctx, pool, []string{aUUID, bUUID})

	require.NoError(t, err)
	require.Equal(t, StockTotal{UnitCode: "EA", OnHand: 4, Allocated: 0, Available: 4, Locations: 2, BelowReorder: true}, totals[aUUID])
	require.Contains(t, totals, bUUID, "an item held nowhere is still listed, so a new item shows 0")
	require.Equal(t, StockTotal{UnitCode: "EA"}, totals[bUUID])
}

func TestStockTotals_NotBelowReorderWhenNoneSet(t *testing.T) {
	pool := testPool(t)
	itemID, itemUUID := seedStockItem(t, pool)
	seedStockRow(t, pool, itemID, 1, 0, 0)

	totals, err := StockTotals(context.Background(), pool, []string{itemUUID})

	require.NoError(t, err)
	require.False(t, totals[itemUUID].BelowReorder)
}

func TestStockTotals_SubtractsOnlyOpenAllocations(t *testing.T) {
	pool := testPool(t)
	itemID, itemUUID := seedStockItem(t, pool)
	seedStockRow(t, pool, itemID, 1, 10, 0)
	seedAllocation(t, pool, itemID, 1, 3, "reserved")
	seedAllocation(t, pool, itemID, 1, 2, "fulfilled")
	seedAllocation(t, pool, itemID, 1, 4, "released")

	totals, err := StockTotals(context.Background(), pool, []string{itemUUID})

	require.NoError(t, err)
	require.Equal(t, 3.0, totals[itemUUID].Allocated, "a fulfilled or released allocation no longer holds stock")
	require.Equal(t, 7.0, totals[itemUUID].Available)
}

func TestStockTotals_IgnoresUnknownAndMalformedIDs(t *testing.T) {
	pool := testPool(t)

	totals, err := StockTotals(context.Background(), pool, []string{"not-a-uuid", "00000000-0000-0000-0000-000000000000"})

	require.NoError(t, err)
	require.Empty(t, totals)
}

func TestSetReorderPoint_CreatesRowWhenNoneHeld(t *testing.T) {
	pool := testPool(t)
	_, itemUUID := seedStockItem(t, pool)

	row, err := SetReorderPoint(context.Background(), pool, itemUUID, 1, 5)

	require.NoError(t, err)
	require.Equal(t, 5.0, row.ReorderPoint)
	require.Equal(t, 0.0, row.OnHand)
	rows, err := StockForItem(context.Background(), pool, itemUUID)
	require.NoError(t, err)
	require.Len(t, rows, 1)
}

func TestSetReorderPoint_UpdatesWithoutTouchingOnHand(t *testing.T) {
	pool := testPool(t)
	itemID, itemUUID := seedStockItem(t, pool)
	seedStockRow(t, pool, itemID, 1, 7, 2)

	row, err := SetReorderPoint(context.Background(), pool, itemUUID, 1, 9)

	require.NoError(t, err)
	require.Equal(t, 7.0, row.OnHand)
	require.Equal(t, 9.0, row.ReorderPoint)
}

func TestSetReorderPoint_Rejects(t *testing.T) {
	pool := testPool(t)
	_, itemUUID := seedStockItem(t, pool)

	for name, tc := range map[string]struct {
		item        string
		warehouse   int
		value       float64
		wantMissing bool
	}{
		"negative":         {itemUUID, 1, -1, false},
		"unknown location": {itemUUID, 999999, 5, false},
		"unknown item":     {"00000000-0000-0000-0000-000000000000", 1, 5, true},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := SetReorderPoint(context.Background(), pool, tc.item, tc.warehouse, tc.value)
			require.Error(t, err)
			if tc.wantMissing {
				require.ErrorIs(t, err, ErrNotFound)
			} else {
				require.True(t, IsClientError(err), "%v", err)
			}
		})
	}
	rows, err := StockForItem(context.Background(), pool, itemUUID)
	require.NoError(t, err)
	require.Empty(t, rows, "a refused write leaves no stock row behind")
}
