// inventoryadjustment/store_dbtest_test.go
//go:build dbtest

package inventoryadjustment

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

// seedBulkItem creates a quantity-tracked item with stock on hand.
func seedBulkItem(t *testing.T, pool *pgxpool.Pool, onHandQty float64) (uuid string, itemID int) {
	t.Helper()
	ctx := context.Background()
	sku := uniq("DBTEST-ADJ-BULK")
	if err := pool.QueryRow(ctx, `
		INSERT INTO inventory_item (inventory_item_sku, inventory_item_name,
			inventory_item_unit_id, inventory_item_tracking)
		SELECT $1, $1, u.unit_id, 'quantity' FROM lkp_unit u WHERE u.unit_code = 'EA'
		RETURNING inventory_item_uuid, inventory_item_id`, sku).Scan(&uuid, &itemID); err != nil {
		t.Fatalf("seed bulk item: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO inventory_stock (inventory_item_id, warehouse_id, quantity_on_hand)
		VALUES ($1, 1, $2)`, itemID, onHandQty); err != nil {
		t.Fatalf("seed stock: %v", err)
	}
	return uuid, itemID
}

// seedSerializedItemAndUnit creates an area item and receives one slab.
func seedSerializedItemAndUnit(t *testing.T, pool *pgxpool.Pool) (itemUUID string, unit *inventory.Unit) {
	t.Helper()
	ctx := context.Background()
	sku := uniq("DBTEST-ADJ-SER")
	if err := pool.QueryRow(ctx, `
		INSERT INTO inventory_item (inventory_item_sku, inventory_item_name,
			inventory_item_unit_id, inventory_item_tracking)
		SELECT $1, $1, u.unit_id, 'serialized' FROM lkp_unit u WHERE u.unit_code = 'SQFT'
		RETURNING inventory_item_uuid`, sku).Scan(&itemUUID); err != nil {
		t.Fatalf("seed serialized item: %v", err)
	}
	u, err := inventory.CreateUnit(ctx, pool, inventory.CreateUnitInput{
		Serial: uniq("DBTEST-ADJ-U"), InventoryItemUUID: itemUUID, WarehouseID: 1,
		LengthMM: 3000, WidthMM: 1400, ThicknessMM: 30,
	}, 1)
	if err != nil {
		t.Fatalf("CreateUnit: %v", err)
	}
	return itemUUID, u
}

func onHand(t *testing.T, pool *pgxpool.Pool, itemUUID string) float64 {
	t.Helper()
	var v float64
	if err := pool.QueryRow(context.Background(), `
		SELECT COALESCE(SUM(s.quantity_on_hand),0) FROM inventory_stock s
		JOIN inventory_item ii ON ii.inventory_item_id = s.inventory_item_id
		WHERE ii.inventory_item_uuid = $1`, itemUUID).Scan(&v); err != nil {
		t.Fatalf("read on-hand: %v", err)
	}
	return v
}

func ledgerSum(t *testing.T, pool *pgxpool.Pool, itemUUID string) float64 {
	t.Helper()
	var v float64
	if err := pool.QueryRow(context.Background(), `
		SELECT COALESCE(SUM(d),0) FROM (
			SELECT l.quantity_delta AS d FROM inventory_ledger l
			  JOIN inventory_item ii ON ii.inventory_item_id = l.inventory_item_id
			  WHERE ii.inventory_item_uuid = $1
			UNION ALL
			SELECT sl.quantity_delta FROM inventory_slab_ledger sl
			  JOIN inventory_item ii ON ii.inventory_item_id = sl.inventory_item_id
			  WHERE ii.inventory_item_uuid = $1) x`, itemUUID).Scan(&v); err != nil {
		t.Fatalf("read ledger sum: %v", err)
	}
	return v
}

// approveAndPost walks a draft through the approval chain.
func approveAndPost(t *testing.T, pool *pgxpool.Pool, uuid string) (*Adjustment, error) {
	t.Helper()
	ctx := context.Background()
	if _, err := Transition(ctx, pool, uuid, StatusPending, "", 1); err != nil {
		t.Fatalf("to pending: %v", err)
	}
	if _, err := Transition(ctx, pool, uuid, StatusApproved, "", 1); err != nil {
		t.Fatalf("to approved: %v", err)
	}
	return Post(ctx, pool, uuid, 1)
}

func TestPostBulkAdjustment_MovesStockAndKeepsTheInvariant(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	itemUUID, _ := seedBulkItem(t, pool, 100)

	a, err := Create(ctx, pool, Input{
		WarehouseID: 1,
		Lines: []LineInput{
			{InventoryItemID: itemUUID, ReasonID: 1, QtyDelta: -7, Notes: "damaged in the rack"},
		},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if a.Number == "" {
		t.Error("document number was not assigned")
	}
	if a.NetDelta != -7 {
		t.Errorf("NetDelta = %v, want -7", a.NetDelta)
	}
	// Drafting must not move anything.
	if got := onHand(t, pool, itemUUID); got != 100 {
		t.Fatalf("drafting an adjustment changed stock: %v", got)
	}

	posted, err := approveAndPost(t, pool, a.ID)
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	if posted.StatusCode != StatusPosted {
		t.Errorf("status = %q, want %q", posted.StatusCode, StatusPosted)
	}
	if got := onHand(t, pool, itemUUID); got != 93 {
		t.Errorf("on-hand = %v, want 93", got)
	}
	// The invariant the whole ledger design exists to hold.
	if got := ledgerSum(t, pool, itemUUID); got != -7 {
		t.Errorf("ledger sum = %v, want -7 (stock was seeded directly, so only the adjustment is ledgered)", got)
	}

	// Posting twice must not move stock twice. POST is terminal, so the status
	// guard refuses before the once-only index is even reached.
	if _, err := Post(ctx, pool, a.ID, 1); err == nil {
		t.Error("expected a second post to be refused")
	}
	if got := onHand(t, pool, itemUUID); got != 93 {
		t.Errorf("a refused second post changed stock: %v", got)
	}
}

func TestPostSerializedAdjustment_UsesTheSlabsOwnArea(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	itemUUID, unit := seedSerializedItemAndUnit(t, pool)
	before := onHand(t, pool, itemUUID)

	// A wildly wrong client quantity must be ignored: only its SIGN is read.
	a, err := Create(ctx, pool, Input{
		WarehouseID: 1,
		Lines: []LineInput{
			{InventoryItemID: itemUUID, InventoryUnitID: &unit.ID, ReasonID: 1, QtyDelta: -99999},
		},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if a.Lines[0].QtyDelta != -unit.Area {
		t.Errorf("line delta = %v, want %v — the slab's own area, not the client's number",
			a.Lines[0].QtyDelta, -unit.Area)
	}

	if _, err := approveAndPost(t, pool, a.ID); err != nil {
		t.Fatalf("Post: %v", err)
	}
	if got := onHand(t, pool, itemUUID); got != before-unit.Area {
		t.Errorf("on-hand = %v, want %v", got, before-unit.Area)
	}
	if got, want := ledgerSum(t, pool, itemUUID), onHand(t, pool, itemUUID); got != want {
		t.Errorf("ledger sum %v != on-hand %v", got, want)
	}

	reloaded, err := inventory.GetUnit(ctx, pool, unit.ID)
	if err != nil {
		t.Fatalf("GetUnit: %v", err)
	}
	if reloaded.Status != "scrapped" {
		t.Errorf("unit status = %q, want scrapped", reloaded.Status)
	}

	// And it can be brought back if it turns up.
	back, err := Create(ctx, pool, Input{
		WarehouseID: 1,
		Lines: []LineInput{
			{InventoryItemID: itemUUID, InventoryUnitID: &unit.ID, ReasonID: 1, QtyDelta: 1},
		},
	}, 1)
	if err != nil {
		t.Fatalf("Create restore: %v", err)
	}
	if _, err := approveAndPost(t, pool, back.ID); err != nil {
		t.Fatalf("Post restore: %v", err)
	}
	if got := onHand(t, pool, itemUUID); got != before {
		t.Errorf("on-hand after restore = %v, want %v", got, before)
	}
}

func TestAdjustment_RefusesTheWrongStockModelAndUnapprovedPosts(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	bulkUUID, _ := seedBulkItem(t, pool, 50)
	serUUID, unit := seedSerializedItemAndUnit(t, pool)

	// A quantity against a serialized item would move stock without naming which
	// slab moved.
	if _, err := Create(ctx, pool, Input{WarehouseID: 1, Lines: []LineInput{
		{InventoryItemID: serUUID, ReasonID: 1, QtyDelta: -5},
	}}, 1); err == nil {
		t.Error("expected a bulk quantity against a serialized item to be refused")
	}
	// And a unit against a quantity item is equally incoherent.
	if _, err := Create(ctx, pool, Input{WarehouseID: 1, Lines: []LineInput{
		{InventoryItemID: bulkUUID, InventoryUnitID: &unit.ID, ReasonID: 1, QtyDelta: -1},
	}}, 1); err == nil {
		t.Error("expected a unit against a quantity-tracked item to be refused")
	}
	// A reason code is not optional on a document that exists to be defended.
	if _, err := Create(ctx, pool, Input{WarehouseID: 1, Lines: []LineInput{
		{InventoryItemID: bulkUUID, QtyDelta: -5},
	}}, 1); err == nil {
		t.Error("expected a line with no reason code to be refused")
	}

	// Posting must go through approval.
	a, err := Create(ctx, pool, Input{WarehouseID: 1, Lines: []LineInput{
		{InventoryItemID: bulkUUID, ReasonID: 1, QtyDelta: -5},
	}}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := Post(ctx, pool, a.ID, 1); err == nil {
		t.Error("expected posting a draft to be refused")
	}
	if got := onHand(t, pool, bulkUUID); got != 50 {
		t.Errorf("a refused post changed stock: %v", got)
	}
	// Skipping straight from draft to approved is not on the machine.
	if _, err := Transition(ctx, pool, a.ID, StatusApproved, "", 1); err == nil {
		t.Error("expected draft -> approved to be refused")
	}
}

func TestAdjustment_DraftEditableThenFrozen(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	bulkUUID, _ := seedBulkItem(t, pool, 50)

	a, err := Create(ctx, pool, Input{WarehouseID: 1, Lines: []LineInput{
		{InventoryItemID: bulkUUID, ReasonID: 1, QtyDelta: -5},
	}}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// A draft is a form: editing replaces the whole line set.
	if err := Update(ctx, pool, a.ID, Input{WarehouseID: 1, Notes: "revised", Lines: []LineInput{
		{InventoryItemID: bulkUUID, ReasonID: 1, QtyDelta: -3},
		{InventoryItemID: bulkUUID, ReasonID: 1, QtyDelta: -1},
	}}, 1); err != nil {
		t.Fatalf("Update: %v", err)
	}
	reloaded, err := Get(ctx, pool, a.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if len(reloaded.Lines) != 2 || reloaded.NetDelta != -4 {
		t.Errorf("after update: %d lines, net %v; want 2 lines, net -4", len(reloaded.Lines), reloaded.NetDelta)
	}

	// Once approved, the numbers signed off must be the numbers that post.
	if _, err := Transition(ctx, pool, a.ID, StatusPending, "", 1); err != nil {
		t.Fatalf("to pending: %v", err)
	}
	if _, err := Transition(ctx, pool, a.ID, StatusApproved, "", 1); err != nil {
		t.Fatalf("to approved: %v", err)
	}
	if err := Update(ctx, pool, a.ID, Input{WarehouseID: 1, Lines: []LineInput{
		{InventoryItemID: bulkUUID, ReasonID: 1, QtyDelta: -999},
	}}, 1); err == nil {
		t.Error("expected editing an approved adjustment to be refused")
	}
}

// Soft delete is the case that was a guaranteed 500 across the whole repo:
// resolveEmployeeID returns 0 for most callers, and the paired CHECK rejects a
// NULL deleted_by.
func TestAdjustment_SoftDeleteWithUnresolvedActor(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	bulkUUID, _ := seedBulkItem(t, pool, 10)

	a, err := Create(ctx, pool, Input{WarehouseID: 1, Lines: []LineInput{
		{InventoryItemID: bulkUUID, ReasonID: 1, QtyDelta: -1},
	}}, 0)
	if err != nil {
		t.Fatalf("Create with unresolved actor: %v", err)
	}
	if err := Delete(ctx, pool, a.ID, 0); err != nil {
		t.Fatalf("Delete with unresolved actor: %v", err)
	}
	if _, err := Get(ctx, pool, a.ID); err != ErrNotFound {
		t.Errorf("Get after delete = %v, want ErrNotFound", err)
	}
}
