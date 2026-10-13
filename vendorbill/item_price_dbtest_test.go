//go:build dbtest

package vendorbill

import (
	"context"
	"fmt"
	"testing"
	"time"
)

// A vendor bill line picked from the catalogue is priced from the item's
// PURCHASING price — what we pay — never its selling price.
func TestCreate_CatalogLineDefaultsToThePurchasingPrice(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	vendorUUID := seedVendor(t, pool)
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())

	var itemUUID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO inventory_item (inventory_item_sku, inventory_item_name, inventory_item_unit_id, inventory_item_unit_price,
			inventory_item_purchase_unit_price, inventory_item_created_by)
		VALUES ($1, $2, 1, 40.00, 25.00, 1) RETURNING inventory_item_uuid::text`,
		"VBPRICE-"+suffix, "Vendor Bill Price Item "+suffix).Scan(&itemUUID); err != nil {
		t.Fatalf("seed inventory item: %v", err)
	}

	bill, err := Create(ctx, pool, CreateVendorBillInput{
		VendorUUID: vendorUUID,
		vendorBillFields: vendorBillFields{
			BillDate: "2026-08-01",
			Items: []LineInput{
				{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 2},
				// An explicit price on the line always wins over the item's.
				{LineNumber: 2, InventoryItemUUID: itemUUID, Quantity: 1, UnitPrice: 31},
			},
		},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if len(bill.Items) != 2 {
		t.Fatalf("Items len = %d, want 2", len(bill.Items))
	}
	if got := bill.Items[0].UnitPrice; got != 25 {
		t.Errorf("defaulted line UnitPrice = %v, want 25 (the purchasing price, not the 40 selling price)", got)
	}
	if got := bill.Items[1].UnitPrice; got != 31 {
		t.Errorf("explicit line UnitPrice = %v, want 31", got)
	}
}
