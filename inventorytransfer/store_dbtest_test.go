// inventorytransfer/store_dbtest_test.go
//go:build dbtest

package inventorytransfer

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/inventory"
)

func uniq(prefix string) string { return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano()) }

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB-backed test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// secondWarehouse returns a destination warehouse, creating it once.
func secondWarehouse(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	ctx := context.Background()
	var id int
	err := pool.QueryRow(ctx, `
		INSERT INTO lkp_warehouse (warehouse_code, warehouse_name, warehouse_created_by)
		VALUES ('XFER-DEST','Transfer Destination', 1)
		ON CONFLICT (warehouse_code) DO UPDATE SET warehouse_name = EXCLUDED.warehouse_name
		RETURNING warehouse_id`).Scan(&id)
	if err != nil {
		t.Fatalf("seed destination warehouse: %v", err)
	}
	return id
}

func seedBulkItem(t *testing.T, pool *pgxpool.Pool, warehouseID int, qty float64) (uuid string) {
	t.Helper()
	ctx := context.Background()
	sku := uniq("DBTEST-XFER-B")
	var itemID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO inventory_item (inventory_item_sku, inventory_item_name,
			inventory_item_unit_id, inventory_item_tracking)
		SELECT $1, $1, u.unit_id, 'quantity' FROM lkp_unit u WHERE u.unit_code = 'EA'
		RETURNING inventory_item_uuid, inventory_item_id`, sku).Scan(&uuid, &itemID); err != nil {
		t.Fatalf("seed bulk item: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO inventory_stock (inventory_item_id, warehouse_id, quantity_on_hand)
		VALUES ($1,$2,$3)`, itemID, warehouseID, qty); err != nil {
		t.Fatalf("seed stock: %v", err)
	}
	return uuid
}

func seedSerialized(t *testing.T, pool *pgxpool.Pool) (itemUUID string, unit *inventory.Unit) {
	t.Helper()
	ctx := context.Background()
	sku := uniq("DBTEST-XFER-S")
	if err := pool.QueryRow(ctx, `
		INSERT INTO inventory_item (inventory_item_sku, inventory_item_name,
			inventory_item_unit_id, inventory_item_tracking)
		SELECT $1, $1, u.unit_id, 'serialized' FROM lkp_unit u WHERE u.unit_code = 'SQFT'
		RETURNING inventory_item_uuid`, sku).Scan(&itemUUID); err != nil {
		t.Fatalf("seed serialized item: %v", err)
	}
	u, err := inventory.CreateUnit(ctx, pool, inventory.CreateUnitInput{
		Serial: uniq("DBTEST-XU"), InventoryItemUUID: itemUUID, WarehouseID: 1,
		LengthMM: 3000, WidthMM: 1400, ThicknessMM: 30,
	}, 1)
	if err != nil {
		t.Fatalf("CreateUnit: %v", err)
	}
	return itemUUID, u
}

// stockAt reads on-hand for one item at one warehouse.
func stockAt(t *testing.T, pool *pgxpool.Pool, itemUUID string, warehouseID int) float64 {
	t.Helper()
	var v float64
	if err := pool.QueryRow(context.Background(), `
		SELECT COALESCE(SUM(s.quantity_on_hand),0) FROM inventory_stock s
		JOIN inventory_item ii ON ii.inventory_item_id = s.inventory_item_id
		WHERE ii.inventory_item_uuid = $1 AND s.warehouse_id = $2`,
		itemUUID, warehouseID).Scan(&v); err != nil {
		t.Fatalf("read stock: %v", err)
	}
	return v
}

func approve(t *testing.T, pool *pgxpool.Pool, uuid string) {
	t.Helper()
	ctx := context.Background()
	if _, err := Transition(ctx, pool, uuid, StatusPending, "", 1); err != nil {
		t.Fatalf("to pending: %v", err)
	}
	if _, err := Transition(ctx, pool, uuid, StatusApproved, "", 1); err != nil {
		t.Fatalf("to approved: %v", err)
	}
}

func TestTransferBulk_ShipDeductsReceiveAdds(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	dest := secondWarehouse(t, pool)
	item := seedBulkItem(t, pool, 1, 40)

	tr, err := Create(ctx, pool, Input{
		FromWarehouseID: 1, ToWarehouseID: dest,
		Lines: []LineInput{{InventoryItemID: item, Qty: 15}},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if tr.Number == "" {
		t.Error("document number was not assigned")
	}
	approve(t, pool, tr.ID)

	// Ship: gone from the source, not yet anywhere else. This is the moment
	// inventory_stock deliberately understates the total.
	if _, err := Ship(ctx, pool, tr.ID, 1); err != nil {
		t.Fatalf("Ship: %v", err)
	}
	if got := stockAt(t, pool, item, 1); got != 25 {
		t.Errorf("source after ship = %v, want 25", got)
	}
	if got := stockAt(t, pool, item, dest); got != 0 {
		t.Errorf("destination after ship = %v, want 0 — it is still on the truck", got)
	}

	if _, err := Receive(ctx, pool, tr.ID, 1); err != nil {
		t.Fatalf("Receive: %v", err)
	}
	if got := stockAt(t, pool, item, 1); got != 25 {
		t.Errorf("source after receive = %v, want 25", got)
	}
	if got := stockAt(t, pool, item, dest); got != 15 {
		t.Errorf("destination after receive = %v, want 15", got)
	}

	// Both legs are once-only. Re-receiving must not add the stock twice — the
	// key carries warehouse_id precisely so the arrival leg does not collide
	// with the departure leg, and this asserts it did not go the other way.
	if _, err := Receive(ctx, pool, tr.ID, 1); err == nil {
		t.Error("expected a second receive to be refused")
	}
	if got := stockAt(t, pool, item, dest); got != 15 {
		t.Errorf("a refused second receive changed stock: %v", got)
	}
}

func TestTransferSerialized_MovesTheSlabAndClearsItsBin(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	dest := secondWarehouse(t, pool)
	item, unit := seedSerialized(t, pool)

	// Put it in a bin at the source, so we can prove the bin is cleared.
	var sourceBin string
	if err := pool.QueryRow(ctx, `
		INSERT INTO inventory_bin (warehouse_id, bin_code, bin_name, bin_type, bin_path)
		VALUES (1, $1, $1, 'rack', $1) RETURNING inventory_bin_uuid`, uniq("XB")).Scan(&sourceBin); err != nil {
		t.Fatalf("seed source bin: %v", err)
	}
	if err := inventory.MoveUnitToBin(ctx, pool, unit.ID, inventory.MoveUnitInput{BinUUID: &sourceBin}, 1); err != nil {
		t.Fatalf("MoveUnitToBin: %v", err)
	}

	tr, err := Create(ctx, pool, Input{
		FromWarehouseID: 1, ToWarehouseID: dest,
		Lines: []LineInput{{InventoryItemID: item, InventoryUnitID: &unit.ID}},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// The quantity is the slab's own area, never the caller's.
	if tr.Lines[0].Qty != unit.Area {
		t.Errorf("line qty = %v, want the slab's area %v", tr.Lines[0].Qty, unit.Area)
	}
	approve(t, pool, tr.ID)

	if _, err := Ship(ctx, pool, tr.ID, 1); err != nil {
		t.Fatalf("Ship: %v", err)
	}
	shipped, err := inventory.GetUnit(ctx, pool, unit.ID)
	if err != nil {
		t.Fatalf("GetUnit: %v", err)
	}
	if shipped.Status != inventory.StatusInTransit {
		t.Errorf("status after ship = %q, want in_transit", shipped.Status)
	}
	// Its bin is in the warehouse it left; keeping it would show the slab as
	// still occupying that rack.
	if shipped.BinID != nil {
		t.Errorf("shipped unit still points at bin %v", shipped.BinID)
	}
	if got := stockAt(t, pool, item, 1); got != 0 {
		t.Errorf("source after ship = %v, want 0", got)
	}

	// A slab on a truck cannot be binned, cut, scrapped or bundled.
	if err := inventory.MoveUnitToBin(ctx, pool, unit.ID, inventory.MoveUnitInput{BinUUID: &sourceBin}, 1); err == nil {
		t.Error("expected binning an in-transit unit to be refused")
	}
	if err := inventory.ScrapUnit(ctx, pool, unit.ID, nil, "", 1); err == nil {
		t.Error("expected scrapping an in-transit unit to be refused")
	}
	if _, err := inventory.CutUnit(ctx, pool, unit.ID, inventory.CutInput{
		Remnants: []inventory.CutPiece{{Serial: uniq("XR"), LengthMM: 900, WidthMM: 600}},
	}, 1); err == nil {
		t.Error("expected cutting an in-transit unit to be refused")
	}

	if _, err := Receive(ctx, pool, tr.ID, 1); err != nil {
		t.Fatalf("Receive: %v", err)
	}
	landed, err := inventory.GetUnit(ctx, pool, unit.ID)
	if err != nil {
		t.Fatalf("GetUnit: %v", err)
	}
	if landed.Status != inventory.StatusAvailable {
		t.Errorf("status after receive = %q, want available", landed.Status)
	}
	if landed.WarehouseID != dest {
		t.Errorf("warehouse after receive = %d, want %d", landed.WarehouseID, dest)
	}
	if got := stockAt(t, pool, item, dest); got != unit.Area {
		t.Errorf("destination after receive = %v, want %v", got, unit.Area)
	}
	// Total across both yards is conserved: the slab moved, it did not multiply.
	if got := stockAt(t, pool, item, 1) + stockAt(t, pool, item, dest); got != unit.Area {
		t.Errorf("total across warehouses = %v, want %v", got, unit.Area)
	}
}

func TestTransfer_RefusesSameWarehouseAndCancelAfterShipping(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	dest := secondWarehouse(t, pool)
	item := seedBulkItem(t, pool, 1, 20)

	if _, err := Create(ctx, pool, Input{
		FromWarehouseID: 1, ToWarehouseID: 1,
		Lines: []LineInput{{InventoryItemID: item, Qty: 5}},
	}, 1); err == nil {
		t.Error("expected a transfer to its own warehouse to be refused")
	}

	tr, err := Create(ctx, pool, Input{
		FromWarehouseID: 1, ToWarehouseID: dest,
		Lines: []LineInput{{InventoryItemID: item, Qty: 5}},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Shipping a draft skips approval.
	if _, err := Ship(ctx, pool, tr.ID, 1); err == nil {
		t.Error("expected shipping an unapproved transfer to be refused")
	}
	// Receiving before shipping would add stock that never left.
	approve(t, pool, tr.ID)
	if _, err := Receive(ctx, pool, tr.ID, 1); err == nil {
		t.Error("expected receiving before shipping to be refused")
	}
	if got := stockAt(t, pool, item, dest); got != 0 {
		t.Errorf("a refused receive added stock: %v", got)
	}

	if _, err := Ship(ctx, pool, tr.ID, 1); err != nil {
		t.Fatalf("Ship: %v", err)
	}
	// Cancelling now would strand the shipped stock in neither warehouse, with a
	// document saying "cancelled" to explain it.
	if _, err := Transition(ctx, pool, tr.ID, StatusCancelled, "changed our mind", 1); err == nil {
		t.Error("expected cancelling a shipped transfer to be refused")
	}
	if err := Delete(ctx, pool, tr.ID, 1); err == nil {
		t.Error("expected deleting a shipped transfer to be refused")
	}
	if got := stockAt(t, pool, item, 1); got != 15 {
		t.Errorf("source stock moved unexpectedly: %v", got)
	}
}

func TestTransfer_DraftFrozenAfterApprovalAndSoftDeleteWorks(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	dest := secondWarehouse(t, pool)
	item := seedBulkItem(t, pool, 1, 30)

	tr, err := Create(ctx, pool, Input{
		FromWarehouseID: 1, ToWarehouseID: dest,
		Lines: []LineInput{{InventoryItemID: item, Qty: 3}},
	}, 0) // unresolved actor, as most callers are
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := Update(ctx, pool, tr.ID, Input{
		FromWarehouseID: 1, ToWarehouseID: dest, Carrier: "Yard truck",
		Lines: []LineInput{{InventoryItemID: item, Qty: 8}},
	}, 0); err != nil {
		t.Fatalf("Update: %v", err)
	}
	reloaded, err := Get(ctx, pool, tr.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if reloaded.TotalQty != 8 || reloaded.Carrier != "Yard truck" {
		t.Errorf("after update: qty %v carrier %q; want 8 and Yard truck", reloaded.TotalQty, reloaded.Carrier)
	}

	approve(t, pool, tr.ID)
	if err := Update(ctx, pool, tr.ID, Input{
		FromWarehouseID: 1, ToWarehouseID: dest,
		Lines: []LineInput{{InventoryItemID: item, Qty: 999}},
	}, 0); err == nil {
		t.Error("expected editing an approved transfer to be refused")
	}

	// Soft delete with an unresolved actor — the paired CHECK case.
	other, err := Create(ctx, pool, Input{
		FromWarehouseID: 1, ToWarehouseID: dest,
		Lines: []LineInput{{InventoryItemID: item, Qty: 1}},
	}, 0)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := Delete(ctx, pool, other.ID, 0); err != nil {
		t.Fatalf("Delete with unresolved actor: %v", err)
	}
}
