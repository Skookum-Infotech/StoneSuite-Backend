// expense/store_transition.go
package expense

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/approvalchain"
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
	// rework without an approver's sign-off (on top of the engine's own
	// always-allowed exits like Void/Cancel/Reject).
	approverTable := moduleConfig().ApproverTable
	var requiredHere int
	if toStatusCode != draftStatusCode {
		requiredHere, err = approvalchain.ActiveApproverCount(ctx, tx, approverTable, recordTypeID, curStatusID)
		if err != nil {
			return nil, err
		}
		if err := approvalchain.CheckTransitionGate(requiredHere, approvalStatus, toStatusCode); err != nil {
			return nil, ErrApprovalRequired
		}
	}
	targetApprovers, err := approvalchain.ActiveApproverCount(ctx, tx, approverTable, recordTypeID, toStatusID)
	if err != nil {
		return nil, err
	}
	newApprovalStatus := approvalchain.StatusNone
	if targetApprovers > 0 {
		newApprovalStatus = approvalchain.StatusPending
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
	notifyTransition(ctx, pool, uuid, internalID, recordTypeID, toStatusID, toStatusCode, requiredHere, approvalStatus, targetApprovers, actorEmployeeID)
	return Get(ctx, pool, uuid)
}

// notifyTransition best-effort-notifies approvers/owner around a manual
// status move (AD-4): approvers when the claim just entered a gated status,
// or the claimant when a gated-unapproved claim escaped via an
// always-allowed exit (void/cancel/reject) instead of clearing approval.
func notifyTransition(ctx context.Context, pool *pgxpool.Pool, uuid string, internalID, recordTypeID, toStatusID int, toStatusCode string, requiredHere int, approvalStatus string, targetApprovers int, actorEmployeeID int) {
	cfg := moduleConfig()
	rec := cfg.Record
	if targetApprovers > 0 {
		approvalchain.NotifyApprovalRequested(ctx, pool, approvalchain.EventContext{
			Table: rec.Table, IDColumn: rec.IDColumn, NumberColumn: rec.NumberColumn,
			ApproverTable: cfg.ApproverTable, RecordTypeID: recordTypeID, StatusID: toStatusID,
			InternalID: internalID, ActorEmployeeID: actorEmployeeID,
			Resource: cfg.Resource, DisplayName: cfg.DisplayName, RecordUUID: uuid,
		})
	}
	if requiredHere > 0 && approvalStatus != approvalchain.StatusApproved && approvalchain.AlwaysAllowedExitCodes[toStatusCode] {
		approvalchain.NotifyApprovalRejected(ctx, pool, approvalchain.EventContext{
			Table: rec.Table, IDColumn: rec.IDColumn, NumberColumn: rec.NumberColumn, OwnerColumn: rec.OwnerColumn,
			InternalID: internalID, ActorEmployeeID: actorEmployeeID,
			Resource: cfg.Resource, DisplayName: cfg.DisplayName, RecordUUID: uuid,
		})
	}
}

// notifyCreated best-effort-notifies the actor who created a new expense
// claim that the creation succeeded.
func notifyCreated(ctx context.Context, pool *pgxpool.Pool, uuid string, internalID, actorEmployeeID int) {
	cfg := moduleConfig()
	approvalchain.NotifyCreated(ctx, pool, approvalchain.EventContext{
		Table: cfg.Record.Table, IDColumn: cfg.Record.IDColumn, NumberColumn: cfg.Record.NumberColumn,
		InternalID: internalID, ActorEmployeeID: actorEmployeeID,
		Resource: cfg.Resource, DisplayName: cfg.DisplayName, RecordUUID: uuid,
	})
}
