//go:build dbtest

package invoice

import (
	"context"
	"strings"
	"testing"

	"stonesuite-backend/salesorder"
)

// The kit label is not looked up, so any well-formed uuid will do.
const kitUUID = "7d0c1f2e-4a3b-4c5d-8e6f-0123456789ab"

func TestCreate_StoresTheKitALineWasAddedFrom(t *testing.T) {
	pool := testPool(t)
	custUUID, itemUUID := seedCustomerAndItem(t, pool)

	in := CreateInvoiceInput{
		CustomerUUID: custUUID,
		Items: []InvoiceLineInput{
			{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1, UnitPrice: 10, KitUUID: kitUUID, KitName: " Kitchen "},
			{LineNumber: 2, InventoryItemUUID: itemUUID, Quantity: 1, UnitPrice: 10},
		},
	}
	got, err := Create(context.Background(), pool, in, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if l := got.Items[0]; l.KitID == nil || *l.KitID != kitUUID || l.KitName != "Kitchen" {
		t.Errorf("line 1 kit = (%v, %q), want (%s, Kitchen)", l.KitID, l.KitName, kitUUID)
	}
	if l := got.Items[1]; l.KitID != nil || l.KitName != "" {
		t.Errorf("line 2 kit = (%v, %q), want none", l.KitID, l.KitName)
	}
}

func TestCreate_RefusesAMalformedKitLabel(t *testing.T) {
	pool := testPool(t)
	custUUID, itemUUID := seedCustomerAndItem(t, pool)

	in := CreateInvoiceInput{
		CustomerUUID: custUUID,
		Items:        []InvoiceLineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1, UnitPrice: 10, KitUUID: "kitchen", KitName: "Kitchen"}},
	}
	_, err := Create(context.Background(), pool, in, 1)
	if err == nil || !strings.Contains(err.Error(), "Line 1: the kit id is not valid.") {
		t.Fatalf("err = %v, want the kit id refused", err)
	}
}

func TestUpdate_ReplacesTheKitLabelsWithTheSavedLines(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID, itemUUID := seedCustomerAndItem(t, pool)

	inv, err := Create(ctx, pool, CreateInvoiceInput{
		CustomerUUID: custUUID,
		Items:        []InvoiceLineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1, UnitPrice: 10, KitUUID: kitUUID, KitName: "Kitchen"}},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	got, err := Update(ctx, pool, inv.ID, UpdateInvoiceInput{
		Items: []InvoiceLineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1, UnitPrice: 10}},
	}, 1)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if l := got.Items[0]; l.KitID != nil || l.KitName != "" {
		t.Errorf("the saved line should have left its kit, got (%v, %q)", l.KitID, l.KitName)
	}
}

// Sales Order → Invoice copies each line verbatim, kit heading included.
func TestConvertFromSalesOrder_CarriesTheKitHeading(t *testing.T) {
	pool := testPool(t)
	custUUID, itemUUID := seedCustomerAndItem(t, pool)
	if _, err := pool.Exec(context.Background(), `
		INSERT INTO inventory_stock (inventory_item_id, warehouse_id, quantity_on_hand)
		SELECT inventory_item_id, 1, 1000 FROM inventory_item WHERE inventory_item_uuid = $1
		ON CONFLICT (inventory_item_id, warehouse_id) DO UPDATE SET quantity_on_hand = 1000`, itemUUID); err != nil {
		t.Fatalf("give item stock: %v", err)
	}

	in := salesorder.CreateOrderInput{CustomerUUID: custUUID}
	in.Items = []salesorder.LineInput2{
		{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1, KitUUID: kitUUID, KitName: "Kitchen"},
		{LineNumber: 2, InventoryItemUUID: itemUUID, Quantity: 1},
	}
	order, err := salesorder.Create(context.Background(), pool, in, 1)
	if err != nil {
		t.Fatalf("seed sales order: %v", err)
	}
	setSalesOrderStatus(t, pool, order.ID, "OPEN")

	got, _, err := ConvertFromSalesOrder(context.Background(), pool, order.ID, 1)
	if err != nil {
		t.Fatalf("ConvertFromSalesOrder: %v", err)
	}
	if l := got.Items[0]; l.KitID == nil || *l.KitID != kitUUID || l.KitName != "Kitchen" {
		t.Errorf("invoice line 1 lost its kit: (%v, %q)", l.KitID, l.KitName)
	}
	if l := got.Items[1]; l.KitID != nil {
		t.Errorf("invoice line 2 gained a kit: %v", *l.KitID)
	}
}
