package payment

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// invoicePayableStatuses are the only invoice statuses Apply accepts new
// money against — identical to the gate the retired invoice.RecordPayment
// enforced (spec §8, §11).
var invoicePayableStatuses = map[string]bool{"SENT": true, "PART": true, "ODUE": true}

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

type lockedInvoice struct {
	internalID int
	customerID int
	statusCode string
	grandTotal float64
	amountPaid float64
}

// lockInvoiceForUpdate loads + row-locks a live invoice by uuid inside tx.
// Callers must already hold the payment row's lock first (fixed lock order:
// payment before invoice) to keep Apply/Unapply/void-cascade deadlock-free.
func lockInvoiceForUpdate(ctx context.Context, tx pgx.Tx, invoiceUUID string) (lockedInvoice, error) {
	var li lockedInvoice
	err := tx.QueryRow(ctx, `
		SELECT i.invoice_id, i.invoice_customer_id, rs.record_status_code, i.invoice_grand_total, i.invoice_amount_paid
		FROM invoice i
		JOIN lkp_record_status rs ON rs.record_status_id = i.invoice_status
		WHERE i.invoice_uuid = $1 AND i.invoice_deleted_at IS NULL
		FOR UPDATE OF i`, invoiceUUID,
	).Scan(&li.internalID, &li.customerID, &li.statusCode, &li.grandTotal, &li.amountPaid)
	if errors.Is(err, pgx.ErrNoRows) {
		return lockedInvoice{}, ClientError{Msg: "Unknown or deleted invoice."}
	}
	if err != nil {
		return lockedInvoice{}, fmt.Errorf("lock invoice: %w", err)
	}
	return li, nil
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

// deriveInvoiceStatus re-derives an invoice's status purely from its
// recomputed balance (spec §8 Apply/Unapply steps). This intentionally does
// NOT go through invoice.CanTransition: that map is for user-directed
// transitions and has no path back out of PAID, or from PART to SENT — moves
// an Unapply legitimately needs. See Task 3.3 step 1 for the full rationale.
func deriveInvoiceStatus(currentCode string, amountPaid, grandTotal float64) string {
	balanceDue := grandTotal - amountPaid
	switch {
	case balanceDue <= 0.005:
		return "PAID"
	case amountPaid > 0.005:
		return "PART"
	case currentCode == "PART" || currentCode == "PAID":
		return "SENT" // fully unapplied back to zero; ODUE re-flagging is a separate concern
	default:
		return currentCode
	}
}

// recomputeInvoice recomputes invoice_amount_paid/balance_due from live
// payment_application rows (across all payments), re-derives status, and
// writes both plus an invoice_history row, inside tx.
func recomputeInvoice(ctx context.Context, tx pgx.Tx, li lockedInvoice, action string, actorEmployeeID int) error {
	var amountPaid float64
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(pa.application_amount), 0)
		FROM payment_application pa
		JOIN payment p ON p.payment_id = pa.payment_id
		WHERE pa.invoice_id = $1 AND pa.application_deleted_at IS NULL AND p.payment_deleted_at IS NULL`,
		li.internalID).Scan(&amountPaid); err != nil {
		return fmt.Errorf("sum invoice applications: %w", err)
	}
	amountPaid = round2(amountPaid)
	balanceDue := round2(li.grandTotal - amountPaid)
	if balanceDue < 0 {
		balanceDue = 0
	}

	toCode := deriveInvoiceStatus(li.statusCode, amountPaid, li.grandTotal)
	var invTypeID int
	if err := tx.QueryRow(ctx, `SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'INVC'`).Scan(&invTypeID); err != nil {
		return fmt.Errorf("resolve INVC type: %w", err)
	}
	var fromStatusID, toStatusID int
	if err := tx.QueryRow(ctx, `SELECT record_status_id FROM lkp_record_status WHERE record_status_record_type = $1 AND record_status_code = $2`,
		invTypeID, li.statusCode).Scan(&fromStatusID); err != nil {
		return fmt.Errorf("resolve invoice from-status: %w", err)
	}
	if err := tx.QueryRow(ctx, `SELECT record_status_id FROM lkp_record_status WHERE record_status_record_type = $1 AND record_status_code = $2`,
		invTypeID, toCode).Scan(&toStatusID); err != nil {
		return fmt.Errorf("resolve invoice to-status: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE invoice SET invoice_amount_paid = $1, invoice_balance_due = $2, invoice_status = $3,
			invoice_updated_at = NOW(), invoice_updated_by = $4, invoice_record_version = invoice_record_version + 1
		WHERE invoice_id = $5`, amountPaid, balanceDue, toStatusID, nullableInt(actorEmployeeID), li.internalID); err != nil {
		return fmt.Errorf("update invoice rollup: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO invoice_history (invoice_id, from_status_id, to_status_id, action, actor_employee_id)
		VALUES ($1, $2, $3, $4, $5)`, li.internalID, fromStatusID, toStatusID, action, nullableInt(actorEmployeeID)); err != nil {
		return fmt.Errorf("insert invoice %s history: %w", action, err)
	}
	return nil
}

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
	li, err := lockInvoiceForUpdate(ctx, tx, invoiceUUID) // then invoice
	if err != nil {
		return nil, err
	}
	if li.customerID != lp.customerID {
		return nil, ClientError{Msg: "Invoice belongs to a different customer than the payment."}
	}
	if !invoicePayableStatuses[li.statusCode] {
		return nil, ClientError{Msg: "Cannot apply payment to a " + li.statusCode + " invoice; it must be sent first."}
	}

	var applied float64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(SUM(application_amount),0) FROM payment_application WHERE payment_id = $1 AND application_deleted_at IS NULL`, lp.internalID).Scan(&applied); err != nil {
		return nil, fmt.Errorf("sum payment applications: %w", err)
	}
	unapplied := round2(lp.amount - applied)
	invoiceBalance := round2(li.grandTotal - li.amountPaid)
	capAmt := unapplied
	if invoiceBalance < capAmt {
		capAmt = invoiceBalance
	}
	if amount > capAmt+0.001 {
		return nil, ClientError{Msg: "Amount exceeds available balance."}
	}

	var existingID int
	err = tx.QueryRow(ctx, `SELECT application_id FROM payment_application WHERE payment_id = $1 AND invoice_id = $2 AND application_deleted_at IS NULL`,
		lp.internalID, li.internalID).Scan(&existingID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		if _, err := tx.Exec(ctx, `
			INSERT INTO payment_application (payment_id, invoice_id, application_amount, application_created_by)
			VALUES ($1,$2,$3,$4)`, lp.internalID, li.internalID, round2(amount), nullableInt(actorEmployeeID)); err != nil {
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
	if err := recomputeInvoice(ctx, tx, li, "payment", actorEmployeeID); err != nil {
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
	li, err := lockInvoiceForUpdate(ctx, tx, invoiceUUID)
	if err != nil {
		return nil, err
	}

	tag, err := tx.Exec(ctx, `
		UPDATE payment_application SET application_deleted_at = NOW(), application_deleted_by = $1
		WHERE payment_id = $2 AND invoice_id = $3 AND application_deleted_at IS NULL`,
		actorOrSystem(actorEmployeeID), lp.internalID, li.internalID)
	if err != nil {
		return nil, fmt.Errorf("unapply: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ClientError{Msg: "No live application between this payment and invoice."}
	}

	if err := recomputePayment(ctx, tx, lp.internalID, lp.amount, actorEmployeeID); err != nil {
		return nil, err
	}
	if err := recomputeInvoice(ctx, tx, li, "unapply", actorEmployeeID); err != nil {
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
