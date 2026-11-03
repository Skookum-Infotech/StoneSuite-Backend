//go:build dbtest

package fabrication

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"stonesuite-backend/database"
	"stonesuite-backend/inventory"
)

// TestSoftDelete_RefusedWhenNotDraftOrCancelled covers the status gate.
func TestSoftDelete_RefusedWhenNotDraftOrCancelled(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	job, err := Create(ctx, pool, CreateJobInput{SalesOrderUUID: seedSalesOrder(t, pool)}, 1)
	require.NoError(t, err)

	err = SoftDelete(ctx, pool, job.ID, 1)
	require.Error(t, err)
	require.True(t, IsClientError(err))

	var deleted bool
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT fabrication_job_deleted_at IS NOT NULL FROM fabrication_job WHERE fabrication_job_uuid = $1`, job.ID).Scan(&deleted))
	require.False(t, deleted)
}

// TestSoftDelete_RefusedWithLiveAllocation covers the dependent-row gate: a
// cancelled job that still holds a reserved slab must not be deletable.
func TestSoftDelete_RefusedWithLiveAllocation(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	job := newCancelledJob(t, pool)

	var item string
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO inventory_item(inventory_item_sku,inventory_item_name,inventory_item_unit_id,inventory_item_tracking) SELECT $1,'Stone',unit_id,'serialized' FROM lkp_unit WHERE unit_code='SQFT' RETURNING inventory_item_uuid`, testActionUUID()).Scan(&item))
	unit, err := inventory.CreateUnit(ctx, pool, inventory.CreateUnitInput{Serial: testActionUUID(), InventoryItemUUID: item, WarehouseID: 1, LengthMM: 2000, WidthMM: 1000, ThicknessMM: 30}, 1)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `
		INSERT INTO fabrication_job_slab (fabrication_job_id, inventory_slab_id, allocation_status)
		SELECT fj.fabrication_job_id, s.inventory_slab_id, 'reserved'
		FROM fabrication_job fj, inventory_slab s
		WHERE fj.fabrication_job_uuid = $1 AND s.inventory_slab_uuid = $2`, job.ID, unit.ID)
	require.NoError(t, err)

	require.ErrorIs(t, SoftDelete(ctx, pool, job.ID, 1), ErrJobHasDependents)

	_, err = pool.Exec(ctx, `UPDATE fabrication_job_slab SET allocation_status = 'released'
		WHERE fabrication_job_id = (SELECT fabrication_job_id FROM fabrication_job WHERE fabrication_job_uuid = $1)`, job.ID)
	require.NoError(t, err)
	require.NoError(t, SoftDelete(ctx, pool, job.ID, 1))
}

// TestSchemaUniqueIndexesSkipOnDuplicates proves the appended DO blocks warn and
// skip, rather than fail the boot, when legacy duplicates exist, and create the
// indexes once the data is clean.
func TestSchemaUniqueIndexesSkipOnDuplicates(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	jobA, err := Create(ctx, pool, CreateJobInput{SalesOrderUUID: seedSalesOrder(t, pool)}, 1)
	require.NoError(t, err)
	jobB, err := Create(ctx, pool, CreateJobInput{SalesOrderUUID: seedSalesOrder(t, pool)}, 1)
	require.NoError(t, err)

	_, err = pool.Exec(ctx, `DROP INDEX IF EXISTS uq_fabrication_job_number`)
	require.NoError(t, err)
	dupNumber := "D" + testActionUUID()[:8]
	_, err = pool.Exec(ctx, `UPDATE fabrication_job SET fabrication_job_number = $3 WHERE fabrication_job_uuid IN ($1, $2)`, jobA.ID, jobB.ID, dupNumber)
	require.NoError(t, err)

	indexExists := func() bool {
		var n int
		require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM pg_indexes WHERE indexname = 'uq_fabrication_job_number'`).Scan(&n))
		return n == 1
	}
	_, err = database.ApplyTenantSchema(ctx, pool)
	require.NoError(t, err, "duplicates must not break the schema apply")
	require.False(t, indexExists())

	_, err = pool.Exec(ctx, `UPDATE fabrication_job SET fabrication_job_number = $2 WHERE fabrication_job_uuid = $1`, jobB.ID, "U"+dupNumber[1:])
	require.NoError(t, err)
	_, err = database.ApplyTenantSchema(ctx, pool)
	require.NoError(t, err)
	require.True(t, indexExists())
	_, err = database.ApplyTenantSchema(ctx, pool)
	require.NoError(t, err, "second apply must be a no-op")
}
