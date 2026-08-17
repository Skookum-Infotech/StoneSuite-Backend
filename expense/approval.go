// expense/approval.go — spec AD-4: the configuration-driven approval gate,
// mirroring requisition/approval.go, plus AD-5's dedicated Reject (which the
// reference module has no equivalent of -- requisition has no RJCT state).
package expense

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/workflow"
)

// Approval status values stored in expense.expense_approval_status (AD-4).
const (
	approvalNone     = "none"     // no approvers configured for the current status
	approvalPending  = "pending"  // gated: awaiting the required sign-offs
	approvalApproved = "approved" // enough configured approvers have signed off
)

// ErrNotApprover is returned when a caller who is not a configured approver
// for the claim's current status tries to approve or reject it. Maps to 403.
var ErrNotApprover = errors.New("you are not a configured approver for this expense claim's current status")

// ErrApprovalRequired is returned when a claim is asked to leave a status
// that still requires approval sign-off. Maps to HTTP 409.
var ErrApprovalRequired = errors.New("this expense claim must be approved before it can leave its current status")

// ErrApprovalNotRequired is returned when an approval is submitted for a
// claim whose current status has no configured approvers. Maps to 409.
var ErrApprovalNotRequired = errors.New("this expense claim's current status does not require approval")

// activeApproverCount returns how many active approvers are configured for
// the EXPN record type at a status. Zero means no approval gate there.
// expense_approver.is_active is what keeps an inactive approver assignment
// from counting toward quorum (spec AD-8, layer 3).
func activeApproverCount(ctx context.Context, q workflow.Querier, recordTypeID, statusID int) (int, error) {
	var n int
	if err := q.QueryRow(ctx, `
		SELECT COUNT(*) FROM expense_approver
		WHERE record_type_id = $1 AND record_status_id = $2 AND is_active`,
		recordTypeID, statusID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count expense approvers: %w", err)
	}
	return n, nil
}

// signOffCount returns how many distinct approvers have signed off on a
// claim at a status.
func signOffCount(ctx context.Context, q workflow.Querier, expInternalID, statusID int) (int, error) {
	var n int
	if err := q.QueryRow(ctx, `
		SELECT COUNT(*) FROM expense_approval
		WHERE expense_id = $1 AND record_status_id = $2`,
		expInternalID, statusID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count expense approvals: %w", err)
	}
	return n, nil
}

// isConfiguredApprover reports whether an employee is an active configured
// approver for the EXPN record type at a status.
func isConfiguredApprover(ctx context.Context, q workflow.Querier, recordTypeID, statusID, employeeID int) (bool, error) {
	var exists bool
	if err := q.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM expense_approver
			WHERE record_type_id = $1 AND record_status_id = $2 AND approver_employee_id = $3 AND is_active)`,
		recordTypeID, statusID, employeeID).Scan(&exists); err != nil {
		return false, fmt.Errorf("check expense approver: %w", err)
	}
	return exists, nil
}

// Approve records one approver's sign-off on an expense claim at its current
// status (AD-4). Requires the caller to be a configured approver for that
// status, is idempotent per (claim, status, approver), and flips the
// header's approval_status to 'approved' once the sign-off count reaches the
// configured approver count. Does not itself move expense_status -- a
// subsequent Transition call does that once approval_status is 'approved'.
func Approve(ctx context.Context, pool *pgxpool.Pool, uuid string, approverEmployeeID int) (*Expense, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin approve expense: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var internalID, curStatusID int
	err = tx.QueryRow(ctx, `
		SELECT expense_id, expense_status FROM expense
		WHERE expense_uuid = $1 AND expense_deleted_at IS NULL
		FOR UPDATE`, uuid).Scan(&internalID, &curStatusID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load expense for approval: %w", err)
	}

	recordTypeID, err := recordTypeIDByCode(ctx, tx, expenseRecordTypeCode)
	if err != nil {
		return nil, fmt.Errorf("resolve EXPN record type: %w", err)
	}

	required, err := activeApproverCount(ctx, tx, recordTypeID, curStatusID)
	if err != nil {
		return nil, err
	}
	if required == 0 {
		return nil, ErrApprovalNotRequired
	}

	ok, err := isConfiguredApprover(ctx, tx, recordTypeID, curStatusID, approverEmployeeID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotApprover
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO expense_approval (expense_id, record_status_id, approver_employee_id)
		VALUES ($1, $2, $3)
		ON CONFLICT (expense_id, record_status_id, approver_employee_id) DO NOTHING`,
		internalID, curStatusID, approverEmployeeID); err != nil {
		return nil, fmt.Errorf("record expense approval: %w", err)
	}

	approved, err := signOffCount(ctx, tx, internalID, curStatusID)
	if err != nil {
		return nil, err
	}
	newStatus := approvalPending
	var approvedBy any
	if approved >= required {
		newStatus = approvalApproved
		approvedBy = approverEmployeeID
	}
	if _, err := tx.Exec(ctx, `
		UPDATE expense SET
			expense_approval_status = $2, expense_approved_by = $3, expense_updated_at = NOW()
		WHERE expense_id = $1`, internalID, newStatus, approvedBy); err != nil {
		return nil, fmt.Errorf("update expense approval status: %w", err)
	}

	writeHistory(ctx, tx, internalID, "approve", &curStatusID, &curStatusID, approverEmployeeID)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit approve expense: %w", err)
	}
	return Get(ctx, pool, uuid)
}

// Reject moves a submitted expense claim directly to RJCT (spec AD-5) --
// unlike Approve, rejecting is itself the decision and never requires
// quorum: if approvers are configured for the current status, only a
// configured approver may reject (ErrNotApprover otherwise); if none are
// configured, any caller with expense:transition may. Always records the
// reason on the header and in expense_history.
func Reject(ctx context.Context, pool *pgxpool.Pool, uuid string, actorEmployeeID int, reason string) (*Expense, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin reject expense: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var internalID, curStatusID int
	var curStatusCode string
	err = tx.QueryRow(ctx, `
		SELECT exp.expense_id, exp.expense_status, rs.record_status_code
		FROM expense exp JOIN lkp_record_status rs ON rs.record_status_id = exp.expense_status
		WHERE exp.expense_uuid = $1 AND exp.expense_deleted_at IS NULL
		FOR UPDATE OF exp`, uuid).Scan(&internalID, &curStatusID, &curStatusCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load expense for reject: %w", err)
	}
	if curStatusCode != "SUBM" {
		return nil, ClientError{Msg: "Only a submitted expense claim can be rejected."}
	}

	recordTypeID, err := recordTypeIDByCode(ctx, tx, expenseRecordTypeCode)
	if err != nil {
		return nil, fmt.Errorf("resolve EXPN record type: %w", err)
	}
	required, err := activeApproverCount(ctx, tx, recordTypeID, curStatusID)
	if err != nil {
		return nil, err
	}
	if required > 0 {
		ok, err := isConfiguredApprover(ctx, tx, recordTypeID, curStatusID, actorEmployeeID)
		if err != nil {
			return nil, err
		}
		if !ok {
			return nil, ErrNotApprover
		}
	}

	rjctStatusID, err := statusIDByCode(ctx, tx, recordTypeID, "RJCT")
	if err != nil {
		return nil, fmt.Errorf("resolve RJCT status: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE expense SET
			expense_status = $2, expense_approval_status = $3,
			expense_approved_by = NULL, expense_rejected_by = $4, expense_rejection_reason = $5,
			expense_updated_at = NOW(),
			expense_updated_by = $4, expense_record_version = expense_record_version + 1
		WHERE expense_id = $1`,
		internalID, rjctStatusID, approvalNone, nullableInt(actorEmployeeID), reason); err != nil {
		return nil, fmt.Errorf("reject expense: %w", err)
	}

	writeHistory(ctx, tx, internalID, "reject", &curStatusID, &rjctStatusID, actorEmployeeID)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit reject expense: %w", err)
	}
	return Get(ctx, pool, uuid)
}
