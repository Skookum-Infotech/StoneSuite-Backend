//go:build dbtest

package vendorbill

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// vendorOfPO returns the uuid of the vendor a seeded purchase order was placed with.
func vendorOfPO(t *testing.T, pool *pgxpool.Pool, poUUID string) string {
	t.Helper()
	var v string
	if err := pool.QueryRow(context.Background(), `
		SELECT v.vendor_uuid::text FROM purchase_order po JOIN vendor v ON v.vendor_id = po.purchase_order_vendor_id
		WHERE po.purchase_order_uuid = $1`, poUUID).Scan(&v); err != nil {
		t.Fatalf("load the order's vendor: %v", err)
	}
	return v
}

// poItemUUID returns the uuid of a seeded purchase order line.
func poItemUUID(t *testing.T, pool *pgxpool.Pool, lineID int) string {
	t.Helper()
	var u string
	if err := pool.QueryRow(context.Background(),
		`SELECT purchase_order_item_uuid::text FROM purchase_order_item WHERE purchase_order_item_id = $1`, lineID).Scan(&u); err != nil {
		t.Fatalf("load po item uuid: %v", err)
	}
	return u
}

func linkedInput(vendorUUID, poUUID, invoice string, items ...LineInput) CreateVendorBillInput {
	return CreateVendorBillInput{
		VendorUUID:        vendorUUID,
		PurchaseOrderUUID: poUUID,
		vendorBillFields:  vendorBillFields{VendorInvoiceNumber: invoice, Items: items},
	}
}

func freeLine(n int) LineInput {
	return LineInput{LineNumber: n, Description: "Freight", Quantity: 1, UnitPrice: 40}
}

func TestCreate_LinkedLinesRecordConversionAtBillQuantities(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	poUUID, ids := seedPurchaseOrder(t, pool, "RCVD", []poLineSeed{{10, 10, 25}})
	in := linkedInput(vendorOfPO(t, pool, poUUID), poUUID, "",
		LineInput{LineNumber: 1, PurchaseOrderItemID: poItemUUID(t, pool, ids[0]), Description: "Slab", Quantity: 4, UnitPrice: 30},
		freeLine(2))
	bill, err := Create(ctx, pool, in, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if bill.Items[0].PurchaseOrderItemID == nil || bill.Items[1].PurchaseOrderItemID != nil {
		t.Errorf("lineage = %v / %v, want linked then unlinked", bill.Items[0].PurchaseOrderItemID, bill.Items[1].PurchaseOrderItemID)
	}
	if got := billedOn(t, pool, poUUID); got[0] != 4 {
		t.Errorf("qtyBilled = %v, want [4] at the bill's quantity", got)
	}
	// 6 remain: another linked bill for 7 is over-billing, 6 is fine.
	over := linkedInput(vendorOfPO(t, pool, poUUID), poUUID, "", LineInput{LineNumber: 1, PurchaseOrderItemID: poItemUUID(t, pool, ids[0]), Description: "Slab", Quantity: 7, UnitPrice: 30})
	if _, err := Create(ctx, pool, over, 1); err == nil || !IsClientError(err) {
		t.Fatalf("over-bill err = %v, want ClientError", err)
	}
	over.Items[0].Quantity = 6
	if _, err := Create(ctx, pool, over, 1); err != nil {
		t.Fatalf("billing the remaining 6: %v", err)
	}
}

func TestCreate_LinkedLinesAreValidated(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	poUUID, ids := seedPurchaseOrder(t, pool, "RCVD", []poLineSeed{{10, 10, 25}})
	otherPO, otherIDs := seedPurchaseOrder(t, pool, "RCVD", []poLineSeed{{10, 10, 25}})
	sentPO, sentIDs := seedPurchaseOrder(t, pool, "SENT", []poLineSeed{{10, 0, 25}})
	line := func(id int) LineInput {
		return LineInput{LineNumber: 1, PurchaseOrderItemID: poItemUUID(t, pool, id), Description: "Slab", Quantity: 1, UnitPrice: 30}
	}
	cases := []struct {
		name string
		in   CreateVendorBillInput
	}{
		{"line from another PO", linkedInput(vendorOfPO(t, pool, poUUID), poUUID, "", line(otherIDs[0]))},
		{"PO of another vendor", linkedInput(vendorOfPO(t, pool, otherPO), poUUID, "", line(ids[0]))},
		{"non-billable PO", linkedInput(vendorOfPO(t, pool, sentPO), sentPO, "", line(sentIDs[0]))},
		{"link without a PO", linkedInput(vendorOfPO(t, pool, poUUID), "", "", line(ids[0]))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Create(ctx, pool, tc.in, 1); err == nil || !IsClientError(err) {
				t.Fatalf("err = %v, want ClientError", err)
			}
		})
	}
	for _, u := range []string{poUUID, otherPO, sentPO} {
		for _, b := range billedOn(t, pool, u) {
			if b != 0 {
				t.Errorf("rejected creates billed %v on %s", b, u)
			}
		}
	}
}

func TestCreate_DuplicateInvoiceNumber(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	vendor := seedVendor(t, pool)
	first, err := Create(ctx, pool, linkedInput(vendor, "", "INV-0042", freeLine(1)), 1)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	_, err = Create(ctx, pool, linkedInput(vendor, "", "inv 42", freeLine(1)), 1)
	var d *DuplicateDocumentError
	if !errors.As(err, &d) || d.ExistingUUID != first.ID || d.ExistingNumber != first.Number {
		t.Fatalf("err = %v, want DuplicateDocumentError of %s", err, first.ID)
	}
	// Another vendor and a blank number are never duplicates.
	if _, err := Create(ctx, pool, linkedInput(seedVendor(t, pool), "", "INV-0042", freeLine(1)), 1); err != nil {
		t.Errorf("same number, other vendor: %v", err)
	}
	for i := 0; i < 2; i++ {
		if _, err := Create(ctx, pool, linkedInput(vendor, "", "", freeLine(1)), 1); err != nil {
			t.Errorf("blank invoice number #%d: %v", i, err)
		}
	}
	// A deleted bill no longer blocks.
	if err := SoftDelete(ctx, pool, first.ID, 1); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := Create(ctx, pool, linkedInput(vendor, "", "INV-0042", freeLine(1)), 1); err != nil {
		t.Errorf("after delete: %v", err)
	}
}

func TestCreate_AllowDuplicateFlagsOverride(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	vendor := seedVendor(t, pool)
	first, err := Create(ctx, pool, linkedInput(vendor, "", "A-1", freeLine(1)), 1)
	if err != nil {
		t.Fatalf("first: %v", err)
	}
	if first.DuplicateOverride {
		t.Error("first create must not flag an override")
	}
	in := linkedInput(vendor, "", "a1", freeLine(1))
	in.AllowDuplicate = true
	second, err := Create(ctx, pool, in, 1)
	if err != nil {
		t.Fatalf("second with AllowDuplicate: %v", err)
	}
	if !second.DuplicateOverride || second.DuplicateOfUUID != first.ID {
		t.Errorf("override = %v of %q, want true of %q", second.DuplicateOverride, second.DuplicateOfUUID, first.ID)
	}
}

func TestCreate_ConcurrentDuplicateInvoice_ExactlyOneWins(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	vendor := seedVendor(t, pool)
	nums := []string{"INV-0099", "inv 99"}
	errs := make([]error, len(nums))
	var wg sync.WaitGroup
	for i := range nums {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, errs[i] = Create(ctx, pool, linkedInput(vendor, "", nums[i], freeLine(1)), 1)
		}()
	}
	wg.Wait()
	var ok, dups int
	for _, err := range errs {
		var d *DuplicateDocumentError
		switch {
		case err == nil:
			ok++
		case errors.As(err, &d):
			dups++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}
	if ok != 1 || dups != 1 {
		t.Fatalf("ok=%d dups=%d, want 1 and 1", ok, dups)
	}
}

// A catalog line keeps the catalog description unless the caller overrides it.
func TestCreate_CatalogLineKeepsDescription(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	var itemUUID string
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	if err := pool.QueryRow(ctx, `
		INSERT INTO inventory_item (inventory_item_sku, inventory_item_name, inventory_item_description, inventory_item_unit_id, inventory_item_unit_price, inventory_item_created_by)
		VALUES ($1, $2, 'Catalog description', 1, 25.00, 1) RETURNING inventory_item_uuid::text`,
		"VB-SKU-"+suffix, "VB Item "+suffix).Scan(&itemUUID); err != nil {
		t.Fatalf("seed inventory item: %v", err)
	}
	vendor := seedVendor(t, pool)
	bill, err := Create(ctx, pool, linkedInput(vendor, "", "",
		LineInput{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1},
		LineInput{LineNumber: 2, InventoryItemUUID: itemUUID, Description: "Custom words", Quantity: 1},
	), 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got := bill.Items[0].Description; got != "Catalog description" {
		t.Errorf("catalog line description = %q, want the catalog description", got)
	}
	if got := bill.Items[1].Description; got != "Custom words" {
		t.Errorf("overridden description = %q, want %q", got, "Custom words")
	}
}
