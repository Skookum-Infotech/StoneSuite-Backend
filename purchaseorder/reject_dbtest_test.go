//go:build dbtest

package purchaseorder

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// configureApprover makes employee 1 the sole configured approver at the PAPV
// gate for the rest of the test.
func configureApprover(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	var recordTypeID, papvStatusID int
	if err := pool.QueryRow(ctx, `SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'PORD'`).Scan(&recordTypeID); err != nil {
		t.Fatalf("resolve PORD record type: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT record_status_id FROM lkp_record_status WHERE record_status_record_type = $1 AND record_status_code = 'PAPV'`, recordTypeID).Scan(&papvStatusID); err != nil {
		t.Fatalf("resolve PAPV status: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO purchase_order_approver (record_type_id, record_status_id, approver_employee_id)
		VALUES ($1, $2, 1) ON CONFLICT DO NOTHING`, recordTypeID, papvStatusID); err != nil {
		t.Fatalf("seed purchase_order_approver: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM purchase_order_approver WHERE record_type_id = $1 AND record_status_id = $2`, recordTypeID, papvStatusID)
	})
}

// pendingOrder creates a purchase order and submits it for approval.
func pendingOrder(t *testing.T, pool *pgxpool.Pool) *PurchaseOrder {
	t.Helper()
	ctx := context.Background()
	vendorUUID, itemUUID := seedVendorAndItem(t, pool)
	created, err := Create(ctx, pool, CreatePurchaseOrderInput{
		VendorUUID:          vendorUUID,
		purchaseOrderFields: purchaseOrderFields{Items: []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}}},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	pending, err := Transition(ctx, pool, created.ID, "PAPV", 1)
	if err != nil {
		t.Fatalf("Transition to PAPV: %v", err)
	}
	return pending
}

func TestReject_SendsBackToDraftAndTheReasonClearsOnResubmit(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	configureApprover(t, pool)
	po := pendingOrder(t, pool)

	rejected, err := Reject(ctx, pool, po.ID, 1, false, "Vendor is wrong")
	if err != nil {
		t.Fatalf("Reject: %v", err)
	}
	if rejected.StatusCode != "DRFT" {
		t.Errorf("status = %q, want DRFT", rejected.StatusCode)
	}
	if rejected.ApprovalStatus != "none" {
		t.Errorf("approval status = %q, want none", rejected.ApprovalStatus)
	}
	info, err := GetApprovalInfo(ctx, pool, po.ID, 1, false)
	if err != nil {
		t.Fatalf("GetApprovalInfo: %v", err)
	}
	if info.Rejection == nil || info.Rejection.Reason != "Vendor is wrong" {
		t.Fatalf("Rejection = %+v, want the reason to be shown on the Draft it was sent back to", info.Rejection)
	}
	if info.Gated {
		t.Error("a Draft order is not gated")
	}

	// Submitting it again is a new round: the old rejection goes away.
	if _, err := Transition(ctx, pool, po.ID, "PAPV", 1); err != nil {
		t.Fatalf("resubmit: %v", err)
	}
	info, err = GetApprovalInfo(ctx, pool, po.ID, 1, false)
	if err != nil {
		t.Fatalf("GetApprovalInfo after resubmit: %v", err)
	}
	if info.Rejection != nil {
		t.Errorf("Rejection = %+v after resubmitting, want it cleared", info.Rejection)
	}
	if !info.Gated || !info.CanApprove || !info.CanReject {
		t.Errorf("info = %+v, want the order gated and open to its approver again", info)
	}

	// Recalling it to Draft is not a rejection: the old reason must not resurface.
	if _, err := Transition(ctx, pool, po.ID, "DRFT", 1); err != nil {
		t.Fatalf("recall: %v", err)
	}
	info, err = GetApprovalInfo(ctx, pool, po.ID, 1, false)
	if err != nil {
		t.Fatalf("GetApprovalInfo after recall: %v", err)
	}
	if info.Rejection != nil {
		t.Errorf("Rejection = %+v after a plain recall to Draft, want none", info.Rejection)
	}
}

func TestReject_RequiresConfiguredApprover(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	configureApprover(t, pool)
	po := pendingOrder(t, pool)

	if _, err := Reject(ctx, pool, po.ID, 999999, false, "not mine to reject"); !errors.Is(err, ErrNotApprover) {
		t.Fatalf("Reject by a non-approver = %v, want ErrNotApprover", err)
	}
	if _, err := Reject(ctx, pool, "00000000-0000-0000-0000-000000000000", 1, false, "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Reject of a missing order = %v, want ErrNotFound", err)
	}
	if got, err := Get(ctx, pool, po.ID); err != nil || got.StatusCode != "PAPV" {
		t.Fatalf("after refused rejects: Get = %+v, %v; want the order still at PAPV", got, err)
	}
}
