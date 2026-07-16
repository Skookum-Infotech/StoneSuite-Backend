// quote/approval.go
package quote

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/workflow"
)

// Approval status values stored in quote.quote_approval_status (AD-8).
const (
	approvalNone     = "none"     // no approvers configured for the current status
	approvalPending  = "pending"  // gated: awaiting the required sign-offs
	approvalApproved = "approved" // enough configured approvers have signed off
)

// ErrNotApprover is returned when a caller who is not a configured approver
// for the quote's current status tries to approve it (AD-8). Maps to 403.
var ErrNotApprover = errors.New("you are not a configured approver for this quote's current status")

// ErrApprovalRequired is returned when an quote is asked to leave a status
// that still requires approval sign-off (AD-8). Maps to HTTP 409.
var ErrApprovalRequired = errors.New("this quote must be approved before it can leave its current status")

// ErrApprovalNotRequired is returned when an approval is submitted for an
// quote whose current status has no configured approvers (AD-8). Maps to 409.
var ErrApprovalNotRequired = errors.New("this quote's current status does not require approval")

// activeApproverCount returns how many active approvers are configured for
// the QUOT record type at a status. Zero means no approval gate there (AD-8).
func activeApproverCount(ctx context.Context, q workflow.Querier, recordTypeID, statusID int) (int, error) {
	var n int
	if err := q.QueryRow(ctx, `
		SELECT COUNT(*) FROM quote_approver
		WHERE record_type_id = $1 AND record_status_id = $2 AND is_active`,
		recordTypeID, statusID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count quote approvers: %w", err)
	}
	return n, nil
}

// signOffCount returns how many distinct approvers have signed off on an
// quote at a status.
func signOffCount(ctx context.Context, q workflow.Querier, quoteInternalID, statusID int) (int, error) {
	var n int
	if err := q.QueryRow(ctx, `
		SELECT COUNT(*) FROM quote_approval
		WHERE quote_id = $1 AND record_status_id = $2`,
		quoteInternalID, statusID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count quote approvals: %w", err)
	}
	return n, nil
}

// isConfiguredApprover reports whether an employee is an active configured
// approver for the QUOT record type at a status.
func isConfiguredApprover(ctx context.Context, q workflow.Querier, recordTypeID, statusID, employeeID int) (bool, error) {
	var exists bool
	if err := q.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM quote_approver
			WHERE record_type_id = $1 AND record_status_id = $2 AND approver_employee_id = $3 AND is_active)`,
		recordTypeID, statusID, employeeID).Scan(&exists); err != nil {
		return false, fmt.Errorf("check quote approver: %w", err)
	}
	return exists, nil
}

// Approve records one approver's sign-off on an quote at its current
// status (AD-8). Requires the caller to be a configured approver for that
// status, is idempotent per (quote, status, approver), and flips the
// header's approval_status to 'approved' once the sign-off count reaches the
// configured approver count.
func Approve(ctx context.Context, pool *pgxpool.Pool, uuid string, approverEmployeeID int) (*Quote, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin approve quote: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var internalID, curStatusID int
	err = tx.QueryRow(ctx, `
		SELECT quote_id, quote_status FROM quote
		WHERE quote_uuid = $1 AND quote_deleted_at IS NULL
		FOR UPDATE`, uuid).Scan(&internalID, &curStatusID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load quote for approval: %w", err)
	}

	recordTypeID, err := recordTypeIDByCode(ctx, tx, quotRecordTypeCode)
	if err != nil {
		return nil, fmt.Errorf("resolve QUOT record type: %w", err)
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
		INSERT INTO quote_approval (quote_id, record_status_id, approver_employee_id)
		VALUES ($1, $2, $3)
		ON CONFLICT (quote_id, record_status_id, approver_employee_id) DO NOTHING`,
		internalID, curStatusID, approverEmployeeID); err != nil {
		return nil, fmt.Errorf("record quote approval: %w", err)
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
		UPDATE quote SET
			quote_approval_status = $2, quote_approved_by = $3, quote_updated_at = NOW()
		WHERE quote_id = $1`, internalID, newStatus, approvedBy); err != nil {
		return nil, fmt.Errorf("update quote approval status: %w", err)
	}

	writeHistory(ctx, tx, internalID, "approve", &curStatusID, &curStatusID, approverEmployeeID)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit approve quote: %w", err)
	}
	return Get(ctx, pool, uuid)
}
