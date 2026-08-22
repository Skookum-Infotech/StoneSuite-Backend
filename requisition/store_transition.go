// requisition/store_transition.go
package requisition

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/approvalchain"
)

// Transition moves a live requisition to toStatusCode, validating the move
// against the static transition map (AD-6), row-locking the requisition to
// serialize concurrent transitions, enforcing the AD-7 approval gate, and
// writing a history row.
func Transition(ctx context.Context, pool *pgxpool.Pool, uuid, toStatusCode string, actorEmployeeID int) (*Requisition, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transition: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var internalID, curStatusID int
	var curStatusCode, approvalStatus string
	err = tx.QueryRow(ctx, `
		SELECT reqn.requisition_id, reqn.requisition_status, rs.record_status_code, reqn.requisition_approval_status
		FROM requisition reqn JOIN lkp_record_status rs ON rs.record_status_id = reqn.requisition_status
		WHERE reqn.requisition_uuid = $1 AND reqn.requisition_deleted_at IS NULL
		FOR UPDATE OF reqn`, uuid,
	).Scan(&internalID, &curStatusID, &curStatusCode, &approvalStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load requisition for transition: %w", err)
	}
	if err := ValidateTransition(curStatusCode, toStatusCode); err != nil {
		return nil, err
	}

	recordTypeID, err := recordTypeIDByCode(ctx, tx, reqnRecordTypeCode)
	if err != nil {
		return nil, fmt.Errorf("resolve REQN record type: %w", err)
	}
	toStatusID, err := statusIDByCode(ctx, tx, recordTypeID, toStatusCode)
	if err != nil {
		return nil, ClientError{Msg: "Unknown target status."}
	}

	// AD-7 approval gate: a requisition may not leave a status that has
	// configured approvers until it has been approved. Recalling to draft
	// (→ DRFT) is always allowed — it is how a submitter withdraws a pending
	// request for rework without an approver's sign-off (on top of the
	// engine's own always-allowed exits like Void/Cancel/Reject).
	approverTable := moduleConfig().ApproverTable
	if toStatusCode != draftStatusCode {
		requiredHere, err := approvalchain.ActiveApproverCount(ctx, tx, approverTable, recordTypeID, curStatusID)
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

	if _, err := tx.Exec(ctx, `
		UPDATE requisition SET
			requisition_status = $2, requisition_approval_status = $4, requisition_approved_by = NULL,
			requisition_updated_at = NOW(),
			requisition_updated_by = $3, requisition_record_version = requisition_record_version + 1
		WHERE requisition_id = $1`, internalID, toStatusID, nullableInt(actorEmployeeID), newApprovalStatus); err != nil {
		return nil, fmt.Errorf("transition requisition: %w", err)
	}

	writeHistory(ctx, tx, internalID, "transition", &curStatusID, &toStatusID, actorEmployeeID)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit transition: %w", err)
	}
	return Get(ctx, pool, uuid)
}
