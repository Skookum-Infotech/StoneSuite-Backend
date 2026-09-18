// estimate/store_test.go
//go:build dbtest

package estimate

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

// seedCustomerAndItem inserts a minimal live customer + inventory_item,
// mirroring invoice/store_test.go's helper of the same name.
func seedCustomerAndItem(t *testing.T, pool *pgxpool.Pool) (custUUID, itemUUID string) {
	t.Helper()
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())

	var custTypeID int
	if err := pool.QueryRow(ctx, `SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'CUST'`).Scan(&custTypeID); err != nil {
		t.Fatalf("resolve CUST record type: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO customer (record_type, customer_name, customer_created_by)
		VALUES ($1, $2, 1) RETURNING customer_uuid`,
		custTypeID, "Test Customer "+suffix).Scan(&custUUID); err != nil {
		t.Fatalf("seed customer: %v", err)
	}

	if err := pool.QueryRow(ctx, `
		INSERT INTO inventory_item (inventory_item_sku, inventory_item_name, inventory_item_unit_id, inventory_item_unit_price, inventory_item_created_by)
		VALUES ($1, $2, 1, 25.00, 1) RETURNING inventory_item_uuid`,
		"SKU-"+suffix, "Test Item "+suffix).Scan(&itemUUID); err != nil {
		t.Fatalf("seed inventory item: %v", err)
	}
	return custUUID, itemUUID
}

func TestCreate_SnapshotsAndTotals(t *testing.T) {
	pool := testPool(t)
	custUUID, itemUUID := seedCustomerAndItem(t, pool)

	in := CreateEstimateInput{
		CustomerUUID: custUUID,
		estimateFields: estimateFields{
			SalesTaxPercent: 8,
			Items: []LineInput{
				{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 2, DiscountPercent: 0},
			},
		},
	}
	got, err := Create(context.Background(), pool, in, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got.Number == "" || got.Number[:5] != "ESTM-" {
		t.Errorf("Number = %q, want ESTM- prefix", got.Number)
	}
	if got.StatusCode != "DRFT" {
		t.Errorf("StatusCode = %q, want DRFT", got.StatusCode)
	}
	if len(got.Items) != 1 {
		t.Fatalf("len(Items) = %d, want 1", len(got.Items))
	}
	// unit price snapshotted from inventory_item (25.00) since request left UnitPrice at 0.
	if got.Items[0].UnitPrice != 25 {
		t.Errorf("Items[0].UnitPrice = %v, want 25", got.Items[0].UnitPrice)
	}
	// subtotal = 2*25=50, tax = 50*0.08=4, grand = 54
	if got.GrandTotal != 54 {
		t.Errorf("GrandTotal = %v, want 54", got.GrandTotal)
	}
}

func TestCreate_RequiresCustomer(t *testing.T) {
	pool := testPool(t)
	_, err := Create(context.Background(), pool, CreateEstimateInput{}, 1)
	if !IsClientError(err) {
		t.Fatalf("Create with no customer = %v, want ClientError", err)
	}
}

func TestUpdate_RecomputesTotalsAndBumpsVersion(t *testing.T) {
	pool := testPool(t)
	custUUID, itemUUID := seedCustomerAndItem(t, pool)

	created, err := Create(context.Background(), pool, CreateEstimateInput{
		CustomerUUID: custUUID,
		estimateFields: estimateFields{
			SalesTaxPercent: 0,
			Items:           []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}},
		},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	updated, err := Update(context.Background(), pool, created.ID, UpdateEstimateInput{
		estimateFields: estimateFields{
			SalesTaxPercent: 0,
			Items:           []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 3}},
		},
	}, 1)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	// 3 * 25 = 75
	if updated.GrandTotal != 75 {
		t.Errorf("GrandTotal after update = %v, want 75", updated.GrandTotal)
	}
	if len(updated.Items) != 1 || updated.Items[0].Quantity != 3 {
		t.Fatalf("Items after update = %+v, want single line qty 3", updated.Items)
	}
}

func TestSoftDelete_ThenGetReturnsNotFound(t *testing.T) {
	pool := testPool(t)
	custUUID, itemUUID := seedCustomerAndItem(t, pool)

	created, err := Create(context.Background(), pool, CreateEstimateInput{
		CustomerUUID:   custUUID,
		estimateFields: estimateFields{Items: []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}}},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := SoftDelete(context.Background(), pool, created.ID, 1); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	if _, err := Get(context.Background(), pool, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after delete = %v, want ErrNotFound", err)
	}
}

func TestTransition_DraftToPendingApproval(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID, itemUUID := seedCustomerAndItem(t, pool)

	created, err := Create(ctx, pool, CreateEstimateInput{
		CustomerUUID:   custUUID,
		estimateFields: estimateFields{Items: []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}}},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// A genuinely gated submission (an approver configured on PAPV) lands on
	// PAPV and stays there -- contrast with
	// TestTransition_ToCheckpointWithNoApprovers_SkipsStraightToApproved,
	// where zero approvers means the checkpoint is skipped entirely.
	var recordTypeID, papvStatusID int
	if err := pool.QueryRow(ctx, `SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'ESTM'`).Scan(&recordTypeID); err != nil {
		t.Fatalf("resolve ESTM record type: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT record_status_id FROM lkp_record_status WHERE record_status_record_type = $1 AND record_status_code = 'PAPV'`, recordTypeID).Scan(&papvStatusID); err != nil {
		t.Fatalf("resolve PAPV status: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO estimate_approver (record_type_id, record_status_id, approver_employee_id)
		VALUES ($1, $2, 1) ON CONFLICT DO NOTHING`, recordTypeID, papvStatusID); err != nil {
		t.Fatalf("seed estimate_approver: %v", err)
	}

	updated, err := Transition(ctx, pool, created.ID, "PAPV", 1)
	if err != nil {
		t.Fatalf("Transition: %v", err)
	}
	if updated.StatusCode != "PAPV" {
		t.Errorf("StatusCode = %q, want PAPV", updated.StatusCode)
	}
	if updated.ApprovalStatus != "pending" {
		t.Errorf("ApprovalStatus = %q, want pending", updated.ApprovalStatus)
	}
}

func TestTransition_RejectsIllegalMove(t *testing.T) {
	pool := testPool(t)
	custUUID, itemUUID := seedCustomerAndItem(t, pool)

	created, err := Create(context.Background(), pool, CreateEstimateInput{
		CustomerUUID:   custUUID,
		estimateFields: estimateFields{Items: []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}}},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := Transition(context.Background(), pool, created.ID, "APPV", 1); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Transition DRFT->APPV = %v, want ErrInvalidTransition", err)
	}
}

func TestTransition_ToCheckpointWithNoApprovers_SkipsStraightToApproved(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID, itemUUID := seedCustomerAndItem(t, pool)

	// estimate_approver is config-level (keyed by record_type_id/status_id,
	// not per-estimate) and other tests in this package seed rows there
	// without cleaning up -- clear PAPV's explicitly so this test's "zero
	// approvers" premise holds regardless of what ran before it.
	var recordTypeID, papvStatusID int
	if err := pool.QueryRow(ctx, `SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'ESTM'`).Scan(&recordTypeID); err != nil {
		t.Fatalf("resolve ESTM record type: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT record_status_id FROM lkp_record_status WHERE record_status_record_type = $1 AND record_status_code = 'PAPV'`, recordTypeID).Scan(&papvStatusID); err != nil {
		t.Fatalf("resolve PAPV status: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM estimate_approver WHERE record_type_id = $1 AND record_status_id = $2`, recordTypeID, papvStatusID); err != nil {
		t.Fatalf("clear estimate_approver: %v", err)
	}

	created, err := Create(ctx, pool, CreateEstimateInput{
		CustomerUUID:   custUUID,
		estimateFields: estimateFields{Items: []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}}},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// No estimate_approver rows configured for (ESTM, PAPV), so submitting
	// for approval should land straight on APPV rather than parking the
	// estimate on an unresolvable "Pending Approval".
	updated, err := Transition(ctx, pool, created.ID, "PAPV", 1)
	if err != nil {
		t.Fatalf("Transition to PAPV: %v", err)
	}
	if updated.StatusCode != "APPV" {
		t.Errorf("StatusCode = %q, want APPV (checkpoint skipped)", updated.StatusCode)
	}
	if updated.ApprovalStatus != "approved" {
		t.Errorf("ApprovalStatus = %q, want approved", updated.ApprovalStatus)
	}
	// The estimate is no longer gated (it's not even at PAPV anymore), so
	// Approve should report the status doesn't require approval.
	if _, err := Approve(ctx, pool, created.ID, 1, false); !errors.Is(err, ErrApprovalNotRequired) {
		t.Fatalf("Approve after auto-skip = %v, want ErrApprovalNotRequired", err)
	}
}

func TestTransition_DraftStraightToSent_WhenNoApprovers(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID, itemUUID := seedCustomerAndItem(t, pool)

	// Config-level approver rows leak between tests (see the SkipsStraight
	// test above) -- clear PAPV's so "nobody configured to approve" holds.
	var recordTypeID, papvStatusID int
	if err := pool.QueryRow(ctx, `SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'ESTM'`).Scan(&recordTypeID); err != nil {
		t.Fatalf("resolve ESTM record type: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT record_status_id FROM lkp_record_status WHERE record_status_record_type = $1 AND record_status_code = 'PAPV'`, recordTypeID).Scan(&papvStatusID); err != nil {
		t.Fatalf("resolve PAPV status: %v", err)
	}
	if _, err := pool.Exec(ctx, `DELETE FROM estimate_approver WHERE record_type_id = $1 AND record_status_id = $2`, recordTypeID, papvStatusID); err != nil {
		t.Fatalf("clear estimate_approver: %v", err)
	}

	created, err := Create(ctx, pool, CreateEstimateInput{
		CustomerUUID:   custUUID,
		estimateFields: estimateFields{Items: []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}}},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// With the checkpoint unconfigured, Draft's next moves collapse straight
	// past it: Sent and Cancel are offered, neither Pending Approval nor
	// Approved (approvalchain.NextStatusCodes with SkipTargetWhenUngated).
	if got := fmt.Sprint(created.NextStatusCodes); got != "[CANC SENT]" {
		t.Fatalf("NextStatusCodes at DRFT = %s, want [CANC SENT]", got)
	}
	// No attachment is needed to leave Draft (attachments are optional).
	sent, err := Transition(ctx, pool, created.ID, "SENT", 1)
	if err != nil {
		t.Fatalf("Transition DRFT->SENT: %v", err)
	}
	if sent.StatusCode != "SENT" {
		t.Errorf("StatusCode = %q, want SENT (checkpoint and Approved both passed through)", sent.StatusCode)
	}
	if sent.ApprovalStatus != "none" {
		t.Errorf("ApprovalStatus = %q, want none (SENT itself has no gate)", sent.ApprovalStatus)
	}
	if got := fmt.Sprint(sent.NextStatusCodes); got != "[CANC EXPR RJCT]" {
		t.Errorf("NextStatusCodes at SENT = %s, want [CANC EXPR RJCT]", got)
	}
	// Once an approver is configured again, the collapse is gone: Draft
	// offers the checkpoint itself and a direct move to Sent is illegal.
	if _, err := pool.Exec(ctx, `
		INSERT INTO estimate_approver (record_type_id, record_status_id, approver_employee_id)
		VALUES ($1, $2, 1) ON CONFLICT DO NOTHING`, recordTypeID, papvStatusID); err != nil {
		t.Fatalf("seed estimate_approver: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM estimate_approver WHERE record_type_id = $1 AND record_status_id = $2`, recordTypeID, papvStatusID)
	})
	gatedDraft, err := Create(ctx, pool, CreateEstimateInput{
		CustomerUUID:   custUUID,
		estimateFields: estimateFields{Items: []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}}},
	}, 1)
	if err != nil {
		t.Fatalf("Create (gated): %v", err)
	}
	if got := fmt.Sprint(gatedDraft.NextStatusCodes); got != "[CANC PAPV]" {
		t.Fatalf("NextStatusCodes at DRFT with an approver = %s, want [CANC PAPV]", got)
	}
	if _, err := Transition(ctx, pool, gatedDraft.ID, "SENT", 1); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Transition DRFT->SENT with an approver configured = %v, want ErrInvalidTransition", err)
	}
}

func TestApprove_SignOffFlipsApprovalStatus(t *testing.T) {
	pool := testPool(t)
	custUUID, itemUUID := seedCustomerAndItem(t, pool)
	ctx := context.Background()

	created, err := Create(ctx, pool, CreateEstimateInput{
		CustomerUUID:   custUUID,
		estimateFields: estimateFields{Items: []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}}},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// Configure an approver on PAPV *before* transitioning there, so this
	// transition actually lands on the checkpoint (a zero-approver PAPV move
	// now auto-skips straight to APPV -- see
	// TestTransition_ToCheckpointWithNoApprovers_SkipsStraightToApproved).
	var recordTypeID, papvStatusID int
	if err := pool.QueryRow(ctx, `SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'ESTM'`).Scan(&recordTypeID); err != nil {
		t.Fatalf("resolve ESTM record type: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT record_status_id FROM lkp_record_status WHERE record_status_record_type = $1 AND record_status_code = 'PAPV'`, recordTypeID).Scan(&papvStatusID); err != nil {
		t.Fatalf("resolve PAPV status: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO estimate_approver (record_type_id, record_status_id, approver_employee_id)
		VALUES ($1, $2, 1) ON CONFLICT DO NOTHING`, recordTypeID, papvStatusID); err != nil {
		t.Fatalf("seed estimate_approver: %v", err)
	}

	if _, err := Transition(ctx, pool, created.ID, "PAPV", 1); err != nil {
		t.Fatalf("Transition to PAPV: %v", err)
	}

	// Approve auto-advances the estimate straight to APPV once quorum is met
	// (estimate/approval.go's finalizeApproval) -- no separate Transition
	// call is needed or possible afterward, since the record is no longer
	// at PAPV.
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

// A file on the record is never a precondition for submitting it: a Draft with
// zero attachments moves out of Draft like any other.
func TestTransition_AttachmentIsOptional(t *testing.T) {
	pool := testPool(t)
	custUUID, itemUUID := seedCustomerAndItem(t, pool)
	ctx := context.Background()

	created, err := Create(ctx, pool, CreateEstimateInput{
		CustomerUUID:   custUUID,
		estimateFields: estimateFields{Items: []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}}},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := Transition(ctx, pool, created.ID, "PAPV", 1); err != nil {
		t.Fatalf("Transition DRFT->PAPV with no attachments: %v", err)
	}
}

func TestSearch_ReturnsCreatedEstimate(t *testing.T) {
	pool := testPool(t)
	custUUID, itemUUID := seedCustomerAndItem(t, pool)
	ctx := context.Background()

	created, err := Create(ctx, pool, CreateEstimateInput{
		CustomerUUID:   custUUID,
		estimateFields: estimateFields{Items: []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}}},
	}, 1)
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
		t.Errorf("Search(%q) did not include the created estimate", created.Number)
	}
}

// TestSoftDelete_UnresolvedActor is the regression for the module-wide
// soft-delete defect: resolveEmployeeID returns 0 whenever the caller has no
// linked employee row, and binding that through nullableInt wrote SQL NULL
// into estimate_deleted_by, which chk_est_soft_delete rejects.
func TestSoftDelete_UnresolvedActor(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID, itemUUID := seedCustomerAndItem(t, pool)

	created, err := Create(ctx, pool, CreateEstimateInput{
		CustomerUUID:   custUUID,
		estimateFields: estimateFields{Items: []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}}},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := SoftDelete(ctx, pool, created.ID, 0); err != nil {
		t.Fatalf("SoftDelete with unresolved actor: %v", err)
	}
	var deletedBy int
	if err := pool.QueryRow(ctx,
		`SELECT estimate_deleted_by FROM estimate WHERE estimate_uuid = $1`, created.ID,
	).Scan(&deletedBy); err != nil {
		t.Fatalf("read estimate_deleted_by: %v", err)
	}
	if deletedBy != systemEmployeeID {
		t.Errorf("estimate_deleted_by = %d, want %d (system employee)", deletedBy, systemEmployeeID)
	}
}
