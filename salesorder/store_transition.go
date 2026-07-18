package salesorder

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
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
	if err := ValidateTransition(curStatusCode, toStatusCode); err != nil {
		return nil, err
	}

	recordTypeID, err := recordTypeIDByCode(ctx, tx, sordRecordTypeCode)
	if err != nil {
		return nil, fmt.Errorf("resolve SORD record type: %w", err)
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
	if requiredHere > 0 && approvalStatus != approvalApproved {
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

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit transition: %w", err)
	}
	return Get(ctx, pool, uuid)
}
