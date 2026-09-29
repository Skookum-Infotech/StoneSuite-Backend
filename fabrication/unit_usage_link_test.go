// fabrication/unit_usage_link_test.go
//go:build dbtest

package fabrication

import (
	"context"
	"fmt"
	"testing"
	"time"

	"stonesuite-backend/inventory"
	"stonesuite-backend/query"
)

// TestInventoryUnit_NamesTheJobHoldingIt covers the seam between the two
// modules: the Inventory screen shows which fabrication job a reserved slab is
// held for, read from the allocation row this package writes.
func TestInventoryUnit_NamesTheJobHoldingIt(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	var itemUUID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO inventory_item (inventory_item_sku, inventory_item_name,
			inventory_item_unit_id, inventory_item_tracking)
		SELECT $1, $1, u.unit_id, 'serialized' FROM lkp_unit u WHERE u.unit_code = 'SQFT'
		RETURNING inventory_item_uuid`,
		fmt.Sprintf("DBTEST-JOBLINK-%d", time.Now().UnixNano())).Scan(&itemUUID); err != nil {
		t.Fatalf("seed item: %v", err)
	}
	unit, err := inventory.CreateUnit(ctx, pool, inventory.CreateUnitInput{
		Serial:            fmt.Sprintf("DBTEST-JOBLINK-%d", time.Now().UnixNano()),
		InventoryItemUUID: itemUUID, WarehouseID: 1,
		LengthMM: 3000, WidthMM: 1400, ThicknessMM: 30,
	}, 1)
	if err != nil {
		t.Fatalf("CreateUnit: %v", err)
	}
	if unit.Usage.JobID != "" || unit.Usage.SalesOrderID != "" || unit.Usage.SalesOrderNumber != "" {
		t.Fatalf("a slab no job has claimed must name no job or order, got %+v", unit.Usage)
	}

	soUUID := seedSalesOrder(t, pool)
	var soNumber string
	if err := pool.QueryRow(ctx,
		`SELECT sales_order_number FROM sales_order WHERE sales_order_uuid = $1`, soUUID).Scan(&soNumber); err != nil {
		t.Fatalf("read sales order number: %v", err)
	}
	job, err := Create(ctx, pool, CreateJobInput{SalesOrderUUID: soUUID}, 1)
	if err != nil {
		t.Fatalf("Create job: %v", err)
	}
	// Reserve directly: AllocateSlab only opens once a job reaches material
	// allocation, and walking the status machine there is not what is under test.
	if _, err := pool.Exec(ctx, `
		INSERT INTO fabrication_job_slab (fabrication_job_id, inventory_slab_id, allocation_status)
		SELECT fj.fabrication_job_id, s.inventory_slab_id, 'reserved'
		FROM fabrication_job fj, inventory_slab s
		WHERE fj.fabrication_job_uuid = $1 AND s.inventory_slab_uuid = $2`, job.ID, unit.ID); err != nil {
		t.Fatalf("reserve slab: %v", err)
	}
	if _, err := pool.Exec(ctx,
		`UPDATE inventory_slab SET slab_status = 'reserved' WHERE inventory_slab_uuid = $1`, unit.ID); err != nil {
		t.Fatalf("mark slab reserved: %v", err)
	}

	got, err := inventory.GetUnit(ctx, pool, unit.ID)
	if err != nil {
		t.Fatalf("GetUnit: %v", err)
	}
	if got.Usage.JobID != job.ID || got.Usage.JobNumber != job.Number {
		t.Errorf("job = %q / %q, want %q / %q", got.Usage.JobID, got.Usage.JobNumber, job.ID, job.Number)
	}
	if got.Usage.SalesOrderID != soUUID || got.Usage.SalesOrderNumber != soNumber {
		t.Errorf("sales order = %q / %q, want %q / %q", got.Usage.SalesOrderID, got.Usage.SalesOrderNumber, soUUID, soNumber)
	}
	if got.Usage.ReservedAt == nil {
		t.Error("ReservedAt must say when the job took the slab")
	}
	if got.Usage.ConsumedAt != nil || got.Usage.UsedArea != 0 {
		t.Errorf("a reserved slab has not been cut yet, got %+v", got.Usage)
	}

	// The list reads the same projection, so it must name the order too.
	page, err := inventory.SearchUnits(ctx, pool, query.Request{Search: unit.Serial, Limit: 10})
	if err != nil {
		t.Fatalf("SearchUnits: %v", err)
	}
	if len(page.Records) != 1 || page.Records[0].Usage.SalesOrderNumber != soNumber || page.Records[0].Usage.JobNumber != job.Number {
		t.Fatalf("list must name the job and order on the slab's row, got %+v", page.Records)
	}

	// Once the job has cut the slab it is still the order's stone.
	if _, err := pool.Exec(ctx, `
		UPDATE fabrication_job_slab SET allocation_status = 'consumed'
		WHERE inventory_slab_id = (SELECT inventory_slab_id FROM inventory_slab WHERE inventory_slab_uuid = $1)`, unit.ID); err != nil {
		t.Fatalf("mark allocation consumed: %v", err)
	}
	cut, err := inventory.GetUnit(ctx, pool, unit.ID)
	if err != nil {
		t.Fatalf("GetUnit after cut: %v", err)
	}
	if cut.Usage.SalesOrderNumber != soNumber || cut.Usage.JobNumber != job.Number {
		t.Errorf("a cut slab must keep naming its job and order, got %+v", cut.Usage)
	}
}
