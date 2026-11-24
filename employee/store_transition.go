package employee

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/approvalchain"
)

// Transition moves a live employee to toStatusCode (Active or Inactive),
// row-locking it to serialise concurrent moves and writing a history row. A
// pending employee can be made Active by hand only when nobody is configured to
// approve it (or it was already approved); otherwise the approvers' sign-off
// does it. The employee's Vendor record follows the move, in the same
// transaction. Nobody can deactivate their own record.
func Transition(ctx context.Context, pool *pgxpool.Pool, uuid, toStatusCode string, actorEmployeeID int) (*Employee, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transition employee: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var internalID, curStatusID int
	var curStatusCode, approvalStatus string
	err = tx.QueryRow(ctx, `
		SELECT e.employee_id, COALESCE(e.employee_status, 0),
		       COALESCE(rs.record_status_code, CASE WHEN e.employee_is_active THEN 'ACTV' ELSE 'INAC' END),
		       e.employee_approval_status
		FROM employee e
		LEFT JOIN lkp_record_status rs ON rs.record_status_id = e.employee_status
		WHERE e.employee_uuid = $1 AND `+empLive+`
		FOR UPDATE OF e`, uuid,
	).Scan(&internalID, &curStatusID, &curStatusCode, &approvalStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load employee for transition: %w", err)
	}
	if err := ValidateTransition(curStatusCode, toStatusCode); err != nil {
		return nil, err
	}
	if toStatusCode == statusInactive && internalID == actorEmployeeID {
		return nil, ClientError{Msg: "You cannot deactivate your own employee record."}
	}

	recordTypeID, err := recordTypeIDByCode(ctx, tx, recordTypeCode)
	if err != nil {
		return nil, fmt.Errorf("resolve EMPL record type: %w", err)
	}
	// A pending employee stays pending until its approvers have signed, unless
	// nobody is configured to approve it any more.
	if curStatusCode == statusPending && toStatusCode == statusActive {
		required, err := approvalchain.ActiveApproverCount(ctx, tx, moduleConfig().ApproverTable, recordTypeID, curStatusID)
		if err != nil {
			return nil, err
		}
		if err := approvalchain.CheckTransitionGate(required, approvalStatus, toStatusCode); err != nil {
			return nil, ErrApprovalRequired
		}
	}
	toStatusID, err := statusIDByCode(ctx, tx, recordTypeID, toStatusCode)
	if err != nil {
		return nil, ClientError{Msg: "Unknown target status."}
	}

	// A fresh lifecycle step clears any stale approval decision; moving a
	// pending employee to Active records it as approved when a chain existed.
	newApproval := approvalchain.StatusNone
	if curStatusCode == statusPending && toStatusCode == statusActive && approvalStatus == approvalchain.StatusApproved {
		newApproval = approvalchain.StatusApproved
	}
	if _, err := tx.Exec(ctx, `
		UPDATE employee SET
			employee_status = $2, employee_approval_status = $3, employee_is_active = $4,
			employee_updated_at = NOW(), employee_updated_by = $5,
			employee_record_version = employee_record_version + 1
		WHERE employee_id = $1`,
		internalID, toStatusID, newApproval, toStatusCode == statusActive, nullableInt(actorEmployeeID)); err != nil {
		return nil, fmt.Errorf("transition employee: %w", err)
	}
	if curStatusID != 0 {
		writeHistory(ctx, tx, internalID, "transition", &curStatusID, &toStatusID, actorEmployeeID)
	} else {
		writeHistory(ctx, tx, internalID, "transition", nil, &toStatusID, actorEmployeeID)
	}
	if err := approvalchain.ClearRejection(ctx, tx, recordTypeID, internalID); err != nil {
		return nil, err
	}
	if err := syncVendorTx(ctx, tx, internalID, actorEmployeeID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit transition employee: %w", err)
	}
	return Get(ctx, pool, uuid)
}
