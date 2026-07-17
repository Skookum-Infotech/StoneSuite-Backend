package refund

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// lockedRefund is the row state read under FOR UPDATE at the start of Apply/Unapply.
type lockedRefund struct {
	internalID int
	customerID int
	statusCode string
	amount     float64
}

func lockRefundForUpdate(ctx context.Context, tx pgx.Tx, refundUUID string) (lockedRefund, error) {
	var lr lockedRefund
	err := tx.QueryRow(ctx, `
		SELECT rfnd.refund_id, rfnd.refund_customer_id, rs.record_status_code, rfnd.refund_amount
		FROM refund rfnd
		JOIN lkp_record_status rs ON rs.record_status_id = rfnd.refund_status
		WHERE rfnd.refund_uuid = $1 AND rfnd.refund_deleted_at IS NULL
		FOR UPDATE OF rfnd`, refundUUID,
	).Scan(&lr.internalID, &lr.customerID, &lr.statusCode, &lr.amount)
	if errors.Is(err, pgx.ErrNoRows) {
		return lockedRefund{}, ErrNotFound
	}
	if err != nil {
		return lockedRefund{}, fmt.Errorf("lock refund: %w", err)
	}
	return lr, nil
}

// lockedSource is a payment or credit_memo row, read under FOR UPDATE, that a
// refund is about to draw down against (AD-2). available is the source's
// unapplied balance net of what refunds have already taken from it.
type lockedSource struct {
	internalID int
	customerID int
	statusCode string
	available  float64
}

func lockPaymentSource(ctx context.Context, tx pgx.Tx, paymentUUID string) (lockedSource, error) {
	var ls lockedSource
	var unapplied, refunded float64
	err := tx.QueryRow(ctx, `
		SELECT p.payment_id, p.payment_customer_id, rs.record_status_code, p.payment_unapplied_amount, p.payment_refunded_total
		FROM payment p
		JOIN lkp_record_status rs ON rs.record_status_id = p.payment_status
		WHERE p.payment_uuid = $1 AND p.payment_deleted_at IS NULL
		FOR UPDATE OF p`, paymentUUID,
	).Scan(&ls.internalID, &ls.customerID, &ls.statusCode, &unapplied, &refunded)
	if errors.Is(err, pgx.ErrNoRows) {
		return lockedSource{}, ClientError{Msg: "Unknown or deleted payment."}
	}
	if err != nil {
		return lockedSource{}, fmt.Errorf("lock payment source: %w", err)
	}
	ls.available = round2(unapplied - refunded)
	return ls, nil
}

func lockCreditMemoSource(ctx context.Context, tx pgx.Tx, creditMemoUUID string) (lockedSource, error) {
	var ls lockedSource
	var unapplied, refunded float64
	err := tx.QueryRow(ctx, `
		SELECT cm.credit_memo_id, cm.credit_memo_customer_id, rs.record_status_code, cm.credit_memo_unapplied_amount, cm.credit_memo_refunded_total
		FROM credit_memo cm
		JOIN lkp_record_status rs ON rs.record_status_id = cm.credit_memo_status
		WHERE cm.credit_memo_uuid = $1 AND cm.credit_memo_deleted_at IS NULL
		FOR UPDATE OF cm`, creditMemoUUID,
	).Scan(&ls.internalID, &ls.customerID, &ls.statusCode, &unapplied, &refunded)
	if errors.Is(err, pgx.ErrNoRows) {
		return lockedSource{}, ClientError{Msg: "Unknown or deleted credit memo."}
	}
	if err != nil {
		return lockedSource{}, fmt.Errorf("lock credit memo source: %w", err)
	}
	ls.available = round2(unapplied - refunded)
	return ls, nil
}

// recomputeRefund recomputes and stores refund_applied_total/unapplied_amount
// from the live refund_application rows, inside tx.
func recomputeRefund(ctx context.Context, tx pgx.Tx, internalID int, amount float64, actorEmployeeID int) error {
	var applied float64
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(application_amount), 0) FROM refund_application
		WHERE refund_id = $1 AND application_deleted_at IS NULL`, internalID).Scan(&applied); err != nil {
		return fmt.Errorf("sum refund applications: %w", err)
	}
	applied = round2(applied)
	unapplied := round2(amount - applied)
	if _, err := tx.Exec(ctx, `
		UPDATE refund SET refund_applied_total = $1, refund_unapplied_amount = $2,
			refund_updated_at = NOW(), refund_updated_by = $3, refund_record_version = refund_record_version + 1
		WHERE refund_id = $4`, applied, unapplied, nullableInt(actorEmployeeID), internalID); err != nil {
		return fmt.Errorf("update refund rollup: %w", err)
	}
	return nil
}

// recomputePaymentRefunded re-sums payment_refunded_total from this payment's
// live refund_application rows (AD-2). This is the sole writer of that
// column; payment's own Go code never reads or writes it, so there is no
// shared invariant to protect — unlike invoice.RecomputeBalance, this stays a
// private helper inside refund/. Bumping payment_record_version on this write
// mirrors invoice.RecomputeBalance, whose cross-module writers likewise bump
// the target's own version counter.
func recomputePaymentRefunded(ctx context.Context, tx pgx.Tx, paymentInternalID int, actorEmployeeID int) error {
	var refunded float64
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(application_amount), 0) FROM refund_application
		WHERE payment_id = $1 AND application_deleted_at IS NULL`, paymentInternalID).Scan(&refunded); err != nil {
		return fmt.Errorf("sum payment refund applications: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE payment SET payment_refunded_total = $1,
			payment_updated_at = NOW(), payment_updated_by = $2, payment_record_version = payment_record_version + 1
		WHERE payment_id = $3`, round2(refunded), nullableInt(actorEmployeeID), paymentInternalID); err != nil {
		return fmt.Errorf("update payment refunded rollup: %w", err)
	}
	return nil
}

// recomputeCreditMemoRefunded mirrors recomputePaymentRefunded for the
// credit-memo source.
func recomputeCreditMemoRefunded(ctx context.Context, tx pgx.Tx, creditMemoInternalID int, actorEmployeeID int) error {
	var refunded float64
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(application_amount), 0) FROM refund_application
		WHERE credit_memo_id = $1 AND application_deleted_at IS NULL`, creditMemoInternalID).Scan(&refunded); err != nil {
		return fmt.Errorf("sum credit memo refund applications: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE credit_memo SET credit_memo_refunded_total = $1,
			credit_memo_updated_at = NOW(), credit_memo_updated_by = $2, credit_memo_record_version = credit_memo_record_version + 1
		WHERE credit_memo_id = $3`, round2(refunded), nullableInt(actorEmployeeID), creditMemoInternalID); err != nil {
		return fmt.Errorf("update credit memo refunded rollup: %w", err)
	}
	return nil
}

// Apply allocates amount of refundUUID's unapplied balance against exactly
// one source — either paymentUUID or creditMemoUUID, never both (AD-2's XOR).
// Caps at min(refund.unapplied_amount, source.available); rejects (400)
// rather than clamping if amount exceeds that cap (AD-6). Requires the
// refund be APPV (AD-5) and, for a credit-memo source, that the credit memo
// itself be APPV or APPL (an unapproved or voided memo authorizes nothing).
func Apply(ctx context.Context, pool *pgxpool.Pool, refundUUID, paymentUUID, creditMemoUUID string, amount float64, actorEmployeeID int) (*Refund, error) {
	if amount <= 0 {
		return nil, ClientError{Msg: "amount must be positive."}
	}
	if (paymentUUID == "") == (creditMemoUUID == "") {
		return nil, ClientError{Msg: "exactly one of paymentUuid or creditMemoUuid is required."}
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin apply: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	lr, err := lockRefundForUpdate(ctx, tx, refundUUID) // lock order: refund first (AD-7)
	if err != nil {
		return nil, err
	}
	if lr.statusCode != "APPV" {
		return nil, ClientError{Msg: "Refund must be approved before it can be applied."}
	}

	var src lockedSource
	var isPayment bool
	if paymentUUID != "" {
		isPayment = true
		src, err = lockPaymentSource(ctx, tx, paymentUUID)
		if err != nil {
			return nil, err
		}
		if src.statusCode == "VOID" {
			return nil, ClientError{Msg: "Cannot draw a refund from a voided payment."}
		}
	} else {
		src, err = lockCreditMemoSource(ctx, tx, creditMemoUUID)
		if err != nil {
			return nil, err
		}
		if src.statusCode != "APPV" && src.statusCode != "APPL" {
			return nil, ClientError{Msg: "Cannot draw a refund from a credit memo that is not approved."}
		}
	}
	if src.customerID != lr.customerID {
		return nil, ClientError{Msg: "Source belongs to a different customer than the refund."}
	}

	capAmt := lr.amount - (func() float64 {
		var applied float64
		_ = tx.QueryRow(ctx, `SELECT COALESCE(SUM(application_amount),0) FROM refund_application WHERE refund_id = $1 AND application_deleted_at IS NULL`, lr.internalID).Scan(&applied)
		return applied
	})()
	if src.available < capAmt {
		capAmt = src.available
	}
	capAmt = round2(capAmt)
	if amount > capAmt+0.001 {
		return nil, ClientError{Msg: "Amount exceeds available balance."}
	}

	var existingID int
	var queryErr error
	if isPayment {
		queryErr = tx.QueryRow(ctx, `SELECT application_id FROM refund_application WHERE refund_id = $1 AND payment_id = $2 AND application_deleted_at IS NULL`,
			lr.internalID, src.internalID).Scan(&existingID)
	} else {
		queryErr = tx.QueryRow(ctx, `SELECT application_id FROM refund_application WHERE refund_id = $1 AND credit_memo_id = $2 AND application_deleted_at IS NULL`,
			lr.internalID, src.internalID).Scan(&existingID)
	}
	switch {
	case errors.Is(queryErr, pgx.ErrNoRows):
		if isPayment {
			_, err = tx.Exec(ctx, `
				INSERT INTO refund_application (refund_id, payment_id, application_amount, application_created_by)
				VALUES ($1,$2,$3,$4)`, lr.internalID, src.internalID, round2(amount), nullableInt(actorEmployeeID))
		} else {
			_, err = tx.Exec(ctx, `
				INSERT INTO refund_application (refund_id, credit_memo_id, application_amount, application_created_by)
				VALUES ($1,$2,$3,$4)`, lr.internalID, src.internalID, round2(amount), nullableInt(actorEmployeeID))
		}
		if err != nil {
			return nil, fmt.Errorf("insert refund application: %w", err)
		}
	case queryErr != nil:
		return nil, fmt.Errorf("check existing application: %w", queryErr)
	default:
		if _, err := tx.Exec(ctx, `
			UPDATE refund_application SET application_amount = application_amount + $1, application_record_version = application_record_version + 1
			WHERE application_id = $2`, round2(amount), existingID); err != nil {
			return nil, fmt.Errorf("increase refund application: %w", err)
		}
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
		SELECT refund_id, refund_status, refund_status, 'apply', $2 FROM refund WHERE refund_id = $1`,
		lr.internalID, nullableInt(actorEmployeeID)); err != nil {
		return nil, fmt.Errorf("insert refund apply history: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit apply: %w", err)
	}
	return Get(ctx, pool, refundUUID)
}

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

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit unapply: %w", err)
	}
	return Get(ctx, pool, refundUUID)
}
