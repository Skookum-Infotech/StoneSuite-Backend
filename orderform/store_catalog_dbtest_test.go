//go:build dbtest

package orderform

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/inventory"
)

func f64(v float64) *float64 { return &v }

// itemRow is what an order leaves in the catalogue for one item name.
type itemRow struct {
	count      int
	sku        string
	unit       string
	price      float64
	trackStock bool
}

func catalogItem(t *testing.T, pool *pgxpool.Pool, name string) itemRow {
	t.Helper()
	var r itemRow
	err := pool.QueryRow(context.Background(), `
		SELECT COUNT(*) OVER (), i.inventory_item_sku, u.unit_code,
		       i.inventory_item_unit_price::float8, i.inventory_item_track_stock
		FROM inventory_item i JOIN lkp_unit u ON u.unit_id = i.inventory_item_unit_id
		WHERE LOWER(i.inventory_item_name) = LOWER($1) AND i.inventory_item_deleted_at IS NULL
		ORDER BY i.inventory_item_id LIMIT 1`, name).Scan(&r.count, &r.sku, &r.unit, &r.price, &r.trackStock)
	require.NoError(t, err, "item %q should exist", name)
	return r
}

func kitMembers(t *testing.T, pool *pgxpool.Pool, kit string) []string {
	t.Helper()
	rows, err := pool.Query(context.Background(), `
		SELECT i.inventory_item_name
		FROM inventory_kit k
		JOIN inventory_kit_item ki ON ki.inventory_kit_id = k.inventory_kit_id
		JOIN inventory_item i ON i.inventory_item_id = ki.inventory_item_id
		WHERE LOWER(k.inventory_kit_name) = LOWER($1) AND k.inventory_kit_deleted_at IS NULL
		ORDER BY ki.kit_item_position`, kit)
	require.NoError(t, err)
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		require.NoError(t, rows.Scan(&n))
		out = append(out, n)
	}
	require.NoError(t, rows.Err())
	return out
}

func TestCreate_LeavesRealKitsAndItemsForStaffToComplete(t *testing.T) {
	pool := testPool(t)
	who := seedPortalCustomer(t, pool)
	area := uniq("Butler Pantry")
	in := placeable(area)

	_, err := Create(context.Background(), pool, who, in, time.Now().UTC())

	require.NoError(t, err)
	a := in.Areas[0]
	for _, name := range []string{a.Color, a.Edge, a.SinkModel} {
		it := catalogItem(t, pool, name)
		assert.Equal(t, 1, it.count)
		assert.True(t, strings.HasPrefix(it.sku, "PORTAL-"), "sku %q", it.sku)
		assert.Equal(t, "EA", it.unit)
		assert.Equal(t, 0.0, it.price)
		assert.False(t, it.trackStock, "a placeholder is not checked against stock")
	}
	assert.Equal(t, []string{a.Color, a.Edge, a.SinkModel}, kitMembers(t, pool, area))
}

func TestCreate_ReusesAnExistingKitAndItemByName(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	who := seedPortalCustomer(t, pool)
	area := uniq("Hutch Bar")
	in := placeable(area)
	a := &in.Areas[0]

	existing, err := inventory.Create(ctx, pool, inventory.CreateItemInput{
		SKU: uniq("SLAB"), Name: a.Color, UnitID: 1, UnitPrice: f64(50), PurchaseUnitPrice: f64(30),
	}, 1)
	require.NoError(t, err)
	kit, err := inventory.CreateKit(ctx, pool, inventory.KitInput{Name: area}, 1)
	require.NoError(t, err)
	// The customer types it differently; it must still be the same row.
	a.Color = strings.ToUpper(a.Color)
	a.Name = "  " + strings.ToUpper(area) + " "

	res, err := Create(ctx, pool, who, in, time.Now().UTC())

	require.NoError(t, err)
	assert.Empty(t, res.CreatedKits, "the kit already existed")
	assert.Len(t, res.CreatedItems, 2, "only the edge and the sink are new")
	assert.Equal(t, 1, catalogItem(t, pool, existing.Name).count, "no second copy of the item")
	assert.Contains(t, kitMembers(t, pool, kit.Name), existing.Name)

	var soID int
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT so.sales_order_id FROM sales_order so WHERE so.sales_order_uuid = $1`, res.SalesOrderUUID).Scan(&soID))
	var price float64
	var kitName string
	require.NoError(t, pool.QueryRow(ctx, `
		SELECT unit_price::float8, kit_name FROM sales_order_item
		WHERE sales_order_id = $1 AND item_name = $2`, soID, existing.Name).Scan(&price, &kitName))
	assert.Equal(t, 50.0, price, "the reused item's own selling price flows onto the line")
	assert.Equal(t, kit.Name, kitName)
}

func TestCreate_RefusesAnAccountThatIsNotActive_AndWritesNothing(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	who := seedPortalCustomer(t, pool)
	_, err := pool.Exec(ctx, `
		UPDATE customer SET customer_crm_status =
			(SELECT crm_status_id FROM lkp_crm_status WHERE crm_status_code = 'CINA')
		WHERE customer_id = $1`, who.CustomerID)
	require.NoError(t, err)
	in := placeable(uniq("Laundry"))

	_, err = Create(ctx, pool, who, in, time.Now().UTC())

	require.ErrorIs(t, err, ErrCustomerNotActive)
	var forms, items int
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM customer_order_form WHERE customer_id = $1`, who.CustomerID).Scan(&forms))
	require.NoError(t, pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM inventory_item WHERE inventory_item_name = $1`, in.Areas[0].Color).Scan(&items))
	assert.Zero(t, forms)
	assert.Zero(t, items)
}

func TestCreate_WritesNothingWhenThePortalLoginIsUnknown(t *testing.T) {
	pool := testPool(t)
	who := seedPortalCustomer(t, pool)
	who.PortalUserID = "00000000-0000-0000-0000-000000000000" // no such portal login
	in := placeable(uniq("Mudroom"))

	_, err := Create(context.Background(), pool, who, in, time.Now().UTC())

	require.Error(t, err)
	var n int
	require.NoError(t, pool.QueryRow(context.Background(),
		`SELECT COUNT(*) FROM inventory_item WHERE inventory_item_name = $1`, in.Areas[0].Color).Scan(&n))
	assert.Zero(t, n)
}
