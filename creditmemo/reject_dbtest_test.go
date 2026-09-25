//go:build dbtest

package creditmemo

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/approvalchain"
)

// configureApprover makes employee 1 the sole configured approver at the DRFT
// gate (Credit Memo has no separate pending status) for the rest of the test.
func configureApprover(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	var recordTypeID, draftStatusID int
	if err := pool.QueryRow(ctx, `SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'CRDT'`).Scan(&recordTypeID); err != nil {
		t.Fatalf("resolve CRDT record type: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT record_status_id FROM lkp_record_status WHERE record_status_record_type = $1 AND record_status_code = 'DRFT'`, recordTypeID).Scan(&draftStatusID); err != nil {
		t.Fatalf("resolve DRFT status: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO credit_memo_approver (record_type_id, record_status_id, approver_employee_id)
		VALUES ($1, $2, 1) ON CONFLICT DO NOTHING`, recordTypeID, draftStatusID); err != nil {
		t.Fatalf("seed credit_memo_approver: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM credit_memo_approver WHERE record_type_id = $1 AND record_status_id = $2`, recordTypeID, draftStatusID)
	})
}

// gatedCreditMemo creates a Draft credit memo waiting on its approver.
func gatedCreditMemo(t *testing.T, pool *pgxpool.Pool) *CreditMemo {
	t.Helper()
	cm, err := Create(context.Background(), pool, CreateCreditMemoInput{
		CustomerUUID: seedCustomer(t, pool),
		Reason:       "Returned goods",
		Lines:        []CreditMemoLineInput{{LineNumber: 1, Description: "Returned slab", Quantity: 1, UnitPrice: 50}},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return cm
}

func TestReject_FlagsTheRecordInPlaceAndBlocksApproving(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	configureApprover(t, pool)
	cm := gatedCreditMemo(t, pool)

	info, err := GetApprovalInfo(ctx, pool, cm.ID, 1, false)
	if err != nil {
		t.Fatalf("GetApprovalInfo: %v", err)
	}
	if !info.Gated || !info.CanApprove || !info.CanReject {
		t.Fatalf("info = %+v before rejecting, want the memo gated and open to its approver", info)
	}

	rejected, err := Reject(ctx, pool, cm.ID, 1, false, "Wrong customer")
	if err != nil {
		t.Fatalf("Reject: %v", err)
	}
	if rejected.StatusCode != "DRFT" {
		t.Errorf("status = %q, want it to stay DRFT (nothing earlier to return to)", rejected.StatusCode)
	}
	info, err = GetApprovalInfo(ctx, pool, cm.ID, 1, false)
	if err != nil {
		t.Fatalf("GetApprovalInfo after reject: %v", err)
	}
	if !info.Gated {
		t.Error("a rejected memo stays gated so its status transitions stay blocked")
	}
	if info.CanApprove || info.CanReject {
		t.Errorf("CanApprove=%v CanReject=%v, want neither until the memo is edited", info.CanApprove, info.CanReject)
	}
	if info.Rejection == nil || info.Rejection.Reason != "Wrong customer" {
		t.Fatalf("Rejection = %+v, want the reason", info.Rejection)
	}

	// Neither approving nor moving on is possible while it is rejected.
	if _, err := Approve(ctx, pool, cm.ID, 1, false); !errors.Is(err, approvalchain.ErrAlreadyRejected) {
		t.Errorf("Approve of a rejected memo = %v, want ErrAlreadyRejected", err)
	}
	if _, err := Transition(ctx, pool, cm.ID, "APPV", 1); !errors.Is(err, ErrApprovalRequired) {
		t.Errorf("Transition of a rejected memo to APPV = %v, want ErrApprovalRequired", err)
	}
	// Void is always a way out.
	if voided, err := Transition(ctx, pool, cm.ID, "VOID", 1); err != nil || voided.StatusCode != "VOID" {
		t.Errorf("Transition to VOID = %+v, %v; want a rejected memo to remain voidable", voided, err)
	}
}

func TestUpdate_ResubmitsARejectedCreditMemo(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	configureApprover(t, pool)
	cm := gatedCreditMemo(t, pool)
	if _, err := Reject(ctx, pool, cm.ID, 1, false, "Wrong customer"); err != nil {
		t.Fatalf("Reject: %v", err)
	}

	if _, err := Update(ctx, pool, cm.ID, UpdateCreditMemoInput{Reason: "Returned goods", Memo: "Customer corrected"}, 1); err != nil {
		t.Fatalf("Update: %v", err)
	}
	info, err := GetApprovalInfo(ctx, pool, cm.ID, 1, false)
	if err != nil {
		t.Fatalf("GetApprovalInfo: %v", err)
	}
	if info.Rejection != nil || !info.Gated || !info.CanApprove || !info.CanReject {
		t.Fatalf("info = %+v, want the memo reopened for approval with no rejection", info)
	}
	approved, err := Approve(ctx, pool, cm.ID, 1, false)
	if err != nil {
		t.Fatalf("Approve after the edit: %v", err)
	}
	if approved.StatusCode != "APPV" {
		t.Errorf("status = %q, want APPV", approved.StatusCode)
	}
}

func TestReject_RequiresConfiguredApprover(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	configureApprover(t, pool)
	cm := gatedCreditMemo(t, pool)

	if _, err := Reject(ctx, pool, cm.ID, 999999, false, "not mine to reject"); !errors.Is(err, ErrNotApprover) {
		t.Fatalf("Reject by a non-approver = %v, want ErrNotApprover", err)
	}
	if _, err := Reject(ctx, pool, "00000000-0000-0000-0000-000000000000", 1, false, "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Reject of a missing memo = %v, want ErrNotFound", err)
	}
	info, err := GetApprovalInfo(ctx, pool, cm.ID, 1, false)
	if err != nil {
		t.Fatalf("GetApprovalInfo: %v", err)
	}
	if info.Rejection != nil || !info.CanReject {
		t.Errorf("info = %+v, want the memo untouched by the refused rejects", info)
	}
}
