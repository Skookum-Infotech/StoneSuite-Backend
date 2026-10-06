//go:build dbtest

package vendorbill

import (
	"context"
	"strings"
	"testing"
)

// editLines re-submits a converted bill's lines with the given quantity per
// line, keeping each line's purchase order link as the client would.
func editLines(t *testing.T, bill *VendorBill, qty float64) UpdateVendorBillInput {
	t.Helper()
	items := make([]LineInput, len(bill.Items))
	for i, l := range bill.Items {
		desc := l.Description
		if desc == "" {
			desc = "edited line"
		}
		in := LineInput{LineNumber: l.LineNumber, Description: desc, Quantity: qty, UnitPrice: l.UnitPrice}
		if l.PurchaseOrderItemID != nil {
			in.PurchaseOrderItemID = *l.PurchaseOrderItemID
		}
		items[i] = in
	}
	return UpdateVendorBillInput{vendorBillFields: vendorBillFields{BillDate: "2026-08-01", Items: items}}
}

// Editing a converted draft from 10 to 4 keeps the PO link and lowers what the
// order reports as billed.
func TestUpdate_ConvertedBillKeepsLinkAndReleasesReducedQuantity(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	poUUID, _ := seedPurchaseOrder(t, pool, "RCVD", []poLineSeed{{10, 10, 25}})
	bill, err := ConvertFromPurchaseOrder(ctx, pool, poUUID, 1)
	if err != nil {
		t.Fatalf("conversion: %v", err)
	}
	if bill.Items[0].PurchaseOrderItemID == nil {
		t.Fatal("converted line has no purchaseOrderItemId")
	}

	updated, err := Update(ctx, pool, bill.ID, editLines(t, bill, 4), 1)
	if err != nil {
		t.Fatalf("Update 10 -> 4: %v", err)
	}
	if updated.Items[0].PurchaseOrderItemID == nil || *updated.Items[0].PurchaseOrderItemID != *bill.Items[0].PurchaseOrderItemID {
		t.Errorf("link after edit = %v, want %v", updated.Items[0].PurchaseOrderItemID, *bill.Items[0].PurchaseOrderItemID)
	}
	if got := billedOn(t, pool, poUUID); got[0] != 4 {
		t.Errorf("qtyBilled after 10 -> 4 edit = %v, want [4]", got)
	}

	// The freed 6 can be billed again, and raising this bill back past what
	// the other bill leaves is refused.
	other, err := ConvertFromPurchaseOrder(ctx, pool, poUUID, 1)
	if err != nil {
		t.Fatalf("second conversion: %v", err)
	}
	if other.Items[0].Quantity != 6 {
		t.Fatalf("second bill qty = %v, want 6", other.Items[0].Quantity)
	}
	if _, err := Update(ctx, pool, bill.ID, editLines(t, bill, 5), 1); err == nil || !IsClientError(err) ||
		!strings.Contains(err.Error(), "still billable") {
		t.Fatalf("Update 4 -> 5 with 6 billed elsewhere = %v, want a 'still billable' ClientError", err)
	}
	if got := billedOn(t, pool, poUUID); got[0] != 10 {
		t.Errorf("qtyBilled after the refused edit = %v, want [10]", got)
	}
}

// A line that links to a purchase order the bill was never converted from is
// refused, as is any link on a bill with no conversion.
func TestUpdate_RefusesForeignOrUnconvertedLinks(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	poUUID, _ := seedPurchaseOrder(t, pool, "RCVD", []poLineSeed{{10, 10, 25}})
	otherPO, _ := seedPurchaseOrder(t, pool, "RCVD", []poLineSeed{{10, 10, 25}})
	bill, err := ConvertFromPurchaseOrder(ctx, pool, poUUID, 1)
	if err != nil {
		t.Fatalf("conversion: %v", err)
	}
	foreign, err := ConvertFromPurchaseOrder(ctx, pool, otherPO, 1)
	if err != nil {
		t.Fatalf("conversion of the other order: %v", err)
	}

	in := editLines(t, bill, 4)
	in.Items[0].PurchaseOrderItemID = *foreign.Items[0].PurchaseOrderItemID
	if _, err := Update(ctx, pool, bill.ID, in, 1); err == nil || !IsClientError(err) {
		t.Fatalf("Update with a foreign PO line = %v, want ClientError", err)
	}

	manual, err := Create(ctx, pool, CreateVendorBillInput{
		VendorUUID: seedVendor(t, pool),
		vendorBillFields: vendorBillFields{
			BillDate: "2026-08-01",
			Items:    []LineInput{{LineNumber: 1, Description: "Manual", Quantity: 1, UnitPrice: 5}},
		},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	in = editLines(t, manual, 1)
	in.Items[0].PurchaseOrderItemID = *bill.Items[0].PurchaseOrderItemID
	if _, err := Update(ctx, pool, manual.ID, in, 1); err == nil || !IsClientError(err) {
		t.Fatalf("Update linking a never-converted bill = %v, want ClientError", err)
	}
}
