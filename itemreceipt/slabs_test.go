package itemreceipt

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/inventory"
)

var (
	sqftItem = inventory.ItemUnitInfo{ItemID: 1, UnitID: 1, UnitCode: inventory.UnitCodeSquareFoot, UnitCategory: inventory.UnitCategoryArea, Tracking: inventory.TrackingSerialized}
	sqmItem  = inventory.ItemUnitInfo{ItemID: 2, UnitID: 2, UnitCode: inventory.UnitCodeSquareM, UnitCategory: inventory.UnitCategoryArea, Tracking: inventory.TrackingSerialized}
	eaItem   = inventory.ItemUnitInfo{ItemID: 3, UnitID: 3, UnitCode: "EA", UnitCategory: inventory.UnitCategoryCount, Tracking: inventory.TrackingSerialized}
)

func TestComputeSlabs(t *testing.T) {
	// 3048 x 1524 mm is exactly 10 ft x 5 ft = 50 sq ft.
	const tenByFiveFeet = 50.0

	tests := []struct {
		name      string
		item      inventory.ItemUnitInfo
		in        []SlabInput
		wantAreas []float64
		wantTotal float64
		wantErr   string
	}{
		{
			name:      "one slab in square feet",
			item:      sqftItem,
			in:        []SlabInput{{LengthMM: 3048, WidthMM: 1524, ThicknessMM: 30}},
			wantAreas: []float64{tenByFiveFeet},
			wantTotal: tenByFiveFeet,
		},
		{
			name:      "two slabs total their areas",
			item:      sqftItem,
			in:        []SlabInput{{LengthMM: 3048, WidthMM: 1524, ThicknessMM: 30}, {LengthMM: 1524, WidthMM: 1524, ThicknessMM: 20}},
			wantAreas: []float64{tenByFiveFeet, 25},
			wantTotal: 75,
		},
		{
			name:      "square metres",
			item:      sqmItem,
			in:        []SlabInput{{LengthMM: 3000, WidthMM: 1500, ThicknessMM: 20}},
			wantAreas: []float64{4.5},
			wantTotal: 4.5,
		},
		{
			name:      "optional attributes are trimmed and carried",
			item:      sqftItem,
			in:        []SlabInput{{LengthMM: 3048, WidthMM: 1524, ThicknessMM: 30, BlockID: " B7 ", Lot: " L2 ", Grade: " A ", SupplierCode: " S-1 "}},
			wantAreas: []float64{tenByFiveFeet},
			wantTotal: tenByFiveFeet,
		},
		{name: "no slabs", item: sqftItem, in: nil, wantErr: "at least one slab"},
		{name: "count unit cannot be measured", item: eaItem, in: []SlabInput{{LengthMM: 3048, WidthMM: 1524, ThicknessMM: 30}}, wantErr: "area unit"},
		{name: "zero length", item: sqftItem, in: []SlabInput{{LengthMM: 0, WidthMM: 1524, ThicknessMM: 30}}, wantErr: "greater than zero"},
		{name: "negative width", item: sqftItem, in: []SlabInput{{LengthMM: 3048, WidthMM: -1, ThicknessMM: 30}}, wantErr: "greater than zero"},
		{name: "zero thickness", item: sqftItem, in: []SlabInput{{LengthMM: 3048, WidthMM: 1524, ThicknessMM: 0}}, wantErr: "greater than zero"},
		{name: "absurd dimension", item: sqftItem, in: []SlabInput{{LengthMM: maxSlabDimensionMM + 1, WidthMM: 1524, ThicknessMM: 30}}, wantErr: "cannot exceed"},
		{name: "too small to measure", item: sqftItem, in: []SlabInput{{LengthMM: 1, WidthMM: 1, ThicknessMM: 1}}, wantErr: "too small"},
		{
			name:    "lot too long",
			item:    sqftItem,
			in:      []SlabInput{{LengthMM: 3048, WidthMM: 1524, ThicknessMM: 30, Lot: strings.Repeat("x", maxSlabTextLen+1)}},
			wantErr: "Lot cannot exceed",
		},
		{
			name:    "supplier code too long",
			item:    sqftItem,
			in:      []SlabInput{{LengthMM: 3048, WidthMM: 1524, ThicknessMM: 30, SupplierCode: strings.Repeat("x", maxSupplierCodeLen+1)}},
			wantErr: "supplier code cannot exceed",
		},
		{
			name: "supplier code repeated case-insensitively",
			item: sqftItem,
			in: []SlabInput{
				{LengthMM: 3048, WidthMM: 1524, ThicknessMM: 30, SupplierCode: "abc"},
				{LengthMM: 3048, WidthMM: 1524, ThicknessMM: 30, SupplierCode: "ABC"},
			},
			wantErr: "used on two slabs",
		},
		{
			name: "blank supplier codes may repeat",
			item: sqftItem,
			in: []SlabInput{
				{LengthMM: 3048, WidthMM: 1524, ThicknessMM: 30},
				{LengthMM: 3048, WidthMM: 1524, ThicknessMM: 30},
			},
			wantAreas: []float64{tenByFiveFeet, tenByFiveFeet},
			wantTotal: 100,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, total, err := computeSlabs(1, tc.item, tc.in)
			if tc.wantErr != "" {
				require.Error(t, err)
				assert.True(t, IsClientError(err), "must be a client error, got %T", err)
				assert.Contains(t, err.Error(), tc.wantErr)
				return
			}
			require.NoError(t, err)
			require.Len(t, got, len(tc.wantAreas))
			for i, want := range tc.wantAreas {
				assert.InDelta(t, want, got[i].area, 1e-9, "slab %d area", i+1)
			}
			assert.InDelta(t, tc.wantTotal, total, 1e-9)
		})
	}

	t.Run("attributes are trimmed", func(t *testing.T) {
		got, _, err := computeSlabs(1, sqftItem, []SlabInput{{
			LengthMM: 3048, WidthMM: 1524, ThicknessMM: 30, BlockID: " B7 ", Lot: " L2 ", Grade: " A ", SupplierCode: " S-1 ",
		}})
		require.NoError(t, err)
		assert.Equal(t, "B7", got[0].blockID)
		assert.Equal(t, "L2", got[0].lot)
		assert.Equal(t, "A", got[0].grade)
		assert.Equal(t, "S-1", got[0].supplierCode)
	})

	t.Run("errors name the line and slab", func(t *testing.T) {
		_, _, err := computeSlabs(4, sqftItem, []SlabInput{
			{LengthMM: 3048, WidthMM: 1524, ThicknessMM: 30},
			{LengthMM: 0, WidthMM: 1524, ThicknessMM: 30},
		})
		require.Error(t, err)
		assert.Contains(t, err.Error(), "Line 4, slab 2")
	})
}

func TestSlabSerial(t *testing.T) {
	tests := []struct {
		prefix string
		n      int
		want   string
	}{
		{"PORD-000012-", 1, "PORD-000012-001"},
		{"PORD-000012-", 42, "PORD-000012-042"},
		{"PORD-000012-", 999, "PORD-000012-999"},
		{"PORD-000012-", 1000, "PORD-000012-1000"}, // grows past the padding rather than wrapping
	}
	for _, tc := range tests {
		assert.Equal(t, tc.want, slabSerial(tc.prefix, tc.n))
	}
}

func TestSerialPrefix(t *testing.T) {
	t.Run("builds from the order number", func(t *testing.T) {
		got, err := serialPrefix("PORD-000012")
		require.NoError(t, err)
		assert.Equal(t, "PORD-000012-", got)
	})
	t.Run("trims whitespace", func(t *testing.T) {
		got, err := serialPrefix("  PORD-7 ")
		require.NoError(t, err)
		assert.Equal(t, "PORD-7-", got)
	})
	t.Run("blank number is a client error", func(t *testing.T) {
		for _, blank := range []string{"", "   "} {
			_, err := serialPrefix(blank)
			require.Error(t, err)
			assert.True(t, IsClientError(err))
		}
	})
	t.Run("a number too long for the serial column is a client error", func(t *testing.T) {
		_, err := serialPrefix(strings.Repeat("P", maxSerialLen))
		require.Error(t, err)
		assert.True(t, IsClientError(err))
	})
	t.Run("the longest accepted number still fits a full serial", func(t *testing.T) {
		po := strings.Repeat("P", maxSerialLen-serialSuffixDigits-1)
		prefix, err := serialPrefix(po)
		require.NoError(t, err)
		assert.LessOrEqual(t, len(slabSerial(prefix, 1)), maxSerialLen)
	})
}

func TestRequireExplicitWarehouse(t *testing.T) {
	slabLine := []resolvedLine{{slabs: []resolvedSlab{{}}}}
	bulkLine := []resolvedLine{{}}
	five, zero := 5, 0

	tests := []struct {
		name    string
		lines   []resolvedLine
		wh      *int
		wantErr bool
	}{
		{"slabs with no warehouse", slabLine, nil, true},
		{"slabs with a zero warehouse", slabLine, &zero, true},
		{"slabs with a warehouse", slabLine, &five, false},
		{"bulk line may rely on the default", bulkLine, nil, false},
		{"bulk line with a warehouse", bulkLine, &five, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := requireExplicitWarehouse(tc.lines, tc.wh)
			if tc.wantErr {
				require.Error(t, err)
				assert.True(t, IsClientError(err))
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestFromInventoryErr(t *testing.T) {
	assert.NoError(t, fromInventoryErr(nil))

	got := fromInventoryErr(inventory.ClientError{Msg: "Unknown bin."})
	assert.True(t, IsClientError(got), "an inventory client error must surface as this package's")
	assert.Equal(t, "Unknown bin.", got.Error())

	plain := errors.New("connection reset")
	assert.Same(t, plain, fromInventoryErr(plain), "non-client errors pass through untouched")
}

// The receiving form sends slabs as JSON; this pins the wire shape both ways so
// a renamed tag on either side fails here, not in the browser.
func TestSlabPayloadWireShape(t *testing.T) {
	t.Run("decodes what the form sends", func(t *testing.T) {
		const body = `{
			"purchaseOrderUuid": "po-1",
			"post": true,
			"warehouseId": 3,
			"items": [{
				"lineNumber": 1,
				"purchaseOrderItemUuid": "poi-1",
				"qtyReceived": 100,
				"slabs": [
					{"lengthMm": 3048, "widthMm": 1524, "thicknessMm": 30, "binId": "bin-uuid", "blockId": "B7", "lot": "L2", "grade": "A", "supplierCode": "S-1"},
					{"lengthMm": 3048, "widthMm": 1524, "thicknessMm": 30}
				]
			}]
		}`
		var in CreateItemReceiptInput
		require.NoError(t, json.Unmarshal([]byte(body), &in))

		require.NotNil(t, in.WarehouseID)
		assert.Equal(t, 3, *in.WarehouseID)
		assert.True(t, in.Post)
		require.Len(t, in.Items, 1)
		require.Len(t, in.Items[0].Slabs, 2)
		assert.Equal(t, SlabInput{
			LengthMM: 3048, WidthMM: 1524, ThicknessMM: 30,
			BinID: "bin-uuid", BlockID: "B7", Lot: "L2", Grade: "A", SupplierCode: "S-1",
		}, in.Items[0].Slabs[0])
		assert.Equal(t, SlabInput{LengthMM: 3048, WidthMM: 1524, ThicknessMM: 30}, in.Items[0].Slabs[1])
	})

	t.Run("a line without slabs decodes to none", func(t *testing.T) {
		var in CreateItemReceiptInput
		require.NoError(t, json.Unmarshal([]byte(`{"items":[{"lineNumber":1,"purchaseOrderItemUuid":"x","qtyReceived":4}]}`), &in))
		assert.Empty(t, in.Items[0].Slabs)
	})

	t.Run("encodes what the form reads", func(t *testing.T) {
		bin, unit := "bin-uuid", "unit-uuid"
		raw, err := json.Marshal(Line{Slabs: []LineSlab{{
			Serial: "PORD-000012-001", LengthMM: 3048, WidthMM: 1524, ThicknessMM: 30, Area: 50,
			BinID: &bin, BinPath: "YARD-A", BlockID: "B7", Lot: "L2", Grade: "A", SupplierCode: "S-1",
			UnitID: &unit, UnitStatus: "available",
		}}})
		require.NoError(t, err)

		var got struct {
			Slabs []map[string]any `json:"slabs"`
		}
		require.NoError(t, json.Unmarshal(raw, &got))
		require.Len(t, got.Slabs, 1)
		want := map[string]any{
			"serial": "PORD-000012-001", "lengthMm": 3048.0, "widthMm": 1524.0, "thicknessMm": 30.0, "area": 50.0,
			"binId": "bin-uuid", "binPath": "YARD-A", "blockId": "B7", "lot": "L2", "grade": "A", "supplierCode": "S-1",
			"unitId": "unit-uuid", "unitStatus": "available",
		}
		assert.Equal(t, want, got.Slabs[0])
	})

	t.Run("a line with no slabs omits the key", func(t *testing.T) {
		raw, err := json.Marshal(Line{})
		require.NoError(t, err)
		assert.NotContains(t, string(raw), "slabs")
	})

	t.Run("the serial preview uses the keys the form reads", func(t *testing.T) {
		raw, err := json.Marshal(SlabSequence{Prefix: "PORD-000012-", Next: 4})
		require.NoError(t, err)
		assert.JSONEq(t, `{"prefix":"PORD-000012-","next":4}`, string(raw))
	})
}
