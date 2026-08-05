// purchaseorder/store_convert_test.go
//go:build dbtest

package purchaseorder

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/requisition"
)

// seedApprovedRequisition creates a live requisition with one catalog-priced
// line and transitions it to APPV (no requisition_approver rows are
// configured for (REQN, PAPV) in this test DB by default, so the approval
// gate is open and the transition needs no sign-off — mirrors
// purchaseorder/store_test.go's TestApprove_RequiresConfiguredApprover).
func seedApprovedRequisition(t *testing.T, pool *pgxpool.Pool, itemUUID string) *requisition.Requisition {
	t.Helper()
	ctx := context.Background()
	in := requisition.CreateRequisitionInput{}
	in.SalesTaxPercent = 8
	in.Items = []requisition.LineInput{
		{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 2},
	}
	reqn, err := requisition.Create(ctx, pool, in, 1)
	if err != nil {
		t.Fatalf("seed requisition: %v", err)
	}
	if _, err := requisition.Transition(ctx, pool, reqn.ID, "PAPV", 1); err != nil {
		t.Fatalf("transition requisition to PAPV: %v", err)
	}
	approved, err := requisition.Transition(ctx, pool, reqn.ID, "APPV", 1)
	if err != nil {
		t.Fatalf("transition requisition to APPV: %v", err)
	}
	return approved
}

func TestConvertFromRequisition_CopiesLinesAndTotals(t *testing.T) {
	pool := testPool(t)
	vendorUUID, itemUUID := seedVendorAndItem(t, pool)
	reqn := seedApprovedRequisition(t, pool, itemUUID)

	got, created, err := ConvertFromRequisition(context.Background(), pool, reqn.ID, vendorUUID, 1)
	if err != nil {
		t.Fatalf("ConvertFromRequisition: %v", err)
	}
	if !created {
		t.Errorf("created = false on first conversion, want true")
	}
	if got.Vendor.ID != vendorUUID {
		t.Errorf("Vendor.ID = %q, want %q", got.Vendor.ID, vendorUUID)
	}
	if len(got.Items) != 1 {
		t.Fatalf("len(Items) = %d, want 1", len(got.Items))
	}
	// unit price snapshotted from inventory_item (25.00) since the requisition left EstimatedUnitPrice at 0.
	if got.Items[0].UnitPrice != 25 {
		t.Errorf("Items[0].UnitPrice = %v, want 25", got.Items[0].UnitPrice)
	}
	// subtotal = 2*25=50, tax = 50*0.08=4, grand = 54 — must match the requisition's totals exactly
	// since lines are copied verbatim, not re-priced.
	if got.GrandTotal != reqn.EstimatedTotal {
		t.Errorf("GrandTotal = %v, want %v (requisition's estimated total)", got.GrandTotal, reqn.EstimatedTotal)
	}
	if got.StatusCode != "DRFT" {
		t.Errorf("StatusCode = %q, want DRFT", got.StatusCode)
	}
}

func TestConvertFromRequisition_IsIdempotent(t *testing.T) {
	pool := testPool(t)
	vendorUUID, itemUUID := seedVendorAndItem(t, pool)
	reqn := seedApprovedRequisition(t, pool, itemUUID)

	first, created1, err := ConvertFromRequisition(context.Background(), pool, reqn.ID, vendorUUID, 1)
	if err != nil {
		t.Fatalf("first ConvertFromRequisition: %v", err)
	}
	if !created1 {
		t.Fatalf("created1 = false, want true")
	}

	second, created2, err := ConvertFromRequisition(context.Background(), pool, reqn.ID, vendorUUID, 1)
	if err != nil {
		t.Fatalf("second ConvertFromRequisition: %v", err)
	}
	if created2 {
		t.Errorf("created2 = true, want false (replay should not create a duplicate)")
	}
	if second.ID != first.ID {
		t.Errorf("second.ID = %q, want %q (same purchase order)", second.ID, first.ID)
	}
}

func TestConvertFromRequisition_RequiresVendor(t *testing.T) {
	pool := testPool(t)
	_, itemUUID := seedVendorAndItem(t, pool)
	reqn := seedApprovedRequisition(t, pool, itemUUID)

	_, _, err := ConvertFromRequisition(context.Background(), pool, reqn.ID, "", 1)
	if !IsClientError(err) {
		t.Fatalf("ConvertFromRequisition with no vendor = %v, want ClientError", err)
	}
}

func TestConvertFromRequisition_RequiresApproved(t *testing.T) {
	pool := testPool(t)
	vendorUUID, itemUUID := seedVendorAndItem(t, pool)
	ctx := context.Background()

	in := requisition.CreateRequisitionInput{}
	in.Items = []requisition.LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}}
	draft, err := requisition.Create(ctx, pool, in, 1)
	if err != nil {
		t.Fatalf("seed draft requisition: %v", err)
	}

	_, _, err = ConvertFromRequisition(ctx, pool, draft.ID, vendorUUID, 1)
	if !IsClientError(err) {
		t.Fatalf("ConvertFromRequisition on a DRFT requisition = %v, want ClientError", err)
	}
}

func TestConvertFromRequisition_UnknownRequisition(t *testing.T) {
	pool := testPool(t)
	vendorUUID, _ := seedVendorAndItem(t, pool)
	_, _, err := ConvertFromRequisition(context.Background(), pool, "00000000-0000-0000-0000-000000000000", vendorUUID, 1)
	if !errors.Is(err, ErrRequisitionNotFound) {
		t.Fatalf("ConvertFromRequisition with unknown uuid: err = %v, want ErrRequisitionNotFound", err)
	}
}
