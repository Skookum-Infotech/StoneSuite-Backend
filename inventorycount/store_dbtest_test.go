// inventorycount/store_dbtest_test.go
//go:build dbtest

package inventorycount

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

// countWarehouse gives each test its own warehouse, so one test's freeze does
// not snapshot another's stock — the whole suite shares a database.
func countWarehouse(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var id int
	// warehouse_code is VARCHAR(20), so the full nanosecond suffix overflows it.
	code := fmt.Sprintf("CW%d", time.Now().UnixNano()%1e12)
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO lkp_warehouse (warehouse_code, warehouse_name, warehouse_created_by)
		VALUES ($1, $1, 1) RETURNING warehouse_id`, code).Scan(&id); err != nil {
		t.Fatalf("seed warehouse: %v", err)
	}
	return id
}

func seedBulk(t *testing.T, pool *pgxpool.Pool, warehouseID int, qty float64) string {
	t.Helper()
	var uuid string
	var itemID int
	sku := uniq("DBTEST-CNT-B")
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO inventory_item (inventory_item_sku, inventory_item_name,
			inventory_item_unit_id, inventory_item_tracking)
		SELECT $1, $1, u.unit_id, 'quantity' FROM lkp_unit u WHERE u.unit_code = 'EA'
		RETURNING inventory_item_uuid, inventory_item_id`, sku).Scan(&uuid, &itemID); err != nil {
		t.Fatalf("seed bulk item: %v", err)
	}
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO inventory_stock (inventory_item_id, warehouse_id, quantity_on_hand)
		VALUES ($1,$2,$3)`, itemID, warehouseID, qty); err != nil {
		t.Fatalf("seed stock: %v", err)
	}
	return uuid
}

func seedSlab(t *testing.T, pool *pgxpool.Pool, warehouseID int) (itemUUID string, unit *inventory.Unit) {
	t.Helper()
	ctx := context.Background()
	sku := uniq("DBTEST-CNT-S")
	if err := pool.QueryRow(ctx, `
		INSERT INTO inventory_item (inventory_item_sku, inventory_item_name,
			inventory_item_unit_id, inventory_item_tracking)
		SELECT $1, $1, u.unit_id, 'serialized' FROM lkp_unit u WHERE u.unit_code = 'SQFT'
		RETURNING inventory_item_uuid`, sku).Scan(&itemUUID); err != nil {
		t.Fatalf("seed serialized item: %v", err)
	}
	u, err := inventory.CreateUnit(ctx, pool, inventory.CreateUnitInput{
		Serial: uniq("DBTEST-CU"), InventoryItemUUID: itemUUID, WarehouseID: warehouseID,
		LengthMM: 3000, WidthMM: 1400, ThicknessMM: 30,
	}, 1)
	if err != nil {
		t.Fatalf("CreateUnit: %v", err)
	}
	return itemUUID, u
}

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

func lineFor(t *testing.T, c *Count, itemUUID string) Line {
	t.Helper()
	for _, l := range c.Lines {
		if l.InventoryItemID == itemUUID {
			return l
		}
	}
	t.Fatalf("no line for item %s in %d lines", itemUUID, len(c.Lines))
	return Line{}
}

func TestFreezeSnapshotsAndPostWritesTheVariance(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	wh := countWarehouse(t, pool)
	bulk := seedBulk(t, pool, wh, 100)

	c, err := Create(ctx, pool, Input{WarehouseID: wh}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	frozen, err := Freeze(ctx, pool, c.ID, 1)
	if err != nil {
		t.Fatalf("Freeze: %v", err)
	}
	if frozen.StatusCode != StatusCounting || frozen.FrozenAt == nil {
		t.Fatalf("after freeze: status %q frozenAt %v", frozen.StatusCode, frozen.FrozenAt)
	}
	line := lineFor(t, frozen, bulk)
	if line.SystemQty != 100 {
		t.Errorf("snapshot = %v, want 100", line.SystemQty)
	}
	// Not counted yet is NOT counted-zero. Collapsing the two would write off
	// every shelf the crew has not reached.
	if line.CountedQty != nil || line.Variance != nil {
		t.Errorf("uncounted line has countedQty %v variance %v, want both nil", line.CountedQty, line.Variance)
	}

	// The crew finds 97.
	got97 := 97.0
	counted, err := RecordCounts(ctx, pool, c.ID, []CountEntry{
		{LineID: line.ID, CountedQty: &got97, ReasonID: intPtr(1), Notes: "three broken"},
	}, 1)
	if err != nil {
		t.Fatalf("RecordCounts: %v", err)
	}
	cl := lineFor(t, counted, bulk)
	if cl.Variance == nil || *cl.Variance != -3 {
		t.Fatalf("variance = %v, want -3", cl.Variance)
	}
	// Counting changes nothing until the count posts.
	if got := stockAt(t, pool, bulk, wh); got != 100 {
		t.Errorf("counting moved stock: %v", got)
	}

	for _, to := range []string{StatusInReview, StatusApproved} {
		if _, err := Transition(ctx, pool, c.ID, to, "", 1); err != nil {
			t.Fatalf("to %s: %v", to, err)
		}
	}
	if _, err := Post(ctx, pool, c.ID, 1); err != nil {
		t.Fatalf("Post: %v", err)
	}
	if got := stockAt(t, pool, bulk, wh); got != 97 {
		t.Errorf("on-hand after post = %v, want 97", got)
	}
	if _, err := Post(ctx, pool, c.ID, 1); err == nil {
		t.Error("expected a second post to be refused")
	}
	if got := stockAt(t, pool, bulk, wh); got != 97 {
		t.Errorf("a refused second post changed stock: %v", got)
	}
}

func TestFreezeBlocksMovementInScope(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	wh := countWarehouse(t, pool)
	_, unit := seedSlab(t, pool, wh)

	var bin string
	if err := pool.QueryRow(ctx, `
		INSERT INTO inventory_bin (warehouse_id, bin_code, bin_name, bin_type, bin_path)
		VALUES ($1,$2,$2,'rack',$2) RETURNING inventory_bin_uuid`, wh, uniq("CB")).Scan(&bin); err != nil {
		t.Fatalf("seed bin: %v", err)
	}
	// Movement is fine before the freeze.
	if err := inventory.MoveUnitToBin(ctx, pool, unit.ID, inventory.MoveUnitInput{BinUUID: &bin}, 1); err != nil {
		t.Fatalf("MoveUnitToBin before freeze: %v", err)
	}

	c, err := Create(ctx, pool, Input{WarehouseID: wh}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := Freeze(ctx, pool, c.ID, 1); err != nil {
		t.Fatalf("Freeze: %v", err)
	}

	// Now stock cannot move under the counters' feet.
	if err := inventory.MoveUnitToBin(ctx, pool, unit.ID, inventory.MoveUnitInput{BinUUID: nil}, 1); err == nil {
		t.Error("expected a bin move inside a frozen count's scope to be refused")
	}
	// A second count over the same scope would snapshot the first's pending
	// variances and post both.
	other, err := Create(ctx, pool, Input{WarehouseID: wh}, 1)
	if err != nil {
		t.Fatalf("Create second: %v", err)
	}
	if _, err := Freeze(ctx, pool, other.ID, 1); err == nil {
		t.Error("expected an overlapping count to be refused")
	}

	// Cancelling releases the freeze.
	if _, err := Transition(ctx, pool, c.ID, StatusCancelled, "abandoned", 1); err != nil {
		t.Fatalf("cancel: %v", err)
	}
	if err := inventory.MoveUnitToBin(ctx, pool, unit.ID, inventory.MoveUnitInput{BinUUID: &bin}, 1); err != nil {
		t.Errorf("MoveUnitToBin after cancel: %v", err)
	}
}

func TestCountSerializedMissingSlabWritesItOff(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	wh := countWarehouse(t, pool)
	item, unit := seedSlab(t, pool, wh)
	before := stockAt(t, pool, item, wh)

	c, err := Create(ctx, pool, Input{WarehouseID: wh}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	frozen, err := Freeze(ctx, pool, c.ID, 1)
	if err != nil {
		t.Fatalf("Freeze: %v", err)
	}
	line := lineFor(t, frozen, item)
	if line.UnitSerial != unit.Serial {
		t.Errorf("line serial = %q, want %q", line.UnitSerial, unit.Serial)
	}

	// A slab is binary: not on the rack.
	no := false
	if _, err := RecordCounts(ctx, pool, c.ID, []CountEntry{
		{LineID: line.ID, Found: &no, ReasonID: intPtr(1), Notes: "not in the yard"},
	}, 1); err != nil {
		t.Fatalf("RecordCounts: %v", err)
	}
	for _, to := range []string{StatusInReview, StatusApproved} {
		if _, err := Transition(ctx, pool, c.ID, to, "", 1); err != nil {
			t.Fatalf("to %s: %v", to, err)
		}
	}
	if _, err := Post(ctx, pool, c.ID, 1); err != nil {
		t.Fatalf("Post: %v", err)
	}

	if got := stockAt(t, pool, item, wh); got != before-unit.Area {
		t.Errorf("on-hand = %v, want %v", got, before-unit.Area)
	}
	gone, err := inventory.GetUnit(ctx, pool, unit.ID)
	if err != nil {
		t.Fatalf("GetUnit: %v", err)
	}
	if gone.Status != inventory.StatusScrapped {
		t.Errorf("missing slab status = %q, want scrapped", gone.Status)
	}
}

func TestCount_RefusesReviewWithUncountedLinesAndVarianceWithoutReason(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	wh := countWarehouse(t, pool)
	bulk := seedBulk(t, pool, wh, 20)

	c, err := Create(ctx, pool, Input{WarehouseID: wh}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	frozen, err := Freeze(ctx, pool, c.ID, 1)
	if err != nil {
		t.Fatalf("Freeze: %v", err)
	}
	// An uncounted line has a NULL variance, which posting skips — so an
	// unfinished count would post as "no discrepancy" for everything nobody
	// looked at.
	if _, err := Transition(ctx, pool, c.ID, StatusInReview, "", 1); err == nil {
		t.Error("expected review with uncounted lines to be refused")
	}

	line := lineFor(t, frozen, bulk)
	short := 15.0
	if _, err := RecordCounts(ctx, pool, c.ID, []CountEntry{
		{LineID: line.ID, CountedQty: &short}, // no reason code
	}, 1); err != nil {
		t.Fatalf("RecordCounts: %v", err)
	}
	for _, to := range []string{StatusInReview, StatusApproved} {
		if _, err := Transition(ctx, pool, c.ID, to, "", 1); err != nil {
			t.Fatalf("to %s: %v", to, err)
		}
	}
	// A five-unit write-off nobody has explained must not post.
	if _, err := Post(ctx, pool, c.ID, 1); err == nil {
		t.Error("expected posting a variance with no reason code to be refused")
	}
	if got := stockAt(t, pool, bulk, wh); got != 20 {
		t.Errorf("a refused post changed stock: %v", got)
	}

	// Freezing is not reachable through the plain transition endpoint, which
	// would skip the snapshot entirely.
	fresh, err := Create(ctx, pool, Input{WarehouseID: countWarehouse(t, pool)}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := Transition(ctx, pool, fresh.ID, StatusCounting, "", 1); err == nil {
		t.Error("expected DRFT -> CNTG through transition to be refused")
	}
}

func intPtr(v int) *int { return &v }
