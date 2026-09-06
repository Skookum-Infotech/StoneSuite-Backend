package salesorder

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/workflow"
)

// fabricationTerminalStatusCodes are the fabrication_job statuses that mean
// a job is no longer active (fabrication.StatusCompleted /
// fabrication.StatusCancelled) -- duplicated here as literals rather than
// importing the fabrication package, which would create a salesorder <->
// fabrication import cycle (fabrication_job has a sales_order_id FK, and the
// fabrication package already imports salesorder-adjacent helpers).
var fabricationTerminalStatusCodes = []string{"COMP", "CANC"}

// CountInFabrication counts live sales orders (status OPEN or PART -- see
// transitions.go) that have at least one linked fabrication_job not yet in a
// terminal status, scoped to the caller's RBAC scope. Used by the KPI strip
// dashboard widget's "N in fabrication" sub-metric.
func CountInFabrication(ctx context.Context, pool *pgxpool.Pool, scope, actorIdentityID string) (int, error) {
	where := []string{
		"so.sales_order_deleted_at IS NULL",
		"srs.record_status_code IN ('OPEN','PART')",
		"fj.fabrication_job_deleted_at IS NULL",
		"frs.record_status_code != ALL($1)",
	}
	args := []any{fabricationTerminalStatusCodes}
	if scope != string(authz.ScopeAll) {
		empID, found := workflow.EmployeeIDByIdentity(ctx, pool, actorIdentityID)
		if !found {
			return 0, nil
		}
		args = append(args, empID)
		where = append(where, fmt.Sprintf("so.sales_order_owner_id = $%d", len(args)))
	}

	q := `SELECT COUNT(DISTINCT so.sales_order_id)
		FROM sales_order so
		JOIN lkp_record_status srs ON srs.record_status_id = so.sales_order_status
		JOIN fabrication_job fj ON fj.sales_order_id = so.sales_order_id
		JOIN lkp_record_status frs ON frs.record_status_id = fj.fabrication_job_status
		WHERE ` + strings.Join(where, " AND ")

	var n int
	if err := pool.QueryRow(ctx, q, args...).Scan(&n); err != nil {
		return 0, fmt.Errorf("count sales orders in fabrication: %w", err)
	}
	return n, nil
}
