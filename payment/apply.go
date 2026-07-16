package payment

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/invoice"
)

// systemEmployeeID is the fallback actor for soft-delete columns that must
// never be NULL when their paired *_deleted_at timestamp is set (enforced by
// a CHECK constraint) — used when the caller has no resolvable employee id.
const systemEmployeeID = 1

// actorOrSystem returns actorEmployeeID, or systemEmployeeID if it's unset
// (0). Use this — never nullableInt — for any *_deleted_by column paired
// with a NOT NULL *_deleted_at via a CHECK constraint.
func actorOrSystem(actorEmployeeID int) int {
	if actorEmployeeID == 0 {
		return systemEmployeeID
	}
	return actorEmployeeID
}

type lockedPayment struct {
	internalID int
	customerID int
	statusCode string
	amount     float64
}

func lockPaymentForUpdate(ctx context.Context, tx pgx.Tx, paymentUUID string) (lockedPayment, error) {
	var lp lockedPayment
	err := tx.QueryRow(ctx, `
		SELECT p.payment_id, p.payment_customer_id, rs.record_status_code, p.payment_amount
		FROM payment p
		JOIN lkp_record_status rs ON rs.record_status_id = p.payment_status
		WHERE p.payment_uuid = $1 AND p.payment_deleted_at IS NULL
		FOR UPDATE OF p`, paymentUUID,
	).Scan(&lp.internalID, &lp.customerID, &lp.statusCode, &lp.amount)
	if errors.Is(err, pgx.ErrNoRows) {
		return lockedPayment{}, ErrNotFound
	}
	if err != nil {
		return lockedPayment{}, fmt.Errorf("lock payment: %w", err)
	}
	return lp, nil
}

// recomputePayment recomputes and stores payment_applied_total/unapplied_amount
// from the live payment_application rows, inside tx.
func recomputePayment(ctx context.Context, tx pgx.Tx, internalID int, amount float64, actorEmployeeID int) error {
	var applied float64
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(application_amount), 0) FROM payment_application
		WHERE payment_id = $1 AND application_deleted_at IS NULL`, internalID).Scan(&applied); err != nil {
		return fmt.Errorf("sum payment applications: %w", err)
	}
	applied = round2(applied)
	unapplied := round2(amount - applied)
	if _, err := tx.Exec(ctx, `
		UPDATE payment SET payment_applied_total = $1, payment_unapplied_amount = $2,
			payment_updated_at = NOW(), payment_updated_by = $3, payment_record_version = payment_record_version + 1
		WHERE payment_id = $4`, applied, unapplied, nullableInt(actorEmployeeID), internalID); err != nil {
		return fmt.Errorf("update payment rollup: %w", err)
	}
	return nil
}

// The invoice AR rollup -- and the status derived from it -- now lives in
// invoice.RecomputeBalance / invoice.DeriveStatus, because credit memos write
// it too (credit_memo_application). Keeping a second copy here is how the cash
// and credit ledgers would silently drift apart.

// Apply allocates amount of paymentUUID's unapplied balance to invoiceUUID.
// Caps at min(payment.unapplied_amount, invoice.balance_due); rejects (400)
// rather than clamping if amount exceeds that cap (spec AD-8). Rejects (409)
// if the payment is VOID or the invoice isn't in a payable status, and (400)
// on a customer mismatch.
func Apply(ctx context.Context, pool *pgxpool.Pool, paymentUUID, invoiceUUID string, amount float64, actorEmployeeID int) (*Payment, error) {
	if amount <= 0 {
		return nil, ClientError{Msg: "amount must be positive."}
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin apply: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	lp, err := lockPaymentForUpdate(ctx, tx, paymentUUID) // lock order: payment first
	if err != nil {
		return nil, err
	}
	if lp.statusCode == "VOID" {
		return nil, ClientError{Msg: "Cannot apply a voided payment."}
	}
	li, err := invoice.LockForUpdate(ctx, tx, invoiceUUID) // then invoice
	if err != nil {
		return nil, err
	}
	if li.CustomerID != lp.customerID {
		return nil, ClientError{Msg: "Invoice belongs to a different customer than the payment."}
	}
	if !invoice.PayableStatuses[li.StatusCode] {
		return nil, ClientError{Msg: "Cannot apply payment to a " + li.StatusCode + " invoice; it must be sent first."}
	}

	var applied float64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(SUM(application_amount),0) FROM payment_application WHERE payment_id = $1 AND application_deleted_at IS NULL`, lp.internalID).Scan(&applied); err != nil {
		return nil, fmt.Errorf("sum payment applications: %w", err)
	}
	unapplied := round2(lp.amount - applied)
	// BalanceDue nets off credit memos as well as cash, so a credited invoice
	// cannot be overpaid.
	invoiceBalance := li.BalanceDue()
	capAmt := unapplied
	if invoiceBalance < capAmt {
		capAmt = invoiceBalance
	}
	if amount > capAmt+0.001 {
		return nil, ClientError{Msg: "Amount exceeds available balance."}
	}

	var existingID int
	err = tx.QueryRow(ctx, `SELECT application_id FROM payment_application WHERE payment_id = $1 AND invoice_id = $2 AND application_deleted_at IS NULL`,
		lp.internalID, li.InternalID).Scan(&existingID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		if _, err := tx.Exec(ctx, `
			INSERT INTO payment_application (payment_id, invoice_id, application_amount, application_created_by)
			VALUES ($1,$2,$3,$4)`, lp.internalID, li.InternalID, round2(amount), nullableInt(actorEmployeeID)); err != nil {
			return nil, fmt.Errorf("insert payment application: %w", err)
		}
	case err != nil:
		return nil, fmt.Errorf("check existing application: %w", err)
	default:
		if _, err := tx.Exec(ctx, `
			UPDATE payment_application SET application_amount = application_amount + $1, application_record_version = application_record_version + 1
			WHERE application_id = $2`, round2(amount), existingID); err != nil {
			return nil, fmt.Errorf("increase payment application: %w", err)
		}
	}

	if err := recomputePayment(ctx, tx, lp.internalID, lp.amount, actorEmployeeID); err != nil {
		return nil, err
	}
	if err := invoice.RecomputeBalance(ctx, tx, li, "payment", actorEmployeeID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO payment_history (payment_id, from_status_id, to_status_id, action, actor_employee_id)
		SELECT payment_id, payment_status, payment_status, 'apply', $2 FROM payment WHERE payment_id = $1`,
		lp.internalID, nullableInt(actorEmployeeID)); err != nil {
		return nil, fmt.Errorf("insert payment apply history: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit apply: %w", err)
	}
	return Get(ctx, pool, paymentUUID)
}

// Unapply reverses the live application between paymentUUID and invoiceUUID
// (soft-deletes it), recomputing both rollups. No invoice-status gate: a
// reversal must be possible regardless of the invoice's current status.
func Unapply(ctx context.Context, pool *pgxpool.Pool, paymentUUID, invoiceUUID string, actorEmployeeID int) (*Payment, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin unapply: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	lp, err := lockPaymentForUpdate(ctx, tx, paymentUUID)
	if err != nil {
		return nil, err
	}
	li, err := invoice.LockForUpdate(ctx, tx, invoiceUUID)
	if err != nil {
		return nil, err
	}

	tag, err := tx.Exec(ctx, `
		UPDATE payment_application SET application_deleted_at = NOW(), application_deleted_by = $1
		WHERE payment_id = $2 AND invoice_id = $3 AND application_deleted_at IS NULL`,
		actorOrSystem(actorEmployeeID), lp.internalID, li.InternalID)
	if err != nil {
		return nil, fmt.Errorf("unapply: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ClientError{Msg: "No live application between this payment and invoice."}
	}

	if err := recomputePayment(ctx, tx, lp.internalID, lp.amount, actorEmployeeID); err != nil {
		return nil, err
	}
	if err := invoice.RecomputeBalance(ctx, tx, li, "unapply", actorEmployeeID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO payment_history (payment_id, from_status_id, to_status_id, action, actor_employee_id)
		SELECT payment_id, payment_status, payment_status, 'unapply', $2 FROM payment WHERE payment_id = $1`,
		lp.internalID, nullableInt(actorEmployeeID)); err != nil {
		return nil, fmt.Errorf("insert payment unapply history: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit unapply: %w", err)
	}
	return Get(ctx, pool, paymentUUID)
}
