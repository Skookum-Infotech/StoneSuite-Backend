//go:build dbtest

package refund

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// ensureLiveApprover links employeeID to an active user when it has none, so
// it counts as a live approver: approval quorum and eligibility only count
// approvers whose employee is linked to a user with status = 'active'
// (approvalchain.LiveApproverJoin). The seeded system employee 1 has no user.
func ensureLiveApprover(t *testing.T, pool *pgxpool.Pool, employeeID int) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		WITH linked AS (
			INSERT INTO users (identity_id, email, full_name, status)
			SELECT gen_random_uuid(), 'live-approver-' || $1::int::text || '@example.test', 'Test Approver', 'active'
			WHERE EXISTS (SELECT 1 FROM employee WHERE employee_id = $1::int AND employee_user_id IS NULL)
			RETURNING id
		)
		UPDATE employee SET employee_user_id = linked.id
		FROM linked WHERE employee_id = $1::int`, employeeID); err != nil {
		t.Fatalf("link employee %d to an active user: %v", employeeID, err)
	}
}
