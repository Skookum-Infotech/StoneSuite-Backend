// inventory/unit_so_hold_dbtest_test.go — a slab can be held for a sales order from
// its Material drawing: listed, held, released, and given back when the order goes.
//go:build dbtest

package inventory

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// seedHoldOrder makes a throwaway sales order in the given status with one line for
// the item (by uuid). It is removed afterwards. Returns the order's uuid and id.
func seedHoldOrder(t *testing.T, pool *pgxpool.Pool, itemUUID, statusCode string) (string, int) {
	t.Helper()
	ctx := context.Background()
	var customerID, orderID int
	var orderUUID string
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO customer (record_type, customer_name)
		VALUES ((SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'CUST'), $1)
		RETURNING customer_id`, uniq("DBTEST-HOLD-CUST")).Scan(&customerID))
	require.NoError(t, pool.QueryRow(ctx, `
		INSERT INTO sales_order (record_type, sales_order_status, sales_order_customer_id, sales_order_number)
		VALUES ((SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'SORD'),
		        (SELECT rs.record_status_id FROM lkp_record_status rs
		         JOIN lkp_record_type rt ON rt.record_type_id = rs.record_status_record_type
		         WHERE rt.record_type_code = 'SORD' AND rs.record_status_code = $3),
		        $1, $2)
		RETURNING sales_order_id, sales_order_uuid`, customerID, shortOrderNumber(), statusCode).Scan(&orderID, &orderUUID))
	_, err := pool.Exec(ctx, `
		INSERT INTO sales_order_item (sales_order_id, line_number, discount_percent, unit_price, inventory_item_id)
		SELECT $1, 1, 0, 10, inventory_item_id FROM inventory_item WHERE inventory_item_uuid = $2`, orderID, itemUUID)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = pool.Exec(ctx, `UPDATE inventory_slab SET reserved_sales_order_id = NULL WHERE reserved_sales_order_id = $1`, orderID)
		_, _ = pool.Exec(ctx, `DELETE FROM sales_order_item WHERE sales_order_id = $1`, orderID)
		_, _ = pool.Exec(ctx, `DELETE FROM sales_order WHERE sales_order_id = $1`, orderID)
		_, _ = pool.Exec(ctx, `DELETE FROM customer WHERE customer_id = $1`, customerID)
	})
	return orderUUID, orderID
}

// unitsOf flattens the listing to the uuids it offers for the one item.
func unitsOf(groups []OrderSlabGroup) []string {
	var ids []string
	for _, g := range groups {
		for _, u := range g.Units {
			ids = append(ids, u.ID)
		}
	}
	return ids
}

func TestHoldUnitForSalesOrder_HoldsListsAndReleases(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	u := newReservableUnit(t)
	orderUUID, _ := seedHoldOrder(t, pool, u.InventoryItemID, "DRFT")
	before := onHand(t, pool, u.InventoryItemID)

	groups, err := ListUnitsForSalesOrder(ctx, pool, orderUUID)
	require.NoError(t, err)
	require.Len(t, groups, 1, "the order's one slab-tracked item")
	assert.Equal(t, u.InventoryItemID, groups[0].ItemID)
	assert.Contains(t, unitsOf(groups), u.ID, "a free slab of the item is offered")

	require.NoError(t, HoldUnitForSalesOrder(ctx, pool, orderUUID, u.ID, 1))
	got, err := GetUnit(ctx, pool, u.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusReserved, got.Status)
	require.NotNil(t, got.ReservedSalesOrderID)
	assert.Equal(t, orderUUID, *got.ReservedSalesOrderID)
	assert.NotEmpty(t, got.ReservedSalesOrderNumber)
	assert.Nil(t, got.ReservedCustomerID, "an order hold is not a customer hold")
	assert.InDelta(t, before, onHand(t, pool, u.InventoryItemID), 1e-9, "a held slab is still on hand")

	groups, err = ListUnitsForSalesOrder(ctx, pool, orderUUID)
	require.NoError(t, err)
	assert.Contains(t, unitsOf(groups), u.ID, "the order still lists the slab it holds")

	// Holding again is a no-op, not an error: a double-clicked Add is harmless.
	require.NoError(t, HoldUnitForSalesOrder(ctx, pool, orderUUID, u.ID, 1))

	require.NoError(t, ReleaseUnitFromSalesOrder(ctx, pool, orderUUID, u.ID, 1))
	got, err = GetUnit(ctx, pool, u.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusAvailable, got.Status)
	assert.Nil(t, got.ReservedSalesOrderID)

	// Releasing a slab that is already free is quietly fine.
	require.NoError(t, ReleaseUnitFromSalesOrder(ctx, pool, orderUUID, u.ID, 1))
}

func TestHoldUnitForSalesOrder_OtherOrdersHoldIsOffLimits(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	u := newReservableUnit(t)
	first, _ := seedHoldOrder(t, pool, u.InventoryItemID, "DRFT")
	second, _ := seedHoldOrder(t, pool, u.InventoryItemID, "DRFT")

	require.NoError(t, HoldUnitForSalesOrder(ctx, pool, first, u.ID, 1))

	err := HoldUnitForSalesOrder(ctx, pool, second, u.ID, 1)
	require.Error(t, err)
	assert.True(t, IsClientError(err))
	assert.Contains(t, err.Error(), "already reserved")

	groups, err := ListUnitsForSalesOrder(ctx, pool, second)
	require.NoError(t, err)
	assert.NotContains(t, unitsOf(groups), u.ID, "another order's slab is not offered")

	err = ReleaseUnitFromSalesOrder(ctx, pool, second, u.ID, 1)
	require.Error(t, err, "an order cannot release a slab another order holds")
	assert.True(t, IsClientError(err))
}

func TestHoldUnitForSalesOrder_Refusals(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	u := newReservableUnit(t)

	// A slab of a material the order has no line for.
	otherItem := seedAreaItem(t, pool, uniq("DBTEST-HOLD-OTHER"))
	wrongMaterial, _ := seedHoldOrder(t, pool, otherItem, "DRFT")
	err := HoldUnitForSalesOrder(ctx, pool, wrongMaterial, u.ID, 1)
	require.Error(t, err)
	assert.True(t, IsClientError(err))
	assert.Contains(t, err.Error(), "different material")

	// A cancelled or filled order takes no new holds.
	for _, status := range []string{"CANC", "FILL"} {
		closed, _ := seedHoldOrder(t, pool, u.InventoryItemID, status)
		err := HoldUnitForSalesOrder(ctx, pool, closed, u.ID, 1)
		require.Error(t, err, status)
		assert.True(t, IsClientError(err), status)
	}

	// A slab not in a bin yet, or not inspected, cannot be held.
	open, _ := seedHoldOrder(t, pool, u.InventoryItemID, "DRFT")
	_, err = pool.Exec(ctx, `UPDATE inventory_slab SET inspection_status = 'pending' WHERE inventory_slab_uuid = $1`, u.ID)
	require.NoError(t, err)
	// Still listed, so stock in the yard does not seem to be missing, but not holdable.
	groups, err := ListUnitsForSalesOrder(ctx, pool, open)
	require.NoError(t, err)
	assert.Contains(t, unitsOf(groups), u.ID, "a slab awaiting inspection is listed")
	err = HoldUnitForSalesOrder(ctx, pool, open, u.ID, 1)
	require.Error(t, err)
	assert.True(t, IsClientError(err))

	got, err := GetUnit(ctx, pool, u.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusAvailable, got.Status, "a refused hold changes nothing")
}

func TestReleaseSalesOrderHoldsTx_GivesEverySlabBack(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	u := newReservableUnit(t)
	orderUUID, orderID := seedHoldOrder(t, pool, u.InventoryItemID, "DRFT")
	require.NoError(t, HoldUnitForSalesOrder(ctx, pool, orderUUID, u.ID, 1))

	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	require.NoError(t, ReleaseSalesOrderHoldsTx(ctx, tx, orderID, 1))
	require.NoError(t, tx.Commit(ctx))

	got, err := GetUnit(ctx, pool, u.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusAvailable, got.Status)
	assert.Nil(t, got.ReservedSalesOrderID)
}

func TestReleaseUnitReservation_AlsoFreesAnOrderHold(t *testing.T) {
	ctx := context.Background()
	pool := testPool(t)
	u := newReservableUnit(t)
	orderUUID, _ := seedHoldOrder(t, pool, u.InventoryItemID, "DRFT")
	require.NoError(t, HoldUnitForSalesOrder(ctx, pool, orderUUID, u.ID, 1))

	// Inventory staff are not stuck with a hold from a stalled order.
	require.NoError(t, ReleaseUnitReservation(ctx, pool, u.ID, "order stalled", 1))
	got, err := GetUnit(ctx, pool, u.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusAvailable, got.Status)
	assert.Nil(t, got.ReservedSalesOrderID)
}
