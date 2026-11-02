package replenishment

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The web client (src/types/replenishment.ts) reads these exact field names, so
// a rename here would silently blank the Reorder page. This pins the contract.
func TestResultJSONShape(t *testing.T) {
	months := []string{"2026-08", "2026-09", "2026-10"}
	res := Result{Months: months, Items: computeItems([]itemFacts{{
		id: "item-1", sku: "S1", name: "Item", tracking: "serialized", unit: SlabUnit,
		usage: []float64{50, 48, 50}, onHand: 10,
		orders: []OpenOrder{{ID: "po-1", Number: "PORD-1", Status: "Sent", Remaining: 2}},
	}}, months)}

	raw, err := json.Marshal(res)
	require.NoError(t, err)
	var got map[string]any
	require.NoError(t, json.Unmarshal(raw, &got))

	assert.ElementsMatch(t, []any{"2026-08", "2026-09", "2026-10"}, got["months"])
	item := got["items"].([]any)[0].(map[string]any)
	for _, key := range []string{
		"inventoryItemId", "sku", "name", "tracking", "unit", "usage",
		"monthlyRequirement", "onHand", "onOrder", "needed", "openOrders",
	} {
		assert.Contains(t, item, key)
	}
	assert.Equal(t, map[string]any{"month": "2026-08", "used": float64(50)}, item["usage"].([]any)[0])
	assert.Equal(t, map[string]any{"id": "po-1", "number": "PORD-1", "status": "Sent", "remaining": float64(2)},
		item["openOrders"].([]any)[0])
}

// With nothing short the lists are empty arrays, never null, and an item with no
// open orders leaves the field out (the client treats it as absent).
func TestResultJSONEmptyAndAbsent(t *testing.T) {
	raw, err := json.Marshal(Result{Months: []string{"2026-08"}, Items: computeItems(nil, []string{"2026-08"})})
	require.NoError(t, err)
	assert.JSONEq(t, `{"months":["2026-08"],"items":[]}`, string(raw))

	items := computeItems([]itemFacts{{id: "i", usage: []float64{9}, tracking: "quantity", unit: "EA"}}, []string{"2026-08"})
	raw, err = json.Marshal(items[0])
	require.NoError(t, err)
	assert.NotContains(t, string(raw), "openOrders")
}

func TestDraftJSONShape(t *testing.T) {
	var in DraftInput
	require.NoError(t, json.Unmarshal([]byte(`{"inventoryItemId":"i","vendors":[{"vendorId":"v","qty":25}]}`), &in))
	assert.Equal(t, DraftInput{InventoryItemID: "i", Vendors: []VendorQty{{VendorID: "v", Qty: 25}}}, in)

	raw, err := json.Marshal(DraftResult{VendorID: "v", PurchaseOrderID: "po"})
	require.NoError(t, err)
	assert.JSONEq(t, `{"vendorId":"v","purchaseOrderId":"po"}`, string(raw))
}
