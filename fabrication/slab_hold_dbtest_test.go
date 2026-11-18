//go:build dbtest

package fabrication

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"

	"stonesuite-backend/inventory"
)

// A slab the order's own drawing holds is the job's to take; the hold is handed over
// (the column cleared, the job's allocation row now owns it). A slab held for another
// order is not.
func TestMaterialAllocationTakesOverAHoldFromTheJobsOwnOrder(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	orderUUID := seedSalesOrder(t, pool)
	otherOrderUUID := seedSalesOrder(t, pool)
	job, err := Create(ctx, pool, CreateJobInput{SalesOrderUUID: orderUUID}, 1)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE fabrication_job SET workflow_version=2,delivery_mode='supply_only' WHERE fabrication_job_uuid=$1`, job.ID)
	require.NoError(t, err)

	var item string
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO inventory_item(inventory_item_sku,inventory_item_name,inventory_item_unit_id,inventory_item_tracking) SELECT $1,'Stone',unit_id,'serialized' FROM lkp_unit WHERE unit_code='SQFT' RETURNING inventory_item_uuid`, testActionUUID()).Scan(&item))
	// Both orders ask for the stone, so either drawing may hold a slab of it.
	for _, uuid := range []string{orderUUID, otherOrderUUID} {
		_, err = pool.Exec(ctx, `
			INSERT INTO sales_order_item (sales_order_id, line_number, discount_percent, unit_price, inventory_item_id)
			SELECT so.sales_order_id, 90, 0, 10, ii.inventory_item_id
			FROM sales_order so, inventory_item ii WHERE so.sales_order_uuid = $1 AND ii.inventory_item_uuid = $2`, uuid, item)
		require.NoError(t, err)
	}
	makeUnit := func() string {
		unit, err := inventory.CreateUnit(ctx, pool, inventory.CreateUnitInput{Serial: testActionUUID(), InventoryItemUUID: item, WarehouseID: 1, LengthMM: 2000, WidthMM: 1000, ThicknessMM: 30}, 1)
		require.NoError(t, err)
		require.NoError(t, inventory.InspectUnit(ctx, pool, unit.ID, inventory.InspectionInput{Decision: "accepted"}, 1))
		return unit.ID
	}
	ownSlab, othersSlab := makeUnit(), makeUnit()
	require.NoError(t, inventory.HoldUnitForSalesOrder(ctx, pool, orderUUID, ownSlab, 1))
	require.NoError(t, inventory.HoldUnitForSalesOrder(ctx, pool, otherOrderUUID, othersSlab, 1))

	line := TemplateLine{SourceLineID: testActionUUID(), MaterialID: item, Pieces: []MeasuredPiece{{Name: "Island", LengthMM: 1500, WidthMM: 600, ThicknessMM: 30}}}
	raw, err := json.Marshal([]TemplateLine{line})
	require.NoError(t, err)
	var revision string
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO fabrication_template_revision(fabrication_job_id,revision,sales_order_version,state,baseline,measured_lines,change_summary) SELECT fabrication_job_id,1,1,'approved','[]',$2,'{}' FROM fabrication_job WHERE fabrication_job_uuid=$1 RETURNING template_uuid`, job.ID, raw).Scan(&revision))
	actor := ActionActor{IdentityID: testActionUUID(), EmployeeID: 1, AllScope: true}
	allocate := func(slab string, version int64) (ActionResult, error) {
		return allocateMaterial(ctx, pool, job.ID, actor, AllocateMaterialInput{
			CommandMeta: CommandMeta{ExpectedVersion: version, RequestID: testActionUUID()},
			SlabID:      slab, TemplateID: revision, SourceLineID: line.SourceLineID,
			Layout: MaterialLayout{Placements: []PiecePlacement{{PieceIndex: 0}}, KerfMM: 3, SuitabilityConfirmed: true, ReviewNote: "Grain and defects reviewed"},
		})
	}

	// Another order's hold stays off limits, and the refusal changes nothing.
	_, err = allocate(othersSlab, job.Version)
	require.Error(t, err)
	got, err := inventory.GetUnit(ctx, pool, othersSlab)
	require.NoError(t, err)
	require.Equal(t, "reserved", got.Status)
	require.NotNil(t, got.ReservedSalesOrderID)
	require.Equal(t, otherOrderUUID, *got.ReservedSalesOrderID)

	// This order's own hold is taken over.
	_, err = allocate(ownSlab, job.Version)
	require.NoError(t, err)
	got, err = inventory.GetUnit(ctx, pool, ownSlab)
	require.NoError(t, err)
	require.Equal(t, "reserved", got.Status)
	require.Nil(t, got.ReservedSalesOrderID, "the hold is handed to the job, not left on the order")
	require.Equal(t, job.ID, got.Usage.JobID, "the job's allocation now names the slab")
}
