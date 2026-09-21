//go:build dbtest

package quote

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
	if err := pool.QueryRow(ctx, `SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'QUOT'`).Scan(&recordTypeID); err != nil {
		t.Fatalf("resolve QUOT record type: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT record_status_id FROM lkp_record_status WHERE record_status_record_type = $1 AND record_status_code = 'PAPV'`, recordTypeID).Scan(&papvStatusID); err != nil {
		t.Fatalf("resolve PAPV status: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO quote_approver (record_type_id, record_status_id, approver_employee_id)
		VALUES ($1, $2, 1) ON CONFLICT DO NOTHING`, recordTypeID, papvStatusID); err != nil {
		t.Fatalf("seed quote_approver: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM quote_approver WHERE record_type_id = $1 AND record_status_id = $2`, recordTypeID, papvStatusID)
	})
}

// pendingQuote creates a quote and submits it for approval.
func pendingQuote(t *testing.T, pool *pgxpool.Pool) *Quote {
	t.Helper()
	ctx := context.Background()
	custUUID, itemUUID := seedCustomerAndItem(t, pool)
	created, err := Create(ctx, pool, CreateQuoteInput{
		CustomerUUID: custUUID,
		quoteFields:  quoteFields{Items: []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}}},
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

func storedRejections(t *testing.T, pool *pgxpool.Pool, uuid string) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(context.Background(), `
		SELECT COUNT(*) FROM approval_rejection ar
		JOIN quote q ON q.quote_id = ar.record_id
		JOIN lkp_record_type rt ON rt.record_type_id = ar.record_type_id AND rt.record_type_code = 'QUOT'
		WHERE q.quote_uuid = $1`, uuid).Scan(&n); err != nil {
		t.Fatalf("count rejections: %v", err)
	}
	return n
}

func TestReject_SendsBackToDraftAndTheReasonClearsOnResubmit(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	configureApprover(t, pool)
	q := pendingQuote(t, pool)

	rejected, err := Reject(ctx, pool, q.ID, 1, false, "Pricing is off")
	if err != nil {
		t.Fatalf("Reject: %v", err)
	}
	if rejected.StatusCode != "DRFT" {
		t.Errorf("status = %q, want DRFT", rejected.StatusCode)
	}
	if rejected.ApprovalStatus != "none" {
		t.Errorf("approval status = %q, want none", rejected.ApprovalStatus)
	}
	info, err := GetApprovalInfo(ctx, pool, q.ID, 1, false)
	if err != nil {
		t.Fatalf("GetApprovalInfo: %v", err)
	}
	if info.Rejection == nil || info.Rejection.Reason != "Pricing is off" {
		t.Fatalf("Rejection = %+v, want the reason to be shown on the Draft it was sent back to", info.Rejection)
	}
	if info.Gated {
		t.Error("a Draft quote is not gated")
	}

	// Submitting it again is a new round: the old rejection goes away for good
	// (a quote cannot be recalled to Draft while its gate is open, so this
	// looks at the stored row rather than at a later recall).
	if _, err := Transition(ctx, pool, q.ID, "PAPV", 1); err != nil {
		t.Fatalf("resubmit: %v", err)
	}
	if n := storedRejections(t, pool, q.ID); n != 0 {
		t.Errorf("stored rejections = %d after resubmitting, want 0", n)
	}
	info, err = GetApprovalInfo(ctx, pool, q.ID, 1, false)
	if err != nil {
		t.Fatalf("GetApprovalInfo after resubmit: %v", err)
	}
	if info.Rejection != nil || !info.Gated || !info.CanApprove || !info.CanReject {
		t.Errorf("info = %+v, want the quote gated and open to its approver again with no rejection", info)
	}
}

func TestReject_RequiresConfiguredApprover(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	configureApprover(t, pool)
	q := pendingQuote(t, pool)

	if _, err := Reject(ctx, pool, q.ID, 999999, false, "not mine to reject"); !errors.Is(err, ErrNotApprover) {
		t.Fatalf("Reject by a non-approver = %v, want ErrNotApprover", err)
	}
	if _, err := Reject(ctx, pool, "00000000-0000-0000-0000-000000000000", 1, false, "x"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Reject of a missing quote = %v, want ErrNotFound", err)
	}
	if got, err := Get(ctx, pool, q.ID); err != nil || got.StatusCode != "PAPV" {
		t.Fatalf("after refused rejects: Get = %+v, %v; want the quote still at PAPV", got, err)
	}
}

// A super admin who is not a configured approver can approve on their behalf
// (an override). It is written to history as "approve_override", which the
// history CHECK has to allow -- otherwise the whole approval transaction
// aborts on the rejected INSERT. Lives beside the reject tests because the
// migration that widens that CHECK for "reject" is the same one that fixes it.
func TestApprove_SuperAdminOverrideIsRecordedInHistory(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	configureApprover(t, pool)
	q := pendingQuote(t, pool)

	superAdmin := seedEmployee(t, pool)
	approved, err := Approve(ctx, pool, q.ID, superAdmin, true)
	if err != nil {
		t.Fatalf("Approve as a super admin override: %v", err)
	}
	if approved.StatusCode != "APPV" {
		t.Errorf("status = %q, want APPV", approved.StatusCode)
	}
	var n int
	if err := pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM quote_history h
		JOIN quote q ON q.quote_id = h.quote_id
		WHERE q.quote_uuid = $1 AND h.action = 'approve_override'`, q.ID).Scan(&n); err != nil {
		t.Fatalf("count override history: %v", err)
	}
	if n != 1 {
		t.Errorf("approve_override history rows = %d, want 1", n)
	}
}

// seedEmployee inserts a real user-linked employee -- an override's approver
// is recorded on the quote itself, so it has to exist.
func seedEmployee(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	ctx := context.Background()
	email := "override-" + t.Name() + "@example.test"
	var userID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (identity_id, email, full_name, status)
		VALUES (gen_random_uuid(), $1, 'Override Admin', 'active') RETURNING id`, email).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	var employeeID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO employee (employee_user_id, employee_first_name, employee_last_name, employee_email, employee_created_by)
		VALUES ($1, 'Override', 'Admin', $2, 1) RETURNING employee_id`, userID, email).Scan(&employeeID); err != nil {
		t.Fatalf("seed employee: %v", err)
	}
	return employeeID
}
