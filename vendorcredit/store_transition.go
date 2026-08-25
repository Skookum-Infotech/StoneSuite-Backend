// vendorcredit/store_transition.go
package vendorcredit

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/approvalchain"
	"stonesuite-backend/vendorbill"
)

// Transition moves a vendor credit to toStatusCode after validating the move
// against the static transition map. Moving to VOID first reverses every
// live application on this credit, restoring each affected vendor bill's
// balance.
//
// VOID is only reachable from DRFT/APPV -- an APPL credit must be reversed
// first -- so the cascade below only ever runs on a partially applied APPV
// credit.
func Transition(ctx context.Context, pool *pgxpool.Pool, id, toStatusCode string, actorEmployeeID int) (*VendorCredit, error) {
	internalID, curStatusCode, err := internalIDByUUID(ctx, pool, id)
	if err != nil {
		return nil, err
	}
	if err := ValidateTransition(curStatusCode, toStatusCode); err != nil {
		return nil, err
	}
	recordTypeID, err := recordTypeIDByCode(ctx, pool, vcrdRecordTypeCode)
	if err != nil {
		return nil, fmt.Errorf("resolve VCRD record type: %w", err)
	}
	fromStatusID, err := statusIDByCode(ctx, pool, recordTypeID, curStatusCode)
	if err != nil {
		return nil, fmt.Errorf("resolve current status: %w", err)
	}
	toStatusID, err := statusIDByCode(ctx, pool, recordTypeID, toStatusCode)
	if err != nil {
		return nil, ClientError{Msg: "Unknown target status: " + toStatusCode}
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transition: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Lock the credit first -- the global order is vendor_credit < vendor_bill.
	var approvalStatus string
	if err := tx.QueryRow(ctx,
		`SELECT vendor_credit_approval_status FROM vendor_credit WHERE vendor_credit_id = $1 FOR UPDATE`, internalID,
	).Scan(&approvalStatus); err != nil {
		return nil, fmt.Errorf("lock vendor credit for transition: %w", err)
	}

	// AD-8 approval gate: a vendor credit may not leave a status that has
	// configured approvers until it has been approved (except into an
	// always-allowed exit like Void -- approvalchain.AlwaysAllowedExitCodes).
	// Vendor Credit has no separate pending status -- the gate sits on DRFT
	// itself, so Void stays reachable from an unapproved draft.
	approverTable := moduleConfig().ApproverTable
	requiredHere, err := approvalchain.ActiveApproverCount(ctx, tx, approverTable, recordTypeID, fromStatusID)
	if err != nil {
		return nil, err
	}
	if err := approvalchain.CheckTransitionGate(requiredHere, approvalStatus, toStatusCode); err != nil {
		return nil, ErrApprovalRequired
	}
	targetApprovers, err := approvalchain.ActiveApproverCount(ctx, tx, approverTable, recordTypeID, toStatusID)
	if err != nil {
		return nil, err
	}
	newApprovalStatus := approvalchain.StatusNone
	if targetApprovers > 0 {
		newApprovalStatus = approvalchain.StatusPending
	}

	if toStatusCode == "VOID" {
		// ORDER BY vendor_bill_id fixes a global lock order across bills so two
		// concurrent VOID cascades touching the same two bills can't lock them
		// in opposite orders and deadlock.
		rows, err := tx.Query(ctx, `
			SELECT vendor_bill_id FROM vendor_credit_application
			WHERE vendor_credit_id = $1 AND application_deleted_at IS NULL ORDER BY vendor_bill_id`, internalID)
		if err != nil {
			return nil, fmt.Errorf("list live applications: %w", err)
		}
		var billInternalIDs []int
		for rows.Next() {
			var bid int
			if err := rows.Scan(&bid); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scan application vendor bill id: %w", err)
			}
			billInternalIDs = append(billInternalIDs, bid)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("list live applications: %w", err)
		}

		for _, billInternalID := range billInternalIDs {
			l, err := vendorbill.LockForUpdateByID(ctx, tx, billInternalID)
			if err != nil {
				return nil, err
			}
			if _, err := tx.Exec(ctx, `
				UPDATE vendor_credit_application SET application_deleted_at = NOW(), application_deleted_by = $1
				WHERE vendor_credit_id = $2 AND vendor_bill_id = $3 AND application_deleted_at IS NULL`,
				actorOrSystem(actorEmployeeID), internalID, billInternalID); err != nil {
				return nil, fmt.Errorf("cascade-reverse: %w", err)
			}
			if err := vendorbill.RecomputeBalance(ctx, tx, l, "reverse", actorEmployeeID); err != nil {
				return nil, err
			}
		}
		if len(billInternalIDs) > 0 {
			// Every live application on this credit was just reversed, so its
			// own rollup needs resetting. Do it directly rather than through a
			// recompute helper: that would re-derive the status back to APPV,
			// and this transition is on its way to VOID.
			if _, err := tx.Exec(ctx, `
				UPDATE vendor_credit SET vendor_credit_applied_total = 0,
					vendor_credit_unapplied_amount = vendor_credit_grand_total
				WHERE vendor_credit_id = $1`, internalID); err != nil {
				return nil, fmt.Errorf("reset vendor credit rollup on void: %w", err)
			}
		}
	}

	if _, err := tx.Exec(ctx, `
		UPDATE vendor_credit SET vendor_credit_status = $1, vendor_credit_approval_status = $2, vendor_credit_approved_by = NULL,
			vendor_credit_updated_at = NOW(), vendor_credit_updated_by = $3, vendor_credit_record_version = vendor_credit_record_version + 1
		WHERE vendor_credit_id = $4`,
		toStatusID, newApprovalStatus, nullableInt(actorEmployeeID), internalID); err != nil {
		return nil, fmt.Errorf("update vendor credit status: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO vendor_credit_history (vendor_credit_id, from_status_id, to_status_id, action, actor_employee_id)
		VALUES ($1, $2, $3, 'transition', $4)`,
		internalID, fromStatusID, toStatusID, nullableInt(actorEmployeeID)); err != nil {
		return nil, fmt.Errorf("insert vendor credit transition history: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit transition: %w", err)
	}
	return Get(ctx, pool, id)
}
