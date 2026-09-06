package requisition

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// PendingApprovalREQN is one requisition awaiting approval sign-off, ranked
// oldest first (longest waiting) by DashboardPendingApproval. Vendor is the
// suggested vendor (AD-2, nullable) and may be blank; Department is the
// free-text field entered at creation. controllers/dashboard_purchases.go's
// requisitionParty picks between them for display.
type PendingApprovalREQN struct {
	ID           string
	RecordNumber string
	Vendor       string
	Department   string
	Value        float64
	CreatedAt    time.Time
}

// PendingApprovalResult mirrors purchaseorder.PendingApprovalResult exactly
// -- see its doc comment for the Eligible/Rows/TotalCount/TotalValue
// contract, which this type shares.
type PendingApprovalResult struct {
	Eligible   bool
	Rows       []PendingApprovalREQN
	TotalCount int
	TotalValue float64
}

// DashboardPendingApproval loads up to limit requisitions awaiting approval
// that employeeID is a configured approver for (every pending requisition,
// unfiltered, when isSuperAdmin), oldest first, alongside the true total
// count/value across every matching requisition. Mirrors
// purchaseorder.DashboardPendingApproval's authorization shape exactly --
// see its doc comment for why this is approver-table membership, not
// generic RBAC resource read/scope.
func DashboardPendingApproval(ctx context.Context, pool *pgxpool.Pool, employeeID int, isSuperAdmin bool, limit int) (PendingApprovalResult, error) {
	where := []string{"reqn.requisition_deleted_at IS NULL", "reqn.requisition_approval_status = 'pending'"}
	var args []any
	join := ""
	if !isSuperAdmin {
		recordTypeID, err := recordTypeIDByCode(ctx, pool, reqnRecordTypeCode)
		if err != nil {
			return PendingApprovalResult{}, fmt.Errorf("resolve requisition record type: %w", err)
		}
		eligible, err := isConfiguredApprover(ctx, pool, recordTypeID, employeeID)
		if err != nil {
			return PendingApprovalResult{}, fmt.Errorf("check requisition approver eligibility: %w", err)
		}
		if !eligible {
			return PendingApprovalResult{Eligible: false}, nil
		}
		join = `JOIN requisition_approver ap ON ap.record_type_id = $1 AND ap.record_status_id = reqn.requisition_status
			AND ap.approver_employee_id = $2 AND ap.is_active`
		args = append(args, recordTypeID, employeeID)
	}

	q := `
		SELECT reqn.requisition_uuid, COALESCE(reqn.requisition_number, ''),
		       reqn.requisition_vendor_name, reqn.requisition_department,
		       reqn.requisition_estimated_total, reqn.requisition_created_at,
		       COUNT(*) OVER (), COALESCE(SUM(reqn.requisition_estimated_total) OVER (), 0)
		FROM requisition reqn
		` + join + `
		WHERE ` + strings.Join(where, " AND ") + `
		ORDER BY reqn.requisition_created_at ASC
		LIMIT ` + strconv.Itoa(limit)

	rows, err := pool.Query(ctx, q, args...)
	if err != nil {
		return PendingApprovalResult{}, fmt.Errorf("query pending approval requisitions: %w", err)
	}
	defer rows.Close()

	res := PendingApprovalResult{Eligible: true, Rows: []PendingApprovalREQN{}}
	for rows.Next() {
		var p PendingApprovalREQN
		if err := rows.Scan(&p.ID, &p.RecordNumber, &p.Vendor, &p.Department, &p.Value, &p.CreatedAt, &res.TotalCount, &res.TotalValue); err != nil {
			return PendingApprovalResult{}, fmt.Errorf("scan pending approval requisition: %w", err)
		}
		res.Rows = append(res.Rows, p)
	}
	if err := rows.Err(); err != nil {
		return PendingApprovalResult{}, fmt.Errorf("query pending approval requisitions: %w", err)
	}
	return res, nil
}

// isConfiguredApprover reports whether employeeID is an active approver for
// at least one requisition status gate, regardless of current pending count
// -- mirrors purchaseorder.isConfiguredApprover exactly (see its doc
// comment for why this is duplicated per package rather than shared).
func isConfiguredApprover(ctx context.Context, pool *pgxpool.Pool, recordTypeID, employeeID int) (bool, error) {
	var eligible bool
	err := pool.QueryRow(ctx,
		`SELECT EXISTS(SELECT 1 FROM requisition_approver WHERE record_type_id = $1 AND approver_employee_id = $2 AND is_active)`,
		recordTypeID, employeeID).Scan(&eligible)
	if err != nil {
		return false, fmt.Errorf("query approver eligibility: %w", err)
	}
	return eligible, nil
}
