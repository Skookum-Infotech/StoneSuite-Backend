package creditmemo

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// A credit memo can be issued from the overpayment sitting on a payment. That
// money is then the memo's, so the memo's grand total is taken out of what the
// payment can still be applied to an invoice or refunded (payment.Apply and
// refund both net payment_credited_total off).
//
// payment_credited_total is a rollup this package alone writes, exactly as
// refund/ alone writes payment_refunded_total. Lock order everywhere is
// credit_memo < payment < invoice.

// creditTolerance absorbs float noise when a memo total is compared with what
// is left on a payment (mirrors payment.Apply's 0.001).
const creditTolerance = 0.001

// lockSourcePayment locks the payment a new memo is drawn from and returns its
// internal id and how much of its overpayment is still free to credit
// (unapplied - refunded - credited). Validating inside the lock, rather than
// before the transaction, closes the window in which the payment could be
// voided, deleted or spent between the check and the memo insert.
func lockSourcePayment(ctx context.Context, tx pgx.Tx, paymentUUID string, customerID int) (int, float64, error) {
	var paymentID, paymentCustomerID int
	var statusCode string
	var unapplied, refunded, credited float64
	err := tx.QueryRow(ctx, `
		SELECT p.payment_id, p.payment_customer_id, rs.record_status_code,
		       p.payment_unapplied_amount, p.payment_refunded_total, p.payment_credited_total
		FROM payment p
		JOIN lkp_record_status rs ON rs.record_status_id = p.payment_status
		WHERE p.payment_uuid = $1 AND p.payment_deleted_at IS NULL
		FOR UPDATE OF p`, paymentUUID,
	).Scan(&paymentID, &paymentCustomerID, &statusCode, &unapplied, &refunded, &credited)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, 0, ClientError{Msg: "Unknown or deleted payment."}
	}
	if err != nil {
		return 0, 0, fmt.Errorf("lock source payment: %w", err)
	}
	if paymentCustomerID != customerID {
		return 0, 0, ClientError{Msg: "Payment belongs to a different customer than the credit memo."}
	}
	if statusCode == "VOID" {
		return 0, 0, ClientError{Msg: "Cannot credit a voided payment."}
	}
	return paymentID, round2(unapplied - refunded - credited), nil
}

// lockLinkedPayment locks the payment an existing memo was issued from and
// returns how much of its overpayment is free to credit, counting this memo's
// own current total as already taken.
func lockLinkedPayment(ctx context.Context, tx pgx.Tx, paymentID int) (float64, error) {
	var unapplied, refunded, credited float64
	if err := tx.QueryRow(ctx, `
		SELECT payment_unapplied_amount, payment_refunded_total, payment_credited_total
		FROM payment WHERE payment_id = $1 FOR UPDATE`, paymentID,
	).Scan(&unapplied, &refunded, &credited); err != nil {
		return 0, fmt.Errorf("lock linked payment: %w", err)
	}
	return round2(unapplied - refunded - credited), nil
}

// checkPaymentRoom rejects a memo whose total is more than the payment has left.
func checkPaymentRoom(grandTotal, room float64) error {
	if grandTotal > room+creditTolerance {
		return ClientError{Msg: fmt.Sprintf(
			"The payment only has %.2f left to credit; this credit memo totals %.2f.", room, grandTotal)}
	}
	return nil
}

// recomputePaymentCredited re-sums payment_credited_total from the payment's
// live, non-void linked credit memos. Call it inside the transaction that
// changed a memo's total, status or existence, with the payment already locked.
// Bumping payment_record_version mirrors refund's own recompute helpers.
func recomputePaymentCredited(ctx context.Context, tx pgx.Tx, paymentID, actorEmployeeID int) error {
	var credited float64
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(cm.credit_memo_grand_total), 0)
		FROM credit_memo cm
		JOIN lkp_record_status rs ON rs.record_status_id = cm.credit_memo_status
		WHERE cm.credit_memo_source_payment_id = $1
		  AND cm.credit_memo_deleted_at IS NULL
		  AND rs.record_status_code <> 'VOID'`, paymentID,
	).Scan(&credited); err != nil {
		return fmt.Errorf("sum credit memos issued from payment: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE payment SET payment_credited_total = $1,
			payment_updated_at = NOW(), payment_updated_by = $2, payment_record_version = payment_record_version + 1
		WHERE payment_id = $3`, round2(credited), nullableInt(actorEmployeeID), paymentID,
	); err != nil {
		return fmt.Errorf("update payment credited rollup: %w", err)
	}
	return nil
}
