package salesorder

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/approvalchain"
)

// ----- Transition ------------------------------------------------------------

// Transition moves a live order to toStatusCode, validating the move against
// the static transition map (spec §8), row-locking the order to serialize
// concurrent transitions, writing a history row, and — on cancellation —
// releasing any open inventory allocations (spec §8, Task 6.1).
func Transition(ctx context.Context, pool *pgxpool.Pool, uuid, toStatusCode string, actorEmployeeID int) (*Order, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transition: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var internalID, curStatusID int
	var curStatusCode, approvalStatus string
	err = tx.QueryRow(ctx, `
		SELECT so.sales_order_id, so.sales_order_status, rs.record_status_code, so.sales_order_approval_status
		FROM sales_order so JOIN lkp_record_status rs ON rs.record_status_id = so.sales_order_status
		WHERE so.sales_order_uuid = $1 AND so.sales_order_deleted_at IS NULL
		FOR UPDATE OF so`, uuid,
	).Scan(&internalID, &curStatusID, &curStatusCode, &approvalStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load sales order for transition: %w", err)
	}
	recordTypeID, err := recordTypeIDByCode(ctx, tx, sordRecordTypeCode)
	if err != nil {
		return nil, fmt.Errorf("resolve SORD record type: %w", err)
	}
	// The static map has the first word on legality, not the last: with
	// nobody configured to approve, a move may pass straight through the
	// approval checkpoint (Draft -> Open) -- exactly the moves the status
	// control offers via NextStatusCodes, so the two can't disagree.
	var viaGate *approvalchain.Gate
	if err := ValidateTransition(curStatusCode, toStatusCode); err != nil {
		ungated, uerr := approvalchain.UngatedGates(ctx, tx, moduleConfig(), recordTypeID)
		if uerr != nil {
			return nil, uerr
		}
		gate, ok := approvalchain.PassThroughGate(curStatusCode, toStatusCode, NextStatuses, ungated)
		if !ok {
			return nil, err
		}
		viaGate = &gate
	}

	toStatusID, err := statusIDByCode(ctx, tx, recordTypeID, toStatusCode)
	if err != nil {
		return nil, ClientError{Msg: "Unknown target status."}
	}

	// AD-10 approval gate: an order may not leave a status that has configured
	// approvers until it has been approved. Statuses with no approvers never block.
	requiredHere, err := activeApproverCount(ctx, tx, recordTypeID, curStatusID)
	if err != nil {
		return nil, err
	}
	if requiredHere > 0 && approvalStatus != approvalApproved && !approvalchain.AlwaysAllowedExitCodes[toStatusCode] {
		return nil, ErrApprovalRequired
	}
	// The status being entered may itself require approval → start it pending.
	targetApprovers, err := activeApproverCount(ctx, tx, recordTypeID, toStatusID)
	if err != nil {
		return nil, err
	}
	newApprovalStatus := approvalNone
	if targetApprovers > 0 {
		newApprovalStatus = approvalPending
	}
	// A move onto the approval checkpoint with zero approvers configured
	// skips straight to Approved instead of parking the order on a "Pending
	// Approval" label nobody can ever resolve -- mirrors finalizeApproval's
	// own floor (approvalApproved) for landing on the gate's approved target.
	if toStatusCode == approvalGateStatusCode && targetApprovers == 0 {
		toStatusCode = approvedStatusCode
		toStatusID, err = statusIDByCode(ctx, tx, recordTypeID, toStatusCode)
		if err != nil {
			return nil, fmt.Errorf("resolve %s status: %w", toStatusCode, err)
		}
		targetApprovers, err = activeApproverCount(ctx, tx, recordTypeID, toStatusID)
		if err != nil {
			return nil, err
		}
		newApprovalStatus = approvalApproved
		if targetApprovers > 0 {
			newApprovalStatus = approvalPending
		}
	}
	// Passing through the unconfigured checkpoint onto its approved target is
	// the same outcome as the auto-skip above -- record it as approved too,
	// so the two paths can't disagree about what landing on APPV means.
	if viaGate != nil && toStatusCode == viaGate.TargetStatusCode && targetApprovers == 0 {
		newApprovalStatus = approvalApproved
	}

	if _, err := tx.Exec(ctx, `
		UPDATE sales_order SET
			sales_order_status = $2, sales_order_approval_status = $4, sales_order_approved_by = NULL,
			sales_order_updated_at = NOW(),
			sales_order_updated_by = $3, sales_order_record_version = sales_order_record_version + 1
		WHERE sales_order_id = $1`, internalID, toStatusID, nullableInt(actorEmployeeID), newApprovalStatus); err != nil {
		return nil, fmt.Errorf("transition sales order: %w", err)
	}

	if toStatusCode == "CANC" {
		if _, err := tx.Exec(ctx, `
			UPDATE inventory_allocation SET allocation_status = 'released', allocation_updated_at = NOW()
			WHERE sales_order_id = $1 AND allocation_status IN ('reserved','partially_fulfilled')`, internalID); err != nil {
			return nil, fmt.Errorf("release allocations on cancel: %w", err)
		}
	}

	writeHistory(ctx, tx, internalID, "transition", &curStatusID, &toStatusID, actorEmployeeID)
	// Any move takes the sales order out of the state a Reject left it in.
	if err := approvalchain.ClearRejection(ctx, tx, recordTypeID, internalID); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit transition: %w", err)
	}
	notifyTransition(ctx, pool, uuid, internalID, recordTypeID, toStatusID, toStatusCode, requiredHere, approvalStatus, targetApprovers, actorEmployeeID)
	return Get(ctx, pool, uuid)
}
