package replenishment

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/inventory"
)

func TestValidateDraftInput(t *testing.T) {
	ok := []VendorQty{{VendorID: "v1", Qty: 25}, {VendorID: "v2", Qty: 15}}
	tests := []struct {
		name    string
		in      DraftInput
		wantMsg string // "" = valid
	}{
		{"valid split across two vendors", DraftInput{InventoryItemID: "i", Vendors: ok}, ""},
		{"no item", DraftInput{Vendors: ok}, "Choose an item to order."},
		{"no vendors", DraftInput{InventoryItemID: "i"}, "Choose at least one vendor."},
		{"a blank vendor", DraftInput{InventoryItemID: "i", Vendors: []VendorQty{{Qty: 5}}}, "Choose a vendor for every line."},
		{"the same vendor twice",
			DraftInput{InventoryItemID: "i", Vendors: []VendorQty{{VendorID: "v1", Qty: 5}, {VendorID: " v1 ", Qty: 5}}},
			"Each vendor can only appear once for an item."},
		{"zero quantity", DraftInput{InventoryItemID: "i", Vendors: []VendorQty{{VendorID: "v1"}}},
			"Enter a quantity between 1 and 100000 for every vendor."},
		{"negative quantity", DraftInput{InventoryItemID: "i", Vendors: []VendorQty{{VendorID: "v1", Qty: -3}}},
			"Enter a quantity between 1 and 100000 for every vendor."},
		{"an absurd quantity", DraftInput{InventoryItemID: "i", Vendors: []VendorQty{{VendorID: "v1", Qty: 1e9}}},
			"Enter a quantity between 1 and 100000 for every vendor."},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := validateDraftInput(tc.in)
			if tc.wantMsg == "" {
				require.NoError(t, err)
				return
			}
			var ce ClientError
			require.ErrorAs(t, err, &ce)
			assert.Equal(t, tc.wantMsg, ce.Msg)
		})
	}

	t.Run("more vendors than the cap", func(t *testing.T) {
		vendors := make([]VendorQty, MaxDraftVendors+1)
		for i := range vendors {
			vendors[i] = VendorQty{VendorID: string(rune('a' + i)), Qty: 1}
		}
		var ce ClientError
		require.ErrorAs(t, validateDraftInput(DraftInput{InventoryItemID: "i", Vendors: vendors}), &ce)
	})
}

func TestDraftItemLine(t *testing.T) {
	t.Run("a quantity-tracked item orders its own unit", func(t *testing.T) {
		line, err := draftItem{tracking: inventory.TrackingQuantity}.line("item-1", 12.5)
		require.NoError(t, err)
		assert.Equal(t, 12.5, line.Quantity)
		assert.Nil(t, line.ExpectedSlabs)
		assert.Nil(t, line.Length)
	})

	t.Run("a slab item with a stated size carries it, so the area is derived", func(t *testing.T) {
		// 3000 x 1500 x 20 mm in inches.
		line, err := draftItem{
			tracking: inventory.TrackingSerialized, dimensionUnit: "in",
			lengthMM: 3000, widthMM: 1500, thicknessMM: 20,
		}.line("item-1", 4)
		require.NoError(t, err)
		require.NotNil(t, line.ExpectedSlabs)
		assert.Equal(t, 4, *line.ExpectedSlabs)
		require.NotNil(t, line.Length)
		require.NotNil(t, line.Width)
		require.NotNil(t, line.Thickness)
		assert.Equal(t, 118.11, *line.Length)
		assert.Equal(t, 59.06, *line.Width)
		assert.Equal(t, 0.79, *line.Thickness)
		assert.Equal(t, "in", line.DimensionUnit)
		assert.Zero(t, line.Quantity, "the server derives the area, so none is sent")
	})

	t.Run("a stated size without a thickness leaves the thickness out", func(t *testing.T) {
		line, err := draftItem{
			tracking: inventory.TrackingSerialized, dimensionUnit: "mm", lengthMM: 3000, widthMM: 1500,
		}.line("item-1", 1)
		require.NoError(t, err)
		assert.Nil(t, line.Thickness)
		assert.Equal(t, 3000.0, *line.Length)
	})

	t.Run("no stated size falls back to slabs times the average slab area", func(t *testing.T) {
		line, err := draftItem{tracking: inventory.TrackingSerialized, avgSlabArea: 48.5}.line("item-1", 3)
		require.NoError(t, err)
		assert.Equal(t, 145.5, line.Quantity)
		assert.Equal(t, 3, *line.ExpectedSlabs)
		assert.Nil(t, line.Length)
	})

	t.Run("no stated size and no slabs on record falls back to the slab count", func(t *testing.T) {
		line, err := draftItem{tracking: inventory.TrackingSerialized}.line("item-1", 3)
		require.NoError(t, err)
		assert.Equal(t, 3.0, line.Quantity)
	})

	t.Run("a part-slab quantity is refused", func(t *testing.T) {
		_, err := draftItem{tracking: inventory.TrackingSerialized}.line("item-1", 2.5)
		var ce ClientError
		require.ErrorAs(t, err, &ce)
		assert.Equal(t, "Enter a whole number of slabs.", ce.Msg)
	})
}
