// bintransfer/helpers_dbtest_test.go
//go:build dbtest

package bintransfer

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/dbtestutil"
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
	dbtestutil.EnsureLocation(t, pool)
	return pool
}

// otherLocation returns a second location, creating it once.
func otherLocation(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	ctx := context.Background()
	const name = "Bin Transfer Other Yard"
	var id int
	err := pool.QueryRow(ctx, `
		SELECT company_location_id FROM company_location
		WHERE name = $1 AND deleted_at IS NULL ORDER BY company_location_id LIMIT 1`, name).Scan(&id)
	if err == nil {
		return id
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO company_location (name) VALUES ($1) RETURNING company_location_id`, name).Scan(&id); err != nil {
		t.Fatalf("seed second location: %v", err)
	}
	return id
}

// newBin creates a bin at a location and returns its uuid.
func newBin(t *testing.T, pool *pgxpool.Pool, warehouseID int) string {
	t.Helper()
	code := uniq("BT")
	var uuid string
	if err := pool.QueryRow(context.Background(), `
		INSERT INTO inventory_bin (warehouse_id, bin_code, bin_name, bin_type, bin_path)
		VALUES ($1, $2, $2, 'rack', $2) RETURNING inventory_bin_uuid`, warehouseID, code).Scan(&uuid); err != nil {
		t.Fatalf("seed bin: %v", err)
	}
	return uuid
}

// newSlab creates a serialized slab at the main test location, optionally in a bin.
func newSlab(t *testing.T, pool *pgxpool.Pool, binUUID *string) *inventory.Unit {
	t.Helper()
	ctx := context.Background()
	var itemUUID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO inventory_item (inventory_item_sku, inventory_item_name,
			inventory_item_unit_id, inventory_item_tracking)
		SELECT $1, $1, u.unit_id, 'serialized' FROM lkp_unit u WHERE u.unit_code = 'SQFT'
		RETURNING inventory_item_uuid`, uniq("DBTEST-BT-S")).Scan(&itemUUID); err != nil {
		t.Fatalf("seed serialized item: %v", err)
	}
	u, err := inventory.CreateUnit(ctx, pool, inventory.CreateUnitInput{
		Serial: uniq("DBTEST-BTU"), InventoryItemUUID: itemUUID, WarehouseID: 1, BinUUID: binUUID,
		LengthMM: 3000, WidthMM: 1400, ThicknessMM: 30,
	}, 1)
	if err != nil {
		t.Fatalf("CreateUnit: %v", err)
	}
	return u
}

// binOf reads the uuid of the bin a unit is currently in ("" for none).
func binOf(t *testing.T, pool *pgxpool.Pool, unitUUID string) string {
	t.Helper()
	var out string
	if err := pool.QueryRow(context.Background(), `
		SELECT COALESCE(b.inventory_bin_uuid::text, '')
		FROM inventory_slab s LEFT JOIN inventory_bin b ON b.inventory_bin_id = s.inventory_bin_id
		WHERE s.inventory_slab_uuid = $1`, unitUUID).Scan(&out); err != nil {
		t.Fatalf("read unit bin: %v", err)
	}
	return out
}

// moveHistory counts a unit's bin_move history rows, and how many of them carry
// the given bin transfer document.
func moveHistory(t *testing.T, pool *pgxpool.Pool, unitUUID, transferUUID string) (total, tagged int) {
	t.Helper()
	if err := pool.QueryRow(context.Background(), `
		SELECT COUNT(*),
		       COUNT(*) FILTER (WHERE bt.bin_transfer_uuid = $2::uuid)
		FROM inventory_unit_history h
		JOIN inventory_slab s ON s.inventory_slab_id = h.inventory_slab_id
		LEFT JOIN bin_transfer bt ON bt.bin_transfer_id = h.bin_transfer_id
		WHERE s.inventory_slab_uuid = $1 AND h.history_action = 'bin_move'`,
		unitUUID, transferUUID).Scan(&total, &tagged); err != nil {
		t.Fatalf("read unit history: %v", err)
	}
	return total, tagged
}

// configureApprover makes employee 1 the sole configured approver at the Pending
// Approval gate for the rest of the test.
func configureApprover(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	var recordTypeID, statusID int
	if err := pool.QueryRow(ctx, `SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'BTRF'`).Scan(&recordTypeID); err != nil {
		t.Fatalf("resolve BTRF record type: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT record_status_id FROM lkp_record_status
		WHERE record_status_record_type = $1 AND record_status_code = 'PAPV'`, recordTypeID).Scan(&statusID); err != nil {
		t.Fatalf("resolve PAPV status: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO bin_transfer_approver (record_type_id, record_status_id, approver_employee_id)
		VALUES ($1, $2, 1) ON CONFLICT DO NOTHING`, recordTypeID, statusID); err != nil {
		t.Fatalf("seed bin_transfer_approver: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM bin_transfer_approver WHERE record_type_id = $1 AND record_status_id = $2`, recordTypeID, statusID)
	})
}

// draftMoving creates a draft transfer moving the given slabs into one bin.
func draftMoving(t *testing.T, pool *pgxpool.Pool, toBin string, units ...*inventory.Unit) *BinTransfer {
	t.Helper()
	lines := make([]LineInput, len(units))
	for i, u := range units {
		lines[i] = LineInput{UnitID: u.ID, ToBinID: toBin}
	}
	bt, err := Create(context.Background(), pool, Input{WarehouseID: 1, Lines: lines}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return bt
}
