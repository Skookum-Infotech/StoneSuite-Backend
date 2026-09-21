//go:build dbtest

package invoice

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
	if err := pool.QueryRow(ctx, `SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'INVC'`).Scan(&recordTypeID); err != nil {
		t.Fatalf("resolve INVC record type: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT record_status_id FROM lkp_record_status WHERE record_status_record_type = $1 AND record_status_code = 'PAPV'`, recordTypeID).Scan(&papvStatusID); err != nil {
		t.Fatalf("resolve PAPV status: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO invoice_approver (record_type_id, record_status_id, approver_employee_id)
		VALUES ($1, $2, 1) ON CONFLICT DO NOTHING`, recordTypeID, papvStatusID); err != nil {
		t.Fatalf("seed invoice_approver: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM invoice_approver WHERE record_type_id = $1 AND record_status_id = $2`, recordTypeID, papvStatusID)
	})
}

// pendingInvoice creates an invoice and submits it for approval.
func pendingInvoice(t *testing.T, pool *pgxpool.Pool) *Invoice {
	t.Helper()
	ctx := context.Background()
	custUUID, itemUUID := seedCustomerAndItem(t, pool)
	inv, err := Create(ctx, pool, CreateInvoiceInput{
		CustomerUUID: custUUID,
		Items:        []InvoiceLineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1, UnitPrice: 10}},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	pending, err := Transition(ctx, pool, inv.ID, "PAPV", 1)
	if err != nil {
		t.Fatalf("Transition to PAPV: %v", err)
	}
	return pending
}

func storedRejections(t *testing.T, pool *pgxpool.Pool, invoiceUUID string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `
		SELECT COUNT(*) FROM approval_rejection ar
		JOIN invoice i ON i.invoice_id = ar.record_id
		JOIN lkp_record_type rt ON rt.record_type_id = ar.record_type_id AND rt.record_type_code = 'INVC'
		WHERE i.invoice_uuid = $1`, invoiceUUID).Scan(&n); err != nil {
		t.Fatalf("count rejections: %v", err)
	}
	return n
}

func TestReject_SendsBackToDraftAndTheReasonClearsOnResubmit(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	configureApprover(t, pool)
	inv := pendingInvoice(t, pool)

	rejected, err := Reject(ctx, pool, inv.ID, 1, false, "Wrong customer")
	if err != nil {
		t.Fatalf("Reject: %v", err)
	}
	if rejected.StatusCode != "DRFT" {
		t.Errorf("status = %q, want DRFT", rejected.StatusCode)
	}
	var approvalStatus string
	if err := pool.QueryRow(ctx, `SELECT invoice_approval_status FROM invoice WHERE invoice_uuid = $1`, inv.ID).Scan(&approvalStatus); err != nil {
		t.Fatalf("load approval status: %v", err)
	}
	if approvalStatus != "none" {
		t.Errorf("approval status = %q, want none", approvalStatus)
	}
	info, err := GetApprovalInfo(ctx, pool, inv.ID, 1, false)
	if err != nil {
		t.Fatalf("GetApprovalInfo: %v", err)
	}
	if info.Rejection == nil || info.Rejection.Reason != "Wrong customer" {
		t.Fatalf("Rejection = %+v, want the reason to be shown on the Draft it was sent back to", info.Rejection)
	}

	// Submitting it again is a new round: the old rejection goes away for good
	// (an invoice cannot be recalled to Draft while its gate is open, so this
	// looks at the stored row rather than at a later recall).
	if _, err := Transition(ctx, pool, inv.ID, "PAPV", 1); err != nil {
		t.Fatalf("resubmit: %v", err)
	}
	if n := storedRejections(t, pool, inv.ID); n != 0 {
		t.Errorf("stored rejections = %d after resubmitting, want 0", n)
	}
	info, err = GetApprovalInfo(ctx, pool, inv.ID, 1, false)
	if err != nil {
		t.Fatalf("GetApprovalInfo after resubmit: %v", err)
	}
	if info.Rejection != nil || !info.Gated || !info.CanApprove || !info.CanReject {
		t.Errorf("info = %+v, want the invoice gated and open to its approver again with no rejection", info)
	}
}

func TestReject_RequiresConfiguredApprover(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	configureApprover(t, pool)
	inv := pendingInvoice(t, pool)

	if _, err := Reject(ctx, pool, inv.ID, 999999, false, "not mine to reject"); !errors.Is(err, ErrNotApprover) {
		t.Fatalf("Reject by a non-approver = %v, want ErrNotApprover", err)
	}
	if _, err := Reject(ctx, pool, "00000000-0000-0000-0000-000000000000", 1, false, "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Reject of a missing invoice = %v, want ErrNotFound", err)
	}
	if got, err := Get(ctx, pool, inv.ID); err != nil || got.StatusCode != "PAPV" {
		t.Fatalf("after refused rejects: Get = %+v, %v; want the invoice still at PAPV", got, err)
	}
}
