//go:build dbtest

package quote

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// addDeactivatedApprover configures a second PAPV approver whose user is then
// deactivated the way userstore.DeactivateUser does it (status only).
func addDeactivatedApprover(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	ctx := context.Background()
	email := "deactivated-approver-" + t.Name() + "@example.test"
	var userID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO users (identity_id, email, full_name, status)
		VALUES (gen_random_uuid(), $1, 'Deactivated Approver', 'active') RETURNING id`, email).Scan(&userID); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	var employeeID int
	if err := pool.QueryRow(ctx, `
		INSERT INTO employee (employee_user_id, employee_first_name, employee_last_name, employee_email, employee_created_by)
		VALUES ($1, 'Deactivated', 'Approver', $2, 1) RETURNING employee_id`, userID, email).Scan(&employeeID); err != nil {
		t.Fatalf("seed employee: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO quote_approver (record_type_id, record_status_id, approver_employee_id)
		SELECT rt.record_type_id, rs.record_status_id, $1
		FROM lkp_record_type rt
		JOIN lkp_record_status rs ON rs.record_status_record_type = rt.record_type_id AND rs.record_status_code = 'PAPV'
		WHERE rt.record_type_code = 'QUOT'`, employeeID); err != nil {
		t.Fatalf("seed second approver: %v", err)
	}
	if _, err := pool.Exec(ctx, `UPDATE users SET status = 'disabled' WHERE id = $1`, userID); err != nil {
		t.Fatalf("deactivate user: %v", err)
	}
}

func TestApprove_DeactivatedApproverDoesNotBlockQuorum(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	configureApprover(t, pool)
	addDeactivatedApprover(t, pool)
	rec := pendingQuote(t, pool)

	info, err := GetApprovalInfo(ctx, pool, rec.ID, 1, false)
	if err != nil {
		t.Fatalf("GetApprovalInfo: %v", err)
	}
	if info.RequiredApprovals != 1 || len(info.Approvers) != 1 || info.Orphaned {
		t.Fatalf("info = %+v, want 1 live approver required and the deactivated one hidden", info)
	}
	approved, err := Approve(ctx, pool, rec.ID, 1, false)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if approved.StatusCode == "PAPV" {
		t.Errorf("still in PAPV: the deactivated approver must not hold the gate")
	}
}
