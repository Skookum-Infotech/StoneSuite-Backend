// purchaseorder/store_slabs_test.go — the optional expected slab count on a slab line.
//go:build dbtest

package purchaseorder

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeSlabItem turns a seeded catalogue item into a slab-tracked one in square
// feet, the only kind of item an expected slab count applies to.
func makeSlabItem(t *testing.T, itemUUID string) {
	t.Helper()
	pool := testPool(t)
	_, err := pool.Exec(context.Background(), `
		UPDATE inventory_item
		SET inventory_item_unit_id = (SELECT unit_id FROM lkp_unit WHERE unit_code = 'SQFT'),
		    inventory_item_tracking = 'serialized'
		WHERE inventory_item_uuid = $1`, itemUUID)
	require.NoError(t, err)
}

func slabOrder(vendorUUID string, lines ...LineInput) CreatePurchaseOrderInput {
	return CreatePurchaseOrderInput{
		VendorUUID:          vendorUUID,
		purchaseOrderFields: purchaseOrderFields{Items: lines},
	}
}

func TestCreate_ExpectedSlabsRoundTrip(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	vendorUUID, itemUUID := seedVendorAndItem(t, pool)
	makeSlabItem(t, itemUUID)

	got, err := Create(ctx, pool, slabOrder(vendorUUID, LineInput{
		LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 600, ExpectedSlabs: intPtr(12),
	}), 1)
	require.NoError(t, err)
	require.Len(t, got.Items, 1)
	line := got.Items[0]
	require.NotNil(t, line.ExpectedSlabs)
	assert.Equal(t, 12, *line.ExpectedSlabs)
	assert.Equal(t, 600.0, line.Quantity, "the quantity stays the area that drives pricing")
	assert.Equal(t, "serialized", line.Tracking)
	assert.Zero(t, line.SlabsReceived, "nothing has arrived yet")

	// Editing the order can change it or clear it.
	updated, err := Update(ctx, pool, got.ID, UpdatePurchaseOrderInput{
		purchaseOrderFields: purchaseOrderFields{Items: []LineInput{{
			LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 650, ExpectedSlabs: intPtr(13),
		}}},
	}, 1)
	require.NoError(t, err)
	require.NotNil(t, updated.Items[0].ExpectedSlabs)
	assert.Equal(t, 13, *updated.Items[0].ExpectedSlabs)

	cleared, err := Update(ctx, pool, got.ID, UpdatePurchaseOrderInput{
		purchaseOrderFields: purchaseOrderFields{Items: []LineInput{{
			LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 650,
		}}},
	}, 1)
	require.NoError(t, err)
	assert.Nil(t, cleared.Items[0].ExpectedSlabs)
}

func TestCreate_ExpectedSlabsIsOptionalOnASlabLine(t *testing.T) {
	pool := testPool(t)
	vendorUUID, itemUUID := seedVendorAndItem(t, pool)
	makeSlabItem(t, itemUUID)

	got, err := Create(context.Background(), pool, slabOrder(vendorUUID, LineInput{
		LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 600,
	}), 1)
	require.NoError(t, err)
	assert.Nil(t, got.Items[0].ExpectedSlabs)
}

func TestCreate_ExpectedSlabsRefusedWhereItMeansNothing(t *testing.T) {
	pool := testPool(t)
	vendorUUID, itemUUID := seedVendorAndItem(t, pool) // a plain quantity item

	tests := []struct {
		name string
		line LineInput
	}{
		{"a quantity-tracked item", LineInput{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 5, ExpectedSlabs: intPtr(2)}},
		{"a free-text line", LineInput{LineNumber: 1, Description: "Sealer", Quantity: 5, ExpectedSlabs: intPtr(2)}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Create(context.Background(), pool, slabOrder(vendorUUID, tc.line), 1)
			require.Error(t, err)
			assert.True(t, IsClientError(err), "must be a 400, got %T: %v", err, err)
			assert.Contains(t, err.Error(), "slab-tracked")
		})
	}
}

func TestCreate_ExpectedSlabsMustBePositive(t *testing.T) {
	pool := testPool(t)
	vendorUUID, itemUUID := seedVendorAndItem(t, pool)
	makeSlabItem(t, itemUUID)

	_, err := Create(context.Background(), pool, slabOrder(vendorUUID, LineInput{
		LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 600, ExpectedSlabs: intPtr(0),
	}), 1)
	require.Error(t, err)
	assert.True(t, IsClientError(err))
}
