// vendorpayment/store_transition.go
package vendorpayment

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/approvalchain"
	"stonesuite-backend/vendorbill"
)

// Transition moves a vendor payment to toStatusCode after validating the
// move against the static transition map (spec §7), enforcing the AD-6
// approval gate the same way every other module built on the approvalchain
// engine does (a manual PAPV->APPV move 409s with ErrApprovalRequired once
// real approvers are configured for PAPV; use Approve instead). Moving to
// SCHD requires vendor_payment_scheduled_date to already be set. Moving to
// VOID cascades: every live vendor_payment_application on this payment is
// reversed, and every live vendor_payment_refund on it is soft-deleted (moot
// once its application is reversed), in the same transaction — mirroring
// payment.Transition's VOID cascade (spec AD-8).
func Transition(ctx context.Context, pool *pgxpool.Pool, id, toStatusCode string, actorEmployeeID int) (*VendorPayment, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transition: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var internalID, curStatusID, typeID int
	var curStatusCode, approvalStatus string
	var scheduledDate *time.Time
	err = tx.QueryRow(ctx, `
		SELECT vp.vendor_payment_id, vp.vendor_payment_status, vp.record_type, rs.record_status_code,
		       vp.vendor_payment_approval_status, vp.vendor_payment_scheduled_date
		FROM vendor_payment vp
		JOIN lkp_record_status rs ON rs.record_status_id = vp.vendor_payment_status
		WHERE vp.vendor_payment_uuid = $1 AND vp.vendor_payment_deleted_at IS NULL
		FOR UPDATE OF vp`, id,
	).Scan(&internalID, &curStatusID, &typeID, &curStatusCode, &approvalStatus, &scheduledDate)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("resolve vendor payment for transition: %w", err)
	}
	if err := ValidateTransition(curStatusCode, toStatusCode); err != nil {
		return nil, err
	}
	approverTable := moduleConfig().ApproverTable
	requiredHere, err := approvalchain.ActiveApproverCount(ctx, tx, approverTable, typeID, curStatusID)
	if err != nil {
		return nil, err
	}
	if err := approvalchain.CheckTransitionGate(requiredHere, approvalStatus, toStatusCode); err != nil {
		return nil, ErrApprovalRequired
	}
	if toStatusCode == "SCHD" && scheduledDate == nil {
		return nil, ClientError{Msg: "A scheduled date is required before scheduling this payment."}
	}
	toStatusID, err := statusIDByCode(ctx, pool, typeID, toStatusCode)
	if err != nil {
		return nil, ClientError{Msg: "Unknown target status: " + toStatusCode}
	}

	if toStatusCode == "VOID" {
		// ORDER BY vendor_bill_id fixes a global lock order across bills so two
		// concurrent VOID cascades touching the same two bills can't lock them
		// in opposite orders and deadlock.
		rows, err := tx.Query(ctx, `SELECT vendor_bill_id FROM vendor_payment_application WHERE vendor_payment_id = $1 AND application_deleted_at IS NULL ORDER BY vendor_bill_id`, internalID)
		if err != nil {
			return nil, fmt.Errorf("list live vendor payment applications: %w", err)
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
			return nil, fmt.Errorf("list live vendor payment applications: %w", err)
		}

		for _, billInternalID := range billInternalIDs {
			li, err := vendorbill.LockForUpdateByID(ctx, tx, billInternalID)
			if err != nil {
				return nil, err
			}
			if _, err := tx.Exec(ctx, `
				UPDATE vendor_payment_application SET application_deleted_at = NOW(), application_deleted_by = $1
				WHERE vendor_payment_id = $2 AND vendor_bill_id = $3 AND application_deleted_at IS NULL`,
				actorOrSystem(actorEmployeeID), internalID, billInternalID); err != nil {
				return nil, fmt.Errorf("cascade-unapply: %w", err)
			}
			if err := vendorbill.RecomputeBalance(ctx, tx, li, "unapply", actorEmployeeID); err != nil {
				return nil, err
			}
		}

		// Every live refund on this payment is moot once its application is
		// reversed above -- soft-delete them too.
		if _, err := tx.Exec(ctx, `
			UPDATE vendor_payment_refund SET refund_deleted_at = NOW(), refund_deleted_by = $1
			WHERE vendor_payment_id = $2 AND refund_deleted_at IS NULL`,
			actorOrSystem(actorEmployeeID), internalID); err != nil {
			return nil, fmt.Errorf("cascade-unrefund: %w", err)
		}

		if len(billInternalIDs) > 0 {
			// Every live application on this payment was just reversed above, so
			// the payment's own rollup needs recomputing too. recomputeVendorPayment
			// needs the payment's amount (not yet in scope here), so load it first.
			var amt float64
			if err := tx.QueryRow(ctx, `SELECT vendor_payment_amount FROM vendor_payment WHERE vendor_payment_id = $1`, internalID).Scan(&amt); err != nil {
				return nil, fmt.Errorf("reload vendor payment amount: %w", err)
			}
			if err := recomputeVendorPayment(ctx, tx, internalID, amt, actorEmployeeID); err != nil {
				return nil, err
			}
		}
	}

	newApprovalStatus := approvalchain.StatusNone
	targetApprovers, err := approvalchain.ActiveApproverCount(ctx, tx, approverTable, typeID, toStatusID)
	if err != nil {
		return nil, err
	}
	if targetApprovers > 0 {
		newApprovalStatus = approvalchain.StatusPending
	}

	if _, err := tx.Exec(ctx, `
		UPDATE vendor_payment SET vendor_payment_status = $1, vendor_payment_approval_status = $2, vendor_payment_approved_by = NULL,
			vendor_payment_updated_at = NOW(), vendor_payment_updated_by = $3, vendor_payment_record_version = vendor_payment_record_version + 1
		WHERE vendor_payment_id = $4`, toStatusID, newApprovalStatus, nullableInt(actorEmployeeID), internalID); err != nil {
		return nil, fmt.Errorf("update vendor payment status: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO vendor_payment_history (vendor_payment_id, from_status_id, to_status_id, action, actor_employee_id)
		VALUES ($1, $2, $3, 'transition', $4)`, internalID, curStatusID, toStatusID, nullableInt(actorEmployeeID)); err != nil {
		return nil, fmt.Errorf("insert vendor payment transition history: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit transition: %w", err)
	}
	return Get(ctx, pool, id)
}
