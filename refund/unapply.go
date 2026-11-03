package refund

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Unapply reverses the live application between refundUUID and exactly one
// of paymentUUID/creditMemoUUID (soft-deletes it), recomputing both sides. No
// refund-status gate: a reversal must be possible regardless of the refund's
// current status (mirrors payment.Unapply).
func Unapply(ctx context.Context, pool *pgxpool.Pool, refundUUID, paymentUUID, creditMemoUUID string, actorEmployeeID int) (*Refund, error) {
	if (paymentUUID == "") == (creditMemoUUID == "") {
		return nil, ClientError{Msg: "exactly one of paymentUuid or creditMemoUuid is required."}
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin unapply: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	lr, err := lockRefundForUpdate(ctx, tx, refundUUID)
	if err != nil {
		return nil, err
	}
	before, err := getQ(ctx, tx, refundUUID)
	if err != nil {
		return nil, err
	}

	var src lockedSource
	var isPayment bool
	if paymentUUID != "" {
		isPayment = true
		src, err = lockPaymentSource(ctx, tx, paymentUUID)
	} else {
		src, err = lockCreditMemoSource(ctx, tx, creditMemoUUID)
	}
	if err != nil {
		return nil, err
	}

	sourceCol := "credit_memo_id"
	if isPayment {
		sourceCol = "payment_id"
	}
	tag, err := tx.Exec(ctx, `
		UPDATE refund_application SET application_deleted_at = NOW(), application_deleted_by = $1
		WHERE refund_id = $2 AND `+sourceCol+` = $3 AND application_deleted_at IS NULL`,
		actorOrSystem(actorEmployeeID), lr.internalID, src.internalID)
	if err != nil {
		return nil, fmt.Errorf("unapply: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ClientError{Msg: "No live application between this refund and source."}
	}

	if err := recomputeRefund(ctx, tx, lr.internalID, lr.amount, actorEmployeeID); err != nil {
		return nil, err
	}
	if isPayment {
		if err := recomputePaymentRefunded(ctx, tx, src.internalID, actorEmployeeID); err != nil {
			return nil, err
		}
	} else {
		if err := recomputeCreditMemoRefunded(ctx, tx, src.internalID, actorEmployeeID); err != nil {
			return nil, err
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO refund_history (refund_id, from_status_id, to_status_id, action, actor_employee_id)
		SELECT refund_id, refund_status, refund_status, 'unapply', $2 FROM refund WHERE refund_id = $1`,
		lr.internalID, nullableInt(actorEmployeeID)); err != nil {
		return nil, fmt.Errorf("insert refund unapply history: %w", err)
	}

	if err := auditTx(ctx, tx, auditUnapply, refundUUID, before, employeeExtra(actorEmployeeID)); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit unapply: %w", err)
	}
	return Get(ctx, pool, refundUUID)
}
