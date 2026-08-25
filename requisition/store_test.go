// requisition/store_test.go
//go:build dbtest

package requisition

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/query"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB-backed test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// seedVendorAndItem inserts a minimal live vendor + inventory_item, mirroring
// purchaseorder/store_test.go's seedVendorAndItem.
func seedVendorAndItem(t *testing.T, pool *pgxpool.Pool) (vendorUUID, itemUUID string) {
	t.Helper()
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())

	var vndrTypeID, activeStatusID int
	if err := pool.QueryRow(ctx, `SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'VNDR'`).Scan(&vndrTypeID); err != nil {
		t.Fatalf("resolve VNDR record type: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		SELECT record_status_id FROM lkp_record_status
		WHERE record_status_record_type = $1 AND record_status_code = 'ACT_'`, vndrTypeID).Scan(&activeStatusID); err != nil {
		t.Fatalf("resolve vendor ACT_ status: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO vendor (record_type, vendor_status, vendor_type, vendor_legal_name, vendor_created_by)
		VALUES ($1, $2, 'Organization', $3, 1) RETURNING vendor_uuid`,
		vndrTypeID, activeStatusID, "Test Vendor "+suffix).Scan(&vendorUUID); err != nil {
		t.Fatalf("seed vendor: %v", err)
	}

	if err := pool.QueryRow(ctx, `
		INSERT INTO inventory_item (inventory_item_sku, inventory_item_name, inventory_item_unit_id, inventory_item_unit_price, inventory_item_created_by)
		VALUES ($1, $2, 1, 25.00, 1) RETURNING inventory_item_uuid`,
		"REQNSKU-"+suffix, "Test Requisition Item "+suffix).Scan(&itemUUID); err != nil {
		t.Fatalf("seed inventory item: %v", err)
	}
	return vendorUUID, itemUUID
}

func TestCreate_SnapshotsAndTotals(t *testing.T) {
	pool := testPool(t)
	_, itemUUID := seedVendorAndItem(t, pool)

	in := CreateRequisitionInput{requisitionFields{
		SalesTaxPercent: 8,
		Items: []LineInput{
			{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 2},
		},
	}}
	got, err := Create(context.Background(), pool, in, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got.Number == "" || got.Number[:5] != "REQN-" {
		t.Errorf("Number = %q, want REQN- prefix", got.Number)
	}
	if got.StatusCode != "DRFT" {
		t.Errorf("StatusCode = %q, want DRFT", got.StatusCode)
	}
	if got.Priority != "normal" {
		t.Errorf("Priority = %q, want normal (default)", got.Priority)
	}
	if got.RequestedByEmployeeID != 1 {
		t.Errorf("RequestedByEmployeeID = %d, want 1 (defaulted from actor)", got.RequestedByEmployeeID)
	}
	if len(got.Items) != 1 {
		t.Fatalf("len(Items) = %d, want 1", len(got.Items))
	}
	// unit price snapshotted from inventory_item (25.00) since request left EstimatedUnitPrice at 0.
	if got.Items[0].EstimatedUnitPrice != 25 {
		t.Errorf("Items[0].EstimatedUnitPrice = %v, want 25", got.Items[0].EstimatedUnitPrice)
	}
	// subtotal = 2*25=50, tax = 50*0.08=4, estimated total = 54
	if got.EstimatedTotal != 54 {
		t.Errorf("EstimatedTotal = %v, want 54", got.EstimatedTotal)
	}
}

func TestCreate_NoVendorRequired(t *testing.T) {
	pool := testPool(t)
	got, err := Create(context.Background(), pool, CreateRequisitionInput{requisitionFields{
		Items: []LineInput{{LineNumber: 1, Description: "Something not in the catalog yet", Quantity: 1, EstimatedUnitPrice: 100}},
	}}, 1)
	if err != nil {
		t.Fatalf("Create with no vendor: %v", err)
	}
	if got.Vendor != nil {
		t.Errorf("Vendor = %+v, want nil (AD-2: vendor is optional)", got.Vendor)
	}
}

func TestCreate_RequiresAtLeastOneLine(t *testing.T) {
	pool := testPool(t)
	_, err := Create(context.Background(), pool, CreateRequisitionInput{}, 1)
	if !IsClientError(err) {
		t.Fatalf("Create with no items = %v, want ClientError", err)
	}
}

func TestCreate_RejectsInvalidPriority(t *testing.T) {
	pool := testPool(t)
	_, itemUUID := seedVendorAndItem(t, pool)
	_, err := Create(context.Background(), pool, CreateRequisitionInput{requisitionFields{
		Priority: "urgentish",
		Items:    []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}},
	}}, 1)
	if !IsClientError(err) {
		t.Fatalf("Create with invalid priority = %v, want ClientError", err)
	}
}

func TestUpdate_OnlyDraftEditable(t *testing.T) {
	pool := testPool(t)
	_, itemUUID := seedVendorAndItem(t, pool)
	ctx := context.Background()

	created, err := Create(ctx, pool, CreateRequisitionInput{requisitionFields{
		Items: []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}},
	}}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Draft update recomputes totals.
	updated, err := Update(ctx, pool, created.ID, UpdateRequisitionInput{requisitionFields{
		Items: []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 3}},
	}}, 1)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.EstimatedTotal != 75 {
		t.Errorf("EstimatedTotal after update = %v, want 75", updated.EstimatedTotal)
	}

	// Past draft, update is rejected (AD-12).
	if _, err := Transition(ctx, pool, created.ID, "PAPV", 1); err != nil {
		t.Fatalf("Transition to PAPV: %v", err)
	}
	_, err = Update(ctx, pool, created.ID, UpdateRequisitionInput{requisitionFields{
		Items: []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 5}},
	}}, 1)
	if !IsClientError(err) {
		t.Fatalf("Update at PAPV = %v, want ClientError", err)
	}
}

func TestSoftDelete_GuardedByStatus(t *testing.T) {
	pool := testPool(t)
	_, itemUUID := seedVendorAndItem(t, pool)
	ctx := context.Background()

	created, err := Create(ctx, pool, CreateRequisitionInput{requisitionFields{
		Items: []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}},
	}}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// A submitted requisition cannot be deleted (AD-11)...
	if _, err := Transition(ctx, pool, created.ID, "PAPV", 1); err != nil {
		t.Fatalf("Transition to PAPV: %v", err)
	}
	if err := SoftDelete(ctx, pool, created.ID, 1); !IsClientError(err) {
		t.Fatalf("SoftDelete at PAPV = %v, want ClientError", err)
	}

	// ...but a draft can.
	if _, err := Transition(ctx, pool, created.ID, "DRFT", 1); err != nil {
		t.Fatalf("Transition back to DRFT: %v", err)
	}
	if err := SoftDelete(ctx, pool, created.ID, 1); err != nil {
		t.Fatalf("SoftDelete at DRFT: %v", err)
	}
	if _, err := Get(ctx, pool, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after delete = %v, want ErrNotFound", err)
	}
}

func TestTransition_RejectsIllegalMove(t *testing.T) {
	pool := testPool(t)
	_, itemUUID := seedVendorAndItem(t, pool)

	created, err := Create(context.Background(), pool, CreateRequisitionInput{requisitionFields{
		Items: []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}},
	}}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := Transition(context.Background(), pool, created.ID, "APPV", 1); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Transition DRFT->APPV = %v, want ErrInvalidTransition", err)
	}
}

func TestApprove_RequiresConfiguredApprover(t *testing.T) {
	pool := testPool(t)
	_, itemUUID := seedVendorAndItem(t, pool)
	ctx := context.Background()

	created, err := Create(ctx, pool, CreateRequisitionInput{requisitionFields{
		Items: []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}},
	}}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := Transition(ctx, pool, created.ID, "PAPV", 1); err != nil {
		t.Fatalf("Transition to PAPV: %v", err)
	}
	// No requisition_approver rows configured for (REQN, PAPV) in this test DB
	// by default, so Approve should report the status doesn't require approval.
	if _, err := Approve(ctx, pool, created.ID, 1, false); !errors.Is(err, ErrApprovalNotRequired) {
		t.Fatalf("Approve with no configured approvers = %v, want ErrApprovalNotRequired", err)
	}
}

func TestApprove_SignOffFlipsApprovalStatusAndGatesTransition(t *testing.T) {
	pool := testPool(t)
	_, itemUUID := seedVendorAndItem(t, pool)
	ctx := context.Background()

	created, err := Create(ctx, pool, CreateRequisitionInput{requisitionFields{
		Items: []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}},
	}}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	var recordTypeID, papvStatusID int
	if err := pool.QueryRow(ctx, `SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'REQN'`).Scan(&recordTypeID); err != nil {
		t.Fatalf("resolve REQN record type: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT record_status_id FROM lkp_record_status WHERE record_status_record_type = $1 AND record_status_code = 'PAPV'`, recordTypeID).Scan(&papvStatusID); err != nil {
		t.Fatalf("resolve PAPV status: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO requisition_approver (record_type_id, record_status_id, approver_employee_id)
		VALUES ($1, $2, 1) ON CONFLICT DO NOTHING`, recordTypeID, papvStatusID); err != nil {
		t.Fatalf("seed requisition_approver: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM requisition_approver WHERE record_type_id = $1 AND record_status_id = $2`, recordTypeID, papvStatusID)
	})

	if _, err := Transition(ctx, pool, created.ID, "PAPV", 1); err != nil {
		t.Fatalf("Transition to PAPV: %v", err)
	}
	// Gate: PAPV -> APPV blocked until the sign-off lands.
	if _, err := Transition(ctx, pool, created.ID, "APPV", 1); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("Transition PAPV->APPV before approval = %v, want ErrApprovalRequired", err)
	}
	// Approve auto-advances the requisition straight to APPV once quorum is
	// met (requisition/approval.go's finalizeApproval) -- no separate
	// Transition call is needed or possible afterward, since the record is
	// no longer at PAPV.
	approved, err := Approve(ctx, pool, created.ID, 1, false)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if approved.ApprovalStatus != "approved" {
		t.Errorf("ApprovalStatus = %q, want approved", approved.ApprovalStatus)
	}
	if approved.StatusCode != "APPV" {
		t.Errorf("StatusCode = %q, want APPV", approved.StatusCode)
	}
}

func TestSearch_ReturnsCreatedRequisition(t *testing.T) {
	pool := testPool(t)
	_, itemUUID := seedVendorAndItem(t, pool)
	ctx := context.Background()

	created, err := Create(ctx, pool, CreateRequisitionInput{requisitionFields{
		Items: []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}},
	}}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	page, err := Search(ctx, pool, "all", "", query.Request{Search: created.Number})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	found := false
	for _, r := range page.Records {
		if r.ID == created.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("Search(%q) did not include the created requisition", created.Number)
	}
}

// TestSoftDelete_UnresolvedActor is the regression for the module-wide
// soft-delete defect (see purchaseorder's identical test): resolveEmployeeID
// returns 0 whenever the caller has no linked employee row, and binding that
// through nullableInt would write SQL NULL into requisition_deleted_by,
// which chk_reqn_soft_delete rejects.
func TestSoftDelete_UnresolvedActor(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	_, itemUUID := seedVendorAndItem(t, pool)

	created, err := Create(ctx, pool, CreateRequisitionInput{requisitionFields{
		Items: []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}},
	}}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := SoftDelete(ctx, pool, created.ID, 0); err != nil {
		t.Fatalf("SoftDelete with unresolved actor: %v", err)
	}
	var deletedBy int
	if err := pool.QueryRow(ctx,
		`SELECT requisition_deleted_by FROM requisition WHERE requisition_uuid = $1`, created.ID,
	).Scan(&deletedBy); err != nil {
		t.Fatalf("read requisition_deleted_by: %v", err)
	}
	if deletedBy != systemEmployeeID {
		t.Errorf("requisition_deleted_by = %d, want %d (system employee)", deletedBy, systemEmployeeID)
	}
}
