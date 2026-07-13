package invoice

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Transition moves an invoice to toStatusCode after validating the move against
// the static transition map. The invoice row is locked for the rest of the
// transaction so concurrent transitions serialize.
func Transition(ctx context.Context, pool *pgxpool.Pool, id, toStatusCode string, actorEmployeeID int) (*Invoice, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transition: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var internalID, curStatusID, typeID int
	var curStatusCode string
	err = tx.QueryRow(ctx, `
		SELECT i.invoice_id, i.invoice_status, i.record_type, rs.record_status_code
		FROM invoice i
		JOIN lkp_record_status rs ON rs.record_status_id = i.invoice_status
		WHERE i.invoice_uuid = $1 AND i.invoice_deleted_at IS NULL
		FOR UPDATE OF i`, id,
	).Scan(&internalID, &curStatusID, &typeID, &curStatusCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("resolve invoice for transition: %w", err)
	}

	if err := ValidateTransition(curStatusCode, toStatusCode); err != nil {
		return nil, err
	}

	toStatusID, err := statusIDByCode(ctx, pool, typeID, toStatusCode)
	if err != nil {
		return nil, ClientError{Msg: "Unknown target status: " + toStatusCode}
	}

	if _, err := tx.Exec(ctx, `
		UPDATE invoice SET invoice_status = $1, invoice_updated_at = NOW(),
			invoice_updated_by = $2, invoice_record_version = invoice_record_version + 1
		WHERE invoice_id = $3`, toStatusID, nullableInt(actorEmployeeID), internalID); err != nil {
		return nil, fmt.Errorf("update invoice status: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO invoice_history (invoice_id, from_status_id, to_status_id, action, actor_employee_id)
		VALUES ($1, $2, $3, 'transition', $4)`, internalID, curStatusID, toStatusID, nullableInt(actorEmployeeID)); err != nil {
		return nil, fmt.Errorf("insert invoice transition history: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit transition: %w", err)
	}
	return Get(ctx, pool, id)
}

// RecordPayment adds to amount_paid, recomputes balance, and auto-transitions
// to PART or PAID if applicable.
func RecordPayment(ctx context.Context, pool *pgxpool.Pool, id string, amount float64, actorEmployeeID int) (*Invoice, error) {
	if amount <= 0 {
		return nil, ClientError{Msg: "Payment amount must be positive."}
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin record payment: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var internalID, curStatusID, typeID int
	var curStatusCode string
	var amountPaid, grandTotal float64

	err = tx.QueryRow(ctx, `
		SELECT i.invoice_id, i.invoice_status, i.record_type, rs.record_status_code, i.invoice_amount_paid, i.invoice_grand_total
		FROM invoice i
		JOIN lkp_record_status rs ON rs.record_status_id = i.invoice_status
		WHERE i.invoice_uuid = $1 AND i.invoice_deleted_at IS NULL
		FOR UPDATE OF i`, id,
	).Scan(&internalID, &curStatusID, &typeID, &curStatusCode, &amountPaid, &grandTotal)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("resolve invoice for payment: %w", err)
	}

	if terminalStatuses[curStatusCode] {
		return nil, ClientError{Msg: "Cannot record payment on a " + curStatusCode + " invoice."}
	}

	newAmountPaid := amountPaid + amount
	// Use decimal math rounding conceptually, but float64 is fine for simple check
	// We'll trust the DB constraints (which are DECIMAL) to catch tiny overflow,
	// but let's check here to return a nice message.
	if newAmountPaid > grandTotal+0.001 { // +0.001 to handle float fuzz
		return nil, ClientError{Msg: "Payment exceeds balance due."}
	}

	newBalanceDue := grandTotal - newAmountPaid
	if newBalanceDue < 0 {
		newBalanceDue = 0
	}

	// Auto-transition logic
	var toStatusCode string
	if newBalanceDue < 0.005 { // basically 0
		toStatusCode = "PAID"
	} else if curStatusCode != "PART" && curStatusCode != "DRFT" && curStatusCode != "PAPV" && curStatusCode != "APPV" {
		// If they pay a bit but they're in SENT/ODUE, move to PART.
		// (Assuming they can't be in DRFT/PAPV/APPV and receive a payment, but if they are, we don't auto-transition to PART yet? 
		// Actually, let's just use CanTransition to safely try PART).
		if CanTransition(curStatusCode, "PART") {
			toStatusCode = "PART"
		}
	}

	var toStatusID int = curStatusID
	if toStatusCode != "" && CanTransition(curStatusCode, toStatusCode) {
		statusID, err := statusIDByCode(ctx, pool, typeID, toStatusCode)
		if err != nil {
			return nil, err
		}
		toStatusID = statusID
	}

	if _, err := tx.Exec(ctx, `
		UPDATE invoice SET invoice_amount_paid = $1, invoice_balance_due = $2,
			invoice_status = $3, invoice_updated_at = NOW(),
			invoice_updated_by = $4, invoice_record_version = invoice_record_version + 1
		WHERE invoice_id = $5`, newAmountPaid, newBalanceDue, toStatusID, nullableInt(actorEmployeeID), internalID); err != nil {
		return nil, fmt.Errorf("update invoice payment: %w", err)
	}

	// Record history for payment
	if _, err := tx.Exec(ctx, `
		INSERT INTO invoice_history (invoice_id, from_status_id, to_status_id, action, actor_employee_id)
		VALUES ($1, $2, $3, 'payment', $4)`, internalID, curStatusID, toStatusID, nullableInt(actorEmployeeID)); err != nil {
		return nil, fmt.Errorf("insert invoice payment history: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit payment: %w", err)
	}

	return Get(ctx, pool, id)
}
