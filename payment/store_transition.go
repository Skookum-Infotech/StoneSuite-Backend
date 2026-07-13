package payment

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Transition moves a payment to toStatusCode after validating the move
// against the static transition map. Moving to VOID first reverses every
// live application on this payment (spec AD-9) — each reversal is its own
// Unapply-shaped step inside the same transaction as the status change.
func Transition(ctx context.Context, pool *pgxpool.Pool, id, toStatusCode string, actorEmployeeID int) (*Payment, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transition: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var internalID, curStatusID, typeID int
	var curStatusCode string
	err = tx.QueryRow(ctx, `
		SELECT p.payment_id, p.payment_status, p.record_type, rs.record_status_code
		FROM payment p
		JOIN lkp_record_status rs ON rs.record_status_id = p.payment_status
		WHERE p.payment_uuid = $1 AND p.payment_deleted_at IS NULL
		FOR UPDATE OF p`, id,
	).Scan(&internalID, &curStatusID, &typeID, &curStatusCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("resolve payment for transition: %w", err)
	}
	if err := ValidateTransition(curStatusCode, toStatusCode); err != nil {
		return nil, err
	}
	toStatusID, err := statusIDByCode(ctx, pool, typeID, toStatusCode)
	if err != nil {
		return nil, ClientError{Msg: "Unknown target status: " + toStatusCode}
	}

	if toStatusCode == "VOID" {
		rows, err := tx.Query(ctx, `SELECT invoice_id FROM payment_application WHERE payment_id = $1 AND application_deleted_at IS NULL`, internalID)
		if err != nil {
			return nil, fmt.Errorf("list live applications: %w", err)
		}
		var invoiceInternalIDs []int
		for rows.Next() {
			var iid int
			if err := rows.Scan(&iid); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scan application invoice id: %w", err)
			}
			invoiceInternalIDs = append(invoiceInternalIDs, iid)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("list live applications: %w", err)
		}

		for _, invInternalID := range invoiceInternalIDs {
			var li lockedInvoice
			li.internalID = invInternalID
			if err := tx.QueryRow(ctx, `
				SELECT rs.record_status_code, i.invoice_grand_total, i.invoice_amount_paid
				FROM invoice i JOIN lkp_record_status rs ON rs.record_status_id = i.invoice_status
				WHERE i.invoice_id = $1 FOR UPDATE OF i`, invInternalID,
			).Scan(&li.statusCode, &li.grandTotal, &li.amountPaid); err != nil {
				return nil, fmt.Errorf("lock invoice for void cascade: %w", err)
			}
			if _, err := tx.Exec(ctx, `
				UPDATE payment_application SET application_deleted_at = NOW(), application_deleted_by = $1
				WHERE payment_id = $2 AND invoice_id = $3 AND application_deleted_at IS NULL`,
				actorOrSystem(actorEmployeeID), internalID, invInternalID); err != nil {
				return nil, fmt.Errorf("cascade-unapply: %w", err)
			}
			if err := recomputeInvoice(ctx, tx, li, "unapply", actorEmployeeID); err != nil {
				return nil, err
			}
		}
		if len(invoiceInternalIDs) > 0 {
			// Every live application on this payment was just reversed above, so
			// the payment's own rollup needs recomputing too. recomputePayment
			// needs the payment's amount (not yet in scope here), so load it first.
			var amt float64
			if err := tx.QueryRow(ctx, `SELECT payment_amount FROM payment WHERE payment_id = $1`, internalID).Scan(&amt); err != nil {
				return nil, fmt.Errorf("reload payment amount: %w", err)
			}
			if err := recomputePayment(ctx, tx, internalID, amt, actorEmployeeID); err != nil {
				return nil, err
			}
		}
	}

	if _, err := tx.Exec(ctx, `
		UPDATE payment SET payment_status = $1, payment_updated_at = NOW(),
			payment_updated_by = $2, payment_record_version = payment_record_version + 1
		WHERE payment_id = $3`, toStatusID, nullableInt(actorEmployeeID), internalID); err != nil {
		return nil, fmt.Errorf("update payment status: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO payment_history (payment_id, from_status_id, to_status_id, action, actor_employee_id)
		VALUES ($1, $2, $3, 'transition', $4)`, internalID, curStatusID, toStatusID, nullableInt(actorEmployeeID)); err != nil {
		return nil, fmt.Errorf("insert payment transition history: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit transition: %w", err)
	}
	return Get(ctx, pool, id)
}
