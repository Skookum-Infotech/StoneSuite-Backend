// approvalchain/liveness.go
package approvalchain

import (
	"context"
	"fmt"

	"stonesuite-backend/workflow"
)

// LiveApproverJoin joins an approver-config row aliased "ea" to its employee
// ("e") and user ("u"), keeping only approvers who can still act: the
// employee isn't soft-deleted and the user isn't deactivated
// (userstore.DeactivateUser sets users.status = 'disabled' and nothing else,
// so the config row's own is_active alone isn't enough). Shared by every
// count, eligibility and recipient query so they can't drift apart.
const LiveApproverJoin = `
	JOIN employee e ON e.employee_id = ea.approver_employee_id AND e.employee_deleted_at IS NULL
	JOIN users u ON u.id = e.employee_user_id AND u.status = 'active'`

// RequiredApprovals turns a status's configured and live approver counts into
// the number of sign-offs it requires. Zero means ungated. A gate whose
// configured approvers have all been deactivated (configured > 0, live == 0)
// is orphaned: it fails closed by requiring one sign-off that no configured
// approver can give, so only a super admin override clears it.
func RequiredApprovals(configured, live int) int {
	if live > 0 {
		return live
	}
	if configured > 0 {
		return 1
	}
	return 0
}

// IsOrphaned reports whether a status has configured approvers but none of
// them can still act.
func IsOrphaned(configured, live int) bool {
	return configured > 0 && live == 0
}

// ApproverCounts returns how many active approver-config rows exist on
// approverTable at (recordTypeID, statusID) and how many of those belong to
// live approvers. Exported for the modules that keep their own approval
// clone (quote, salesorder) so they share this exact query.
func ApproverCounts(ctx context.Context, q workflow.Querier, approverTable string, recordTypeID, statusID int) (configured, live int, err error) {
	return approverCounts(ctx, q, approverTable, recordTypeID, statusID)
}

func approverCounts(ctx context.Context, q workflow.Querier, approverTable string, recordTypeID, statusID int) (configured, live int, err error) {
	// LEFT JOIN form of LiveApproverJoin: every config row counts toward
	// configured, only rows whose user survived both joins count as live.
	if err := q.QueryRow(ctx, fmt.Sprintf(`
		SELECT COUNT(*), COUNT(u.id)
		FROM %s ea
		LEFT JOIN employee e ON e.employee_id = ea.approver_employee_id AND e.employee_deleted_at IS NULL
		LEFT JOIN users u ON u.id = e.employee_user_id AND u.status = 'active'
		WHERE ea.record_type_id = $1 AND ea.record_status_id = $2 AND ea.is_active`, approverTable),
		recordTypeID, statusID).Scan(&configured, &live); err != nil {
		return 0, 0, fmt.Errorf("count active approvers in %s: %w", approverTable, err)
	}
	return configured, live, nil
}
