// expense/store_transition.go
package expense

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Transition moves a live expense claim to toStatusCode, validating the move
// against the static transition map (spec §5), row-locking the claim to
// serialize concurrent transitions, enforcing the AD-4 approval gate, and
// writing a history row. RJCT is not a reachable target here -- rejection
// always goes through the dedicated Reject function (approval.go, AD-5), so
// a reason is always captured.
func Transition(ctx context.Context, pool *pgxpool.Pool, uuid, toStatusCode string, actorEmployeeID int) (*Expense, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transition: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var internalID, curStatusID int
	var curStatusCode, approvalStatus string
	err = tx.QueryRow(ctx, `
		SELECT exp.expense_id, exp.expense_status, rs.record_status_code, exp.expense_approval_status
		FROM expense exp JOIN lkp_record_status rs ON rs.record_status_id = exp.expense_status
		WHERE exp.expense_uuid = $1 AND exp.expense_deleted_at IS NULL
		FOR UPDATE OF exp`, uuid,
	).Scan(&internalID, &curStatusID, &curStatusCode, &approvalStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load expense for transition: %w", err)
	}
	if err := ValidateTransition(curStatusCode, toStatusCode); err != nil {
		return nil, err
	}

	recordTypeID, err := recordTypeIDByCode(ctx, tx, expenseRecordTypeCode)
	if err != nil {
		return nil, fmt.Errorf("resolve EXPN record type: %w", err)
	}
	toStatusID, err := statusIDByCode(ctx, tx, recordTypeID, toStatusCode)
	if err != nil {
		return nil, ClientError{Msg: "Unknown target status."}
	}

	// AD-4 approval gate: a claim may not leave a status that has configured
	// approvers until it has been approved. Recalling to draft (-> DRFT) is
	// always allowed -- it is how a submitter withdraws a pending claim for
	// rework without an approver's sign-off.
	if toStatusCode != draftStatusCode {
		requiredHere, err := activeApproverCount(ctx, tx, recordTypeID, curStatusID)
		if err != nil {
			return nil, err
		}
		if requiredHere > 0 && approvalStatus != approvalApproved {
			return nil, ErrApprovalRequired
		}
	}
	targetApprovers, err := activeApproverCount(ctx, tx, recordTypeID, toStatusID)
	if err != nil {
		return nil, err
	}
	newApprovalStatus := approvalNone
	if targetApprovers > 0 {
		newApprovalStatus = approvalPending
	}

	// Every generic transition clears approved_by/rejected_by/rejection_reason
	// -- a fresh lifecycle step (e.g. RJCT->DRFT revise, then re-submit)
	// should never carry a stale decision from a prior cycle forward.
	if _, err := tx.Exec(ctx, `
		UPDATE expense SET
			expense_status = $2, expense_approval_status = $4,
			expense_approved_by = NULL, expense_rejected_by = NULL, expense_rejection_reason = '',
			expense_updated_at = NOW(),
			expense_updated_by = $3, expense_record_version = expense_record_version + 1
		WHERE expense_id = $1`, internalID, toStatusID, nullableInt(actorEmployeeID), newApprovalStatus); err != nil {
		return nil, fmt.Errorf("transition expense: %w", err)
	}

	writeHistory(ctx, tx, internalID, "transition", &curStatusID, &toStatusID, actorEmployeeID)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit transition: %w", err)
	}
	return Get(ctx, pool, uuid)
}
