//go:build dbtest

package inventory

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// unitIDByCode finds a seeded lkp_unit row, so the tests do not hardcode ids.
func unitIDByCode(t *testing.T, pool *pgxpool.Pool, code string) int {
	t.Helper()
	var id int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT unit_id FROM lkp_unit WHERE unit_code = $1`, code).Scan(&id))
	return id
}

func itemInUnit(t *testing.T, pool *pgxpool.Pool, code, tracking string) CreateItemInput {
	in := itemInputIn("")
	in.UnitID = unitIDByCode(t, pool, code)
	in.Tracking = tracking
	return in
}

func cleanupItem(t *testing.T, pool *pgxpool.Pool, uuid string) {
	t.Helper()
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM inventory_item WHERE inventory_item_uuid::text = $1`, uuid)
	})
}

// A square unit is how the shop tells a slab from anything else, so an item
// priced per sq ft is slab-tracked whatever tracking the form sent.
func TestCreate_AnAreaUnitMakesTheItemSlabTracked(t *testing.T) {
	pool := testPool(t)

	got, err := Create(context.Background(), pool, itemInUnit(t, pool, "SQFT", TrackingQuantity), 1)

	require.NoError(t, err)
	cleanupItem(t, pool, got.ID)
	require.Equal(t, TrackingSerialized, got.Tracking)
	require.True(t, got.TrackStock, "a slab item always has stock to run out of")
}

func TestCreate_ANonAreaUnitIsCountedByQuantity(t *testing.T) {
	pool := testPool(t)

	got, err := Create(context.Background(), pool, itemInUnit(t, pool, "EA", TrackingSerialized), 1)

	require.NoError(t, err)
	cleanupItem(t, pool, got.ID)
	require.Equal(t, TrackingQuantity, got.Tracking, "only an area unit can back a slab item")
}

func TestUpdate_SwitchingToAnAreaUnitMakesTheItemSlabTracked(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	created, err := Create(ctx, pool, itemInUnit(t, pool, "EA", TrackingQuantity), 1)
	require.NoError(t, err)
	cleanupItem(t, pool, created.ID)

	in := itemInUnit(t, pool, "SQM", TrackingQuantity)
	in.SKU, in.Name = created.SKU, created.Name
	require.NoError(t, Update(ctx, pool, created.ID, in, 1))

	got, err := Get(ctx, pool, created.ID)
	require.NoError(t, err)
	require.Equal(t, TrackingSerialized, got.Tracking)
}

func TestUpdate_RefusesToDropTheAreaUnitOfAnItemThatHasSlabs(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	created, err := Create(ctx, pool, itemInUnit(t, pool, "SQFT", TrackingSerialized), 1)
	require.NoError(t, err)
	cleanupItem(t, pool, created.ID)
	seedSlabFor(t, pool, created.ID)

	in := itemInUnit(t, pool, "EA", TrackingQuantity)
	in.SKU, in.Name = created.SKU, created.Name
	err = Update(ctx, pool, created.ID, in, 1)

	require.Error(t, err)
	require.True(t, IsClientError(err), "%v", err)
	got, err := Get(ctx, pool, created.ID)
	require.NoError(t, err)
	require.Equal(t, TrackingSerialized, got.Tracking, "the refused write changed nothing")
}

// seedSlabFor puts one slab on hand for the item, so the unit it is measured in
// can no longer be changed out from under it.
func seedSlabFor(t *testing.T, pool *pgxpool.Pool, itemUUID string) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `
		INSERT INTO inventory_slab (slab_serial, inventory_item_id, warehouse_id,
		                            slab_length_mm, slab_width_mm, slab_thickness_mm, slab_area, slab_area_unit_id)
		SELECT $2, inventory_item_id, 1, 3048, 1524, 30, 50, inventory_item_unit_id
		FROM inventory_item WHERE inventory_item_uuid::text = $1`, itemUUID, uniq("DBT-SLAB"))
	require.NoError(t, err)
}
