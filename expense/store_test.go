// expense/store_test.go
//go:build dbtest

package expense

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

// seedInactiveEmployee inserts a live, inactive employee for spec AD-8
// layer 2 tests -- the claimant must be active, not merely non-deleted.
func seedInactiveEmployee(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	var employeeID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO employee (employee_first_name, employee_last_name, employee_email, employee_is_active, employee_created_by)
		VALUES ('Inactive', 'Employee', $1, FALSE, 1) RETURNING employee_id`,
		"inactive-"+suffix+"@test.local").Scan(&employeeID); err != nil {
		t.Fatalf("seed inactive employee: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM employee WHERE employee_id = $1`, employeeID)
	})
	return employeeID
}

func TestCreate_TotalsAndNumber(t *testing.T) {
	pool := testPool(t)
	got, err := Create(context.Background(), pool, CreateExpenseInput{expenseFields{
		Department: "Sales",
		Items: []LineInput{
			{LineNumber: 1, CategoryCode: "TRAVEL", ExpenseDate: "2026-08-10", Amount: 412.50},
			{LineNumber: 2, CategoryCode: "MEALS", ExpenseDate: "2026-08-10", Amount: 38.20},
		},
	}}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got.Number == "" || got.Number[:5] != "EXPN-" {
		t.Errorf("Number = %q, want EXPN- prefix", got.Number)
	}
	if got.StatusCode != "DRFT" {
		t.Errorf("StatusCode = %q, want DRFT", got.StatusCode)
	}
	if got.ClaimantEmployeeID != 1 {
		t.Errorf("ClaimantEmployeeID = %d, want 1 (the acting employee)", got.ClaimantEmployeeID)
	}
	if len(got.Items) != 2 {
		t.Fatalf("len(Items) = %d, want 2", len(got.Items))
	}
	if got.Total != 450.70 {
		t.Errorf("Total = %v, want 450.70", got.Total)
	}
}

func TestCreate_RequiresAtLeastOneLine(t *testing.T) {
	pool := testPool(t)
	_, err := Create(context.Background(), pool, CreateExpenseInput{}, 1)
	if !IsClientError(err) {
		t.Fatalf("Create with no items = %v, want ClientError", err)
	}
}

func TestCreate_RequiresResolvedActor(t *testing.T) {
	pool := testPool(t)
	_, err := Create(context.Background(), pool, CreateExpenseInput{expenseFields{
		Items: []LineInput{{LineNumber: 1, CategoryCode: "TRAVEL", ExpenseDate: "2026-08-10", Amount: 10}},
	}}, 0)
	if !IsClientError(err) {
		t.Fatalf("Create with unresolved actor = %v, want ClientError", err)
	}
}

func TestCreate_RejectsInactiveClaimant(t *testing.T) {
	pool := testPool(t)
	inactiveID := seedInactiveEmployee(t, pool)
	_, err := Create(context.Background(), pool, CreateExpenseInput{expenseFields{
		Items: []LineInput{{LineNumber: 1, CategoryCode: "TRAVEL", ExpenseDate: "2026-08-10", Amount: 10}},
	}}, inactiveID)
	if !IsClientError(err) {
		t.Fatalf("Create with inactive claimant = %v, want ClientError", err)
	}
}

func TestCreate_RejectsUnknownCategory(t *testing.T) {
	pool := testPool(t)
	_, err := Create(context.Background(), pool, CreateExpenseInput{expenseFields{
		Items: []LineInput{{LineNumber: 1, CategoryCode: "NOPE", ExpenseDate: "2026-08-10", Amount: 10}},
	}}, 1)
	if !IsClientError(err) {
		t.Fatalf("Create with unknown category = %v, want ClientError", err)
	}
}

func TestUpdate_OnlyDraftEditable(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	created, err := Create(ctx, pool, CreateExpenseInput{expenseFields{
		Items: []LineInput{{LineNumber: 1, CategoryCode: "TRAVEL", ExpenseDate: "2026-08-10", Amount: 100}},
	}}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	updated, err := Update(ctx, pool, created.ID, UpdateExpenseInput{expenseFields{
		Items: []LineInput{{LineNumber: 1, CategoryCode: "TRAVEL", ExpenseDate: "2026-08-10", Amount: 300}},
	}}, 1)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if updated.Total != 300 {
		t.Errorf("Total after update = %v, want 300", updated.Total)
	}

	if _, err := Transition(ctx, pool, created.ID, "SUBM", 1); err != nil {
		t.Fatalf("Transition to SUBM: %v", err)
	}
	_, err = Update(ctx, pool, created.ID, UpdateExpenseInput{expenseFields{
		Items: []LineInput{{LineNumber: 1, CategoryCode: "TRAVEL", ExpenseDate: "2026-08-10", Amount: 500}},
	}}, 1)
	if !IsClientError(err) {
		t.Fatalf("Update at SUBM = %v, want ClientError", err)
	}
}

func TestSoftDelete_GuardedByStatus(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	created, err := Create(ctx, pool, CreateExpenseInput{expenseFields{
		Items: []LineInput{{LineNumber: 1, CategoryCode: "TRAVEL", ExpenseDate: "2026-08-10", Amount: 10}},
	}}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// A submitted claim cannot be deleted...
	if _, err := Transition(ctx, pool, created.ID, "SUBM", 1); err != nil {
		t.Fatalf("Transition to SUBM: %v", err)
	}
	if err := SoftDelete(ctx, pool, created.ID, 1); !IsClientError(err) {
		t.Fatalf("SoftDelete at SUBM = %v, want ClientError", err)
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
	created, err := Create(context.Background(), pool, CreateExpenseInput{expenseFields{
		Items: []LineInput{{LineNumber: 1, CategoryCode: "TRAVEL", ExpenseDate: "2026-08-10", Amount: 10}},
	}}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := Transition(context.Background(), pool, created.ID, "APPV", 1); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Transition DRFT->APPV = %v, want ErrInvalidTransition", err)
	}
	// RJCT is reachable only through Reject, never the generic Transition path (AD-5).
	if _, err := Transition(context.Background(), pool, created.ID, "RJCT", 1); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Transition DRFT->RJCT = %v, want ErrInvalidTransition", err)
	}
}

func TestApprove_RequiresConfiguredApprover(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	created, err := Create(ctx, pool, CreateExpenseInput{expenseFields{
		Items: []LineInput{{LineNumber: 1, CategoryCode: "TRAVEL", ExpenseDate: "2026-08-10", Amount: 10}},
	}}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := Transition(ctx, pool, created.ID, "SUBM", 1); err != nil {
		t.Fatalf("Transition to SUBM: %v", err)
	}
	// No expense_approver rows configured for (EXPN, SUBM) in this test DB by
	// default, so Approve should report the status doesn't require approval.
	if _, err := Approve(ctx, pool, created.ID, 1, false); !errors.Is(err, ErrApprovalNotRequired) {
		t.Fatalf("Approve with no configured approvers = %v, want ErrApprovalNotRequired", err)
	}
}

func TestApprove_SignOffFlipsApprovalStatusAndGatesTransition(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	created, err := Create(ctx, pool, CreateExpenseInput{expenseFields{
		Items: []LineInput{{LineNumber: 1, CategoryCode: "TRAVEL", ExpenseDate: "2026-08-10", Amount: 10}},
	}}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	var recordTypeID, submStatusID int
	if err := pool.QueryRow(ctx, `SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'EXPN'`).Scan(&recordTypeID); err != nil {
		t.Fatalf("resolve EXPN record type: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT record_status_id FROM lkp_record_status WHERE record_status_record_type = $1 AND record_status_code = 'SUBM'`, recordTypeID).Scan(&submStatusID); err != nil {
		t.Fatalf("resolve SUBM status: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO expense_approver (record_type_id, record_status_id, approver_employee_id)
		VALUES ($1, $2, 1) ON CONFLICT DO NOTHING`, recordTypeID, submStatusID); err != nil {
		t.Fatalf("seed expense_approver: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM expense_approver WHERE record_type_id = $1 AND record_status_id = $2`, recordTypeID, submStatusID)
	})

	if _, err := Transition(ctx, pool, created.ID, "SUBM", 1); err != nil {
		t.Fatalf("Transition to SUBM: %v", err)
	}
	// Gate: SUBM -> APPV blocked until the sign-off lands.
	if _, err := Transition(ctx, pool, created.ID, "APPV", 1); !errors.Is(err, ErrApprovalRequired) {
		t.Fatalf("Transition SUBM->APPV before approval = %v, want ErrApprovalRequired", err)
	}
	// Approve auto-advances the expense straight to APPV once quorum is met
	// (expense/approval.go's finalizeApproval) -- no separate Transition
	// call is needed or possible afterward, since the record is no longer
	// at SUBM.
	approved, err := Approve(ctx, pool, created.ID, 1, false)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if approved.StatusCode != "APPV" {
		t.Errorf("StatusCode = %q, want APPV", approved.StatusCode)
	}
	// APPV -> REIM ("process") requires no approval (zero approvers configured there).
	reimbursed, err := Transition(ctx, pool, created.ID, "REIM", 1)
	if err != nil {
		t.Fatalf("Transition APPV->REIM: %v", err)
	}
	if reimbursed.StatusCode != "REIM" {
		t.Errorf("StatusCode = %q, want REIM", reimbursed.StatusCode)
	}
}

func TestReject_UngatedWhenNoApproversConfigured(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	created, err := Create(ctx, pool, CreateExpenseInput{expenseFields{
		Items: []LineInput{{LineNumber: 1, CategoryCode: "TRAVEL", ExpenseDate: "2026-08-10", Amount: 10}},
	}}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := Transition(ctx, pool, created.ID, "SUBM", 1); err != nil {
		t.Fatalf("Transition to SUBM: %v", err)
	}
	rejected, err := Reject(ctx, pool, created.ID, 1, "Missing receipt")
	if err != nil {
		t.Fatalf("Reject: %v", err)
	}
	if rejected.StatusCode != "RJCT" {
		t.Errorf("StatusCode = %q, want RJCT", rejected.StatusCode)
	}
	if rejected.RejectionReason != "Missing receipt" {
		t.Errorf("RejectionReason = %q, want %q", rejected.RejectionReason, "Missing receipt")
	}
	// Rejected claims can be revised back to draft and resubmitted.
	revised, err := Transition(ctx, pool, created.ID, "DRFT", 1)
	if err != nil {
		t.Fatalf("Transition RJCT->DRFT: %v", err)
	}
	if revised.StatusCode != "DRFT" {
		t.Errorf("StatusCode = %q, want DRFT", revised.StatusCode)
	}
	if revised.RejectionReason != "" {
		t.Errorf("RejectionReason after revise = %q, want cleared", revised.RejectionReason)
	}
}

func TestReject_RequiresConfiguredApproverWhenConfigured(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	created, err := Create(ctx, pool, CreateExpenseInput{expenseFields{
		Items: []LineInput{{LineNumber: 1, CategoryCode: "TRAVEL", ExpenseDate: "2026-08-10", Amount: 10}},
	}}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	var recordTypeID, submStatusID int
	if err := pool.QueryRow(ctx, `SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'EXPN'`).Scan(&recordTypeID); err != nil {
		t.Fatalf("resolve EXPN record type: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT record_status_id FROM lkp_record_status WHERE record_status_record_type = $1 AND record_status_code = 'SUBM'`, recordTypeID).Scan(&submStatusID); err != nil {
		t.Fatalf("resolve SUBM status: %v", err)
	}
	// Configure employee 1 as the only approver -- any other employee id must be rejected.
	if _, err := pool.Exec(ctx, `
		INSERT INTO expense_approver (record_type_id, record_status_id, approver_employee_id)
		VALUES ($1, $2, 1) ON CONFLICT DO NOTHING`, recordTypeID, submStatusID); err != nil {
		t.Fatalf("seed expense_approver: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(),
			`DELETE FROM expense_approver WHERE record_type_id = $1 AND record_status_id = $2`, recordTypeID, submStatusID)
	})

	if _, err := Transition(ctx, pool, created.ID, "SUBM", 1); err != nil {
		t.Fatalf("Transition to SUBM: %v", err)
	}
	if _, err := Reject(ctx, pool, created.ID, 999999, "not my call"); !errors.Is(err, ErrNotApprover) {
		t.Fatalf("Reject by non-approver = %v, want ErrNotApprover", err)
	}
	if _, err := Reject(ctx, pool, created.ID, 1, "configured approver rejects"); err != nil {
		t.Fatalf("Reject by configured approver: %v", err)
	}
}

func TestSearch_ReturnsCreatedExpense(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	created, err := Create(ctx, pool, CreateExpenseInput{expenseFields{
		Items: []LineInput{{LineNumber: 1, CategoryCode: "TRAVEL", ExpenseDate: "2026-08-10", Amount: 10}},
	}}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	page, err := Search(ctx, pool, "all", "", query.Request{Search: created.Number})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	found := false
	for _, e := range page.Records {
		if e.ID == created.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("Search(%q) did not include the created expense", created.Number)
	}
}

// TestSoftDelete_UnresolvedActor is the regression for the module-wide
// soft-delete defect (see requisition's identical test): resolveEmployeeID
// returns 0 whenever the caller has no linked employee row, and binding that
// through nullableInt would write SQL NULL into expense_deleted_by, which
// chk_exp_soft_delete rejects.
func TestSoftDelete_UnresolvedActor(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	created, err := Create(ctx, pool, CreateExpenseInput{expenseFields{
		Items: []LineInput{{LineNumber: 1, CategoryCode: "TRAVEL", ExpenseDate: "2026-08-10", Amount: 10}},
	}}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := SoftDelete(ctx, pool, created.ID, 0); err != nil {
		t.Fatalf("SoftDelete with unresolved actor: %v", err)
	}
	var deletedBy int
	if err := pool.QueryRow(ctx,
		`SELECT expense_deleted_by FROM expense WHERE expense_uuid = $1`, created.ID,
	).Scan(&deletedBy); err != nil {
		t.Fatalf("read expense_deleted_by: %v", err)
	}
	if deletedBy != systemEmployeeID {
		t.Errorf("expense_deleted_by = %d, want %d (system employee)", deletedBy, systemEmployeeID)
	}
}
