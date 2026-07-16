package creditmemo

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/invoice"
)

// appliableStatuses are the statuses from which a memo may move credit.
//
// This is deliberately stricter than payment's equivalent gate, which allows
// applying a PEND payment: there, the money physically arrived and approval is
// bookkeeping. Here nothing has arrived — the memo IS the authorization — so
// unapproved credit must never offset AR (spec AD-7).
var appliableStatuses = map[string]bool{"APPV": true, "APPL": true}

type lockedMemo struct {
	internalID int
	customerID int
	statusCode string
	grandTotal float64
}

// lockCreditMemoForUpdate loads + row-locks a live credit memo by uuid inside
// tx. It is the FIRST lock taken on any apply path: the global lock order is
// credit_memo < payment < invoice, which keeps invoice always last and so makes
// a deadlock cycle impossible (spec AD-12).
func lockCreditMemoForUpdate(ctx context.Context, tx pgx.Tx, memoUUID string) (lockedMemo, error) {
	var lm lockedMemo
	err := tx.QueryRow(ctx, `
		SELECT cm.credit_memo_id, cm.credit_memo_customer_id, rs.record_status_code, cm.credit_memo_grand_total
		FROM credit_memo cm
		JOIN lkp_record_status rs ON rs.record_status_id = cm.credit_memo_status
		WHERE cm.credit_memo_uuid = $1 AND cm.credit_memo_deleted_at IS NULL
		FOR UPDATE OF cm`, memoUUID,
	).Scan(&lm.internalID, &lm.customerID, &lm.statusCode, &lm.grandTotal)
	if errors.Is(err, pgx.ErrNoRows) {
		return lockedMemo{}, ErrNotFound
	}
	if err != nil {
		return lockedMemo{}, fmt.Errorf("lock credit memo: %w", err)
	}
	return lm, nil
}

// recomputeMemo recomputes applied_total/unapplied_amount from the live
// credit_memo_application rows and re-derives the memo's own status between
// APPV and APPL.
//
// APPL is derived, never user-directed (spec AD-13): a memo is Applied exactly
// when its credit is fully consumed, and drops back to APPV the moment any of
// it is returned by an unapply. VOID is left alone — the void cascade owns it.
func recomputeMemo(ctx context.Context, tx pgx.Tx, lm lockedMemo, action string, actorEmployeeID int) error {
	var applied float64
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(application_amount), 0) FROM credit_memo_application
		WHERE credit_memo_id = $1 AND application_deleted_at IS NULL`, lm.internalID).Scan(&applied); err != nil {
		return fmt.Errorf("sum credit memo applications: %w", err)
	}
	applied = round2(applied)
	unapplied := round2(lm.grandTotal - applied)
	if unapplied < 0 {
		unapplied = 0
	}

	toCode := lm.statusCode
	if lm.statusCode == "APPV" || lm.statusCode == "APPL" {
		if unapplied <= 0.005 {
			toCode = "APPL"
		} else {
			toCode = "APPV"
		}
	}

	var typeID int
	if err := tx.QueryRow(ctx, `SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'CRDT'`).Scan(&typeID); err != nil {
		return fmt.Errorf("resolve CRDT type: %w", err)
	}
	var fromStatusID, toStatusID int
	if err := tx.QueryRow(ctx, `SELECT record_status_id FROM lkp_record_status WHERE record_status_record_type = $1 AND record_status_code = $2`,
		typeID, lm.statusCode).Scan(&fromStatusID); err != nil {
		return fmt.Errorf("resolve credit memo from-status: %w", err)
	}
	if err := tx.QueryRow(ctx, `SELECT record_status_id FROM lkp_record_status WHERE record_status_record_type = $1 AND record_status_code = $2`,
		typeID, toCode).Scan(&toStatusID); err != nil {
		return fmt.Errorf("resolve credit memo to-status: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE credit_memo SET credit_memo_applied_total = $1, credit_memo_unapplied_amount = $2,
			credit_memo_status = $3, credit_memo_updated_at = NOW(), credit_memo_updated_by = $4,
			credit_memo_record_version = credit_memo_record_version + 1
		WHERE credit_memo_id = $5`,
		applied, unapplied, toStatusID, nullableInt(actorEmployeeID), lm.internalID); err != nil {
		return fmt.Errorf("update credit memo rollup: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO credit_memo_history (credit_memo_id, from_status_id, to_status_id, action, actor_employee_id)
		VALUES ($1, $2, $3, $4, $5)`,
		lm.internalID, fromStatusID, toStatusID, action, nullableInt(actorEmployeeID)); err != nil {
		return fmt.Errorf("insert credit memo %s history: %w", action, err)
	}
	return nil
}

// Apply allocates amount of memoUUID's unapplied credit to invoiceUUID,
// reducing that invoice's balance_due via its invoice_credit_total rollup.
//
// Caps at min(memo.unapplied_amount, invoice.balance_due) and rejects (400)
// rather than clamping if amount exceeds that cap (spec AD-9). Rejects (409) if
// the memo is not approved or the invoice isn't in a payable status, and (400)
// on a customer mismatch.
func Apply(ctx context.Context, pool *pgxpool.Pool, memoUUID, invoiceUUID string, amount float64, actorEmployeeID int) (*CreditMemo, error) {
	if amount <= 0 {
		return nil, ClientError{Msg: "amount must be positive."}
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin apply: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	lm, err := lockCreditMemoForUpdate(ctx, tx, memoUUID) // lock order: credit_memo first
	if err != nil {
		return nil, err
	}
	if !appliableStatuses[lm.statusCode] {
		return nil, ClientError{Msg: "Cannot apply a " + lm.statusCode + " credit memo; it must be approved first."}
	}
	li, err := invoice.LockForUpdate(ctx, tx, invoiceUUID) // then invoice, always last
	if err != nil {
		return nil, err
	}
	if li.CustomerID != lm.customerID {
		return nil, ClientError{Msg: "Invoice belongs to a different customer than the credit memo."}
	}
	if !invoice.PayableStatuses[li.StatusCode] {
		return nil, ClientError{Msg: "Cannot apply credit to a " + li.StatusCode + " invoice; it must be sent first."}
	}

	var applied float64
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(application_amount),0) FROM credit_memo_application
		WHERE credit_memo_id = $1 AND application_deleted_at IS NULL`, lm.internalID).Scan(&applied); err != nil {
		return nil, fmt.Errorf("sum credit memo applications: %w", err)
	}
	unapplied := round2(lm.grandTotal - applied)
	capAmt := unapplied
	if b := li.BalanceDue(); b < capAmt {
		capAmt = b
	}
	if amount > capAmt+0.001 {
		return nil, ClientError{Msg: "Amount exceeds available credit or invoice balance."}
	}

	// uq_cm_app_live_pair permits one live row per (memo, invoice), so a
	// re-apply increments the existing row rather than inserting a second.
	var existingID int
	err = tx.QueryRow(ctx, `
		SELECT application_id FROM credit_memo_application
		WHERE credit_memo_id = $1 AND invoice_id = $2 AND application_deleted_at IS NULL`,
		lm.internalID, li.InternalID).Scan(&existingID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		if _, err := tx.Exec(ctx, `
			INSERT INTO credit_memo_application (credit_memo_id, invoice_id, application_amount, application_created_by)
			VALUES ($1,$2,$3,$4)`, lm.internalID, li.InternalID, round2(amount), nullableInt(actorEmployeeID)); err != nil {
			return nil, fmt.Errorf("insert credit memo application: %w", err)
		}
	case err != nil:
		return nil, fmt.Errorf("check existing application: %w", err)
	default:
		if _, err := tx.Exec(ctx, `
			UPDATE credit_memo_application
			SET application_amount = application_amount + $1, application_record_version = application_record_version + 1
			WHERE application_id = $2`, round2(amount), existingID); err != nil {
			return nil, fmt.Errorf("increase credit memo application: %w", err)
		}
	}

	if err := recomputeMemo(ctx, tx, lm, "apply", actorEmployeeID); err != nil {
		return nil, err
	}
	if err := invoice.RecomputeBalance(ctx, tx, li, "credit", actorEmployeeID); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit apply: %w", err)
	}
	return Get(ctx, pool, memoUUID)
}

// Unapply reverses the live application between memoUUID and invoiceUUID
// (soft-deletes it), recomputing both rollups. No invoice-status gate: a
// reversal must be possible regardless of the invoice's current status.
func Unapply(ctx context.Context, pool *pgxpool.Pool, memoUUID, invoiceUUID string, actorEmployeeID int) (*CreditMemo, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin unapply: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	lm, err := lockCreditMemoForUpdate(ctx, tx, memoUUID)
	if err != nil {
		return nil, err
	}
	li, err := invoice.LockForUpdate(ctx, tx, invoiceUUID)
	if err != nil {
		return nil, err
	}

	tag, err := tx.Exec(ctx, `
		UPDATE credit_memo_application SET application_deleted_at = NOW(), application_deleted_by = $1
		WHERE credit_memo_id = $2 AND invoice_id = $3 AND application_deleted_at IS NULL`,
		actorOrSystem(actorEmployeeID), lm.internalID, li.InternalID)
	if err != nil {
		return nil, fmt.Errorf("unapply: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ClientError{Msg: "No live application between this credit memo and invoice."}
	}

	if err := recomputeMemo(ctx, tx, lm, "unapply", actorEmployeeID); err != nil {
		return nil, err
	}
	if err := invoice.RecomputeBalance(ctx, tx, li, "uncredit", actorEmployeeID); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit unapply: %w", err)
	}
	return Get(ctx, pool, memoUUID)
}
