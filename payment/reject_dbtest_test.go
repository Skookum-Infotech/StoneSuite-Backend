//go:build dbtest

package payment

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/approvalchain"
)

// configureApprover makes employee 1 the sole configured approver at the PEND
// gate for the rest of the test. It must be in place before Create, or the
// payment auto-skips straight to APPV.
func configureApprover(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	var recordTypeID, pendStatusID int
	if err := pool.QueryRow(ctx, `SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'PYMT'`).Scan(&recordTypeID); err != nil {
		t.Fatalf("resolve PYMT record type: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT record_status_id FROM lkp_record_status WHERE record_status_record_type = $1 AND record_status_code = 'PEND'`, recordTypeID).Scan(&pendStatusID); err != nil {
		t.Fatalf("resolve PEND status: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO payment_approver (record_type_id, record_status_id, approver_employee_id)
		VALUES ($1, $2, 1) ON CONFLICT DO NOTHING`, recordTypeID, pendStatusID); err != nil {
		t.Fatalf("seed payment_approver: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM payment_approver WHERE record_type_id = $1 AND record_status_id = $2`, recordTypeID, pendStatusID)
	})
}

// gatedPayment creates a Pending payment waiting on its approver.
func gatedPayment(t *testing.T, pool *pgxpool.Pool) *Payment {
	t.Helper()
	p, err := Create(context.Background(), pool, CreatePaymentInput{
		CustomerUUID: seedCustomer(t, pool), MethodID: firstMethodID(t, pool), Amount: 500,
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if p.StatusCode != "PEND" {
		t.Fatalf("payment status = %q, want PEND (approver configured)", p.StatusCode)
	}
	return p
}

func TestReject_FlagsTheRecordInPlaceAndBlocksApproving(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	configureApprover(t, pool)
	p := gatedPayment(t, pool)

	info, err := GetApprovalInfo(ctx, pool, p.ID, 1, false)
	if err != nil {
		t.Fatalf("GetApprovalInfo: %v", err)
	}
	if !info.Gated || !info.CanApprove || !info.CanReject {
		t.Fatalf("info = %+v before rejecting, want the payment gated and open to its approver", info)
	}

	rejected, err := Reject(ctx, pool, p.ID, 1, false, "Check bounced")
	if err != nil {
		t.Fatalf("Reject: %v", err)
	}
	if rejected.StatusCode != "PEND" {
		t.Errorf("status = %q, want it to stay PEND (nothing earlier to return to)", rejected.StatusCode)
	}
	info, err = GetApprovalInfo(ctx, pool, p.ID, 1, false)
	if err != nil {
		t.Fatalf("GetApprovalInfo after reject: %v", err)
	}
	if !info.Gated {
		t.Error("a rejected payment stays gated so its status transitions stay blocked")
	}
	if info.CanApprove || info.CanReject {
		t.Errorf("CanApprove=%v CanReject=%v, want neither until the payment is edited", info.CanApprove, info.CanReject)
	}
	if info.Rejection == nil || info.Rejection.Reason != "Check bounced" {
		t.Fatalf("Rejection = %+v, want the reason", info.Rejection)
	}

	// Neither approving nor moving on is possible while it is rejected.
	if _, err := Approve(ctx, pool, p.ID, 1, false); !errors.Is(err, approvalchain.ErrAlreadyRejected) {
		t.Errorf("Approve of a rejected payment = %v, want ErrAlreadyRejected", err)
	}
	if _, err := Transition(ctx, pool, p.ID, "APPV", 1); !errors.Is(err, ErrApprovalRequired) {
		t.Errorf("Transition of a rejected payment to APPV = %v, want ErrApprovalRequired", err)
	}
	// Void is always a way out.
	if voided, err := Transition(ctx, pool, p.ID, "VOID", 1); err != nil || voided.StatusCode != "VOID" {
		t.Errorf("Transition to VOID = %+v, %v; want a rejected payment to remain voidable", voided, err)
	}
}

func TestUpdate_ResubmitsARejectedPayment(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	configureApprover(t, pool)
	p := gatedPayment(t, pool)
	if _, err := Reject(ctx, pool, p.ID, 1, false, "Check bounced"); err != nil {
		t.Fatalf("Reject: %v", err)
	}

	if _, err := Update(ctx, pool, p.ID, UpdatePaymentInput{MethodID: firstMethodID(t, pool), ReferenceNumber: "CHK-2", Memo: "Replacement check"}, 1); err != nil {
		t.Fatalf("Update: %v", err)
	}
	info, err := GetApprovalInfo(ctx, pool, p.ID, 1, false)
	if err != nil {
		t.Fatalf("GetApprovalInfo: %v", err)
	}
	if info.Rejection != nil || !info.Gated || !info.CanApprove || !info.CanReject {
		t.Fatalf("info = %+v, want the payment reopened for approval with no rejection", info)
	}
	approved, err := Approve(ctx, pool, p.ID, 1, false)
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
	p := gatedPayment(t, pool)

	if _, err := Reject(ctx, pool, p.ID, 999999, false, "not mine to reject"); !errors.Is(err, ErrNotApprover) {
		t.Fatalf("Reject by a non-approver = %v, want ErrNotApprover", err)
	}
	if _, err := Reject(ctx, pool, "00000000-0000-0000-0000-000000000000", 1, false, "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Reject of a missing payment = %v, want ErrNotFound", err)
	}
	info, err := GetApprovalInfo(ctx, pool, p.ID, 1, false)
	if err != nil {
		t.Fatalf("GetApprovalInfo: %v", err)
	}
	if info.Rejection != nil || !info.CanReject {
		t.Errorf("info = %+v, want the payment untouched by the refused rejects", info)
	}
}
