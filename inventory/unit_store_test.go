// inventory/unit_store_test.go
//go:build dbtest

package inventory

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// uniq suffixes a fixture identifier so the suite is re-runnable against a
// database it has already touched. Fixed SKUs would collide with
// uq_inventory_item_sku_active on the second run, and the tests would fail for
// a reason that has nothing to do with the code under test.
func uniq(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

// seedAreaItem creates a catalogue item denominated in square feet — the only
// kind a serialized unit may hang off, since a count unit would make offcut
// recovery produce a fractional count.
func seedAreaItem(t *testing.T, pool *pgxpool.Pool, sku string) string {
	t.Helper()
	ctx := context.Background()
	var uuid string
	err := pool.QueryRow(ctx, `
		INSERT INTO inventory_item (inventory_item_sku, inventory_item_name,
			inventory_item_unit_id, inventory_item_tracking)
		SELECT $1, $1, u.unit_id, 'serialized' FROM lkp_unit u WHERE u.unit_code = 'SQFT'
		RETURNING inventory_item_uuid`, sku).Scan(&uuid)
	if err != nil {
		t.Fatalf("seed item: %v", err)
	}
	return uuid
}

// onHand reads the running stock total for an item across all warehouses.
func onHand(t *testing.T, pool *pgxpool.Pool, itemUUID string) float64 {
	t.Helper()
	var v float64
	if err := pool.QueryRow(context.Background(), `
		SELECT COALESCE(SUM(s.quantity_on_hand),0)
		FROM inventory_stock s JOIN inventory_item i ON i.inventory_item_id = s.inventory_item_id
		WHERE i.inventory_item_uuid = $1`, itemUUID).Scan(&v); err != nil {
		t.Fatalf("read on-hand: %v", err)
	}
	return v
}

// ledgerSum reads the sum of every slab-ledger delta for an item. The whole
// design rests on this equalling on-hand.
func ledgerSum(t *testing.T, pool *pgxpool.Pool, itemUUID string) float64 {
	t.Helper()
	var v float64
	if err := pool.QueryRow(context.Background(), `
		SELECT COALESCE(SUM(l.quantity_delta),0)
		FROM inventory_slab_ledger l JOIN inventory_item i ON i.inventory_item_id = l.inventory_item_id
		WHERE i.inventory_item_uuid = $1`, itemUUID).Scan(&v); err != nil {
		t.Fatalf("read ledger sum: %v", err)
	}
	return v
}

func TestCreateUnit_ComputesAreaFromMillimetres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	item := seedAreaItem(t, pool, uniq("DBTEST-AREA"))

	// The client sends a deliberately WRONG area. It must be ignored in favour
	// of the value computed from the millimetres into the item's own unit —
	// this is the guard against a SQM measurement being ledgered against a
	// SQFT item, which no database constraint would catch.
	u, err := CreateUnit(ctx, pool, CreateUnitInput{
		Serial:            uniq("DBTEST-SLAB"),
		InventoryItemUUID: item,
		WarehouseID:       1,
		LengthMM:          3000,
		WidthMM:           1400,
		ThicknessMM:       30,
		Area:              999999, // lie
	}, 1)
	if err != nil {
		t.Fatalf("CreateUnit: %v", err)
	}

	const wantArea = 45.208 // 3000*1400 mm^2 in square feet
	if u.Area != wantArea {
		t.Fatalf("Area = %v, want %v (client-supplied area must be ignored)", u.Area, wantArea)
	}
	if got := onHand(t, pool, item); got != wantArea {
		t.Errorf("on-hand = %v, want %v", got, wantArea)
	}
	if got := ledgerSum(t, pool, item); got != onHand(t, pool, item) {
		t.Errorf("ledger sum %v != on-hand %v; the core invariant is broken", got, onHand(t, pool, item))
	}
}

func TestCreateUnit_RejectsCountUnitItem(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	var item string
	if err := pool.QueryRow(ctx, `
		INSERT INTO inventory_item (inventory_item_sku, inventory_item_name, inventory_item_unit_id)
		SELECT $1, $1, u.unit_id FROM lkp_unit u WHERE u.unit_code = 'EA'
		RETURNING inventory_item_uuid`, uniq("DBTEST-COUNT")).Scan(&item); err != nil {
		t.Fatalf("seed count item: %v", err)
	}

	_, err := CreateUnit(ctx, pool, CreateUnitInput{
		Serial: uniq("DBTEST-SLAB"), InventoryItemUUID: item, WarehouseID: 1,
		LengthMM: 3000, WidthMM: 1400, ThicknessMM: 30,
	}, 1)
	if err == nil {
		t.Fatal("expected a count-unit item to be rejected")
	}
	if !IsClientError(err) {
		t.Errorf("error should be a ClientError (400), got %T: %v", err, err)
	}
}

func TestScrapUnit_DecrementsStockAndKeepsTheInvariant(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	item := seedAreaItem(t, pool, uniq("DBTEST-AREA"))

	u, err := CreateUnit(ctx, pool, CreateUnitInput{
		Serial: uniq("DBTEST-SLAB"), InventoryItemUUID: item, WarehouseID: 1,
		LengthMM: 3000, WidthMM: 1400, ThicknessMM: 30,
	}, 1)
	if err != nil {
		t.Fatalf("CreateUnit: %v", err)
	}
	if onHand(t, pool, item) == 0 {
		t.Fatal("stock did not increase on receipt")
	}

	if err := ScrapUnit(ctx, pool, u.ID, nil, "damaged in transit", 1); err != nil {
		t.Fatalf("ScrapUnit: %v", err)
	}
	if got := onHand(t, pool, item); got != 0 {
		t.Errorf("on-hand after scrap = %v, want 0", got)
	}
	if got := ledgerSum(t, pool, item); got != 0 {
		t.Errorf("ledger sum after scrap = %v, want 0", got)
	}

	// Scrapping twice must be refused rather than double-deducting.
	if err := ScrapUnit(ctx, pool, u.ID, nil, "", 1); err == nil {
		t.Error("expected the second scrap to be refused")
	}
	if got := onHand(t, pool, item); got != 0 {
		t.Errorf("on-hand after a refused second scrap = %v, want 0", got)
	}
}

func TestMoveUnitToBin_IsStockNeutral(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	item := seedAreaItem(t, pool, uniq("DBTEST-AREA"))

	u, err := CreateUnit(ctx, pool, CreateUnitInput{
		Serial: uniq("DBTEST-SLAB"), InventoryItemUUID: item, WarehouseID: 1,
		LengthMM: 3000, WidthMM: 1400, ThicknessMM: 30,
	}, 1)
	if err != nil {
		t.Fatalf("CreateUnit: %v", err)
	}
	before := onHand(t, pool, item)
	ledgerBefore := ledgerSum(t, pool, item)

	// The seeded STAGING bin in MAIN.
	var binUUID string
	if err := pool.QueryRow(ctx, `
		SELECT inventory_bin_uuid FROM inventory_bin
		WHERE LOWER(bin_code) = 'staging' AND bin_deleted_at IS NULL`).Scan(&binUUID); err != nil {
		t.Fatalf("find staging bin: %v", err)
	}

	if err := MoveUnitToBin(ctx, pool, u.ID, MoveUnitInput{BinUUID: &binUUID, Note: "put away"}, 1); err != nil {
		t.Fatalf("MoveUnitToBin: %v", err)
	}

	// A bin move must touch neither stock nor the ledger: bins locate units,
	// inventory_stock is keyed on (item, warehouse), so the move is
	// stock-neutral by construction.
	if got := onHand(t, pool, item); got != before {
		t.Errorf("on-hand changed on a bin move: %v -> %v", before, got)
	}
	if got := ledgerSum(t, pool, item); got != ledgerBefore {
		t.Errorf("a bin move wrote a ledger row: sum %v -> %v", ledgerBefore, got)
	}

	moved, err := GetUnit(ctx, pool, u.ID)
	if err != nil {
		t.Fatalf("GetUnit: %v", err)
	}
	if moved.BinID == nil || *moved.BinID != binUUID {
		t.Errorf("unit was not relocated; binId = %v", moved.BinID)
	}
	if moved.BinPath != "STAGING" {
		t.Errorf("binPath = %q, want %q", moved.BinPath, "STAGING")
	}

	// And it must be recorded operationally.
	entries, err := UnitHistory(ctx, pool, u.ID)
	if err != nil {
		t.Fatalf("UnitHistory: %v", err)
	}
	var sawMove bool
	for _, e := range entries {
		if e.Action == "bin_move" && e.ToBin == "STAGING" {
			sawMove = true
		}
	}
	if !sawMove {
		t.Errorf("no bin_move history row was written; got %+v", entries)
	}
}
