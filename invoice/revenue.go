package invoice

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/workflow"
)

// billedStatusCodes are the invoice statuses that count as "billed" for
// revenue purposes: SENT or later in the lifecycle (SENT -> PART/ODUE ->
// PAID). DRFT/PAPV/APPV (not yet sent) and VOID are excluded -- an invoice
// that was never sent, or was voided, was never actually billed revenue.
var billedStatusCodes = []string{"SENT", "PART", "ODUE", "PAID"}

// RevenueBetween sums invoice_grand_total for billed invoices (see
// billedStatusCodes) created in [since, until), scoped to the caller's RBAC
// scope. A zero since/until is unbounded on that side. Used by the KPI
// strip dashboard widget's Revenue metric.
func RevenueBetween(ctx context.Context, pool *pgxpool.Pool, scope, actorIdentityID string, since, until time.Time) (float64, error) {
	where := []string{
		"i.invoice_deleted_at IS NULL",
		"rs.record_status_code = ANY($1)",
	}
	args := []any{billedStatusCodes}
	if scope != string(authz.ScopeAll) {
		empID, found := workflow.EmployeeIDByIdentity(ctx, pool, actorIdentityID)
		if !found {
			return 0, nil
		}
		args = append(args, empID)
		where = append(where, fmt.Sprintf("i.invoice_owner_id = $%d", len(args)))
	}
	if !since.IsZero() {
		args = append(args, since)
		where = append(where, fmt.Sprintf("i.invoice_created_at >= $%d", len(args)))
	}
	if !until.IsZero() {
		args = append(args, until)
		where = append(where, fmt.Sprintf("i.invoice_created_at < $%d", len(args)))
	}

	q := `SELECT COALESCE(SUM(i.invoice_grand_total), 0)
		FROM invoice i
		JOIN lkp_record_status rs ON rs.record_status_id = i.invoice_status
		WHERE ` + strings.Join(where, " AND ")

	var total float64
	if err := pool.QueryRow(ctx, q, args...).Scan(&total); err != nil {
		return 0, fmt.Errorf("sum invoice revenue: %w", err)
	}
	return total, nil
}
