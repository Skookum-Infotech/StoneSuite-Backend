package vendorcredit

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/vendorbill"
)

// appliableStatuses are the statuses from which a vendor credit may move
// credit.
//
// This is deliberately stricter than payment's equivalent gate, which allows
// applying a PEND payment: there, the money physically arrived and approval is
// bookkeeping. Here nothing has arrived — the credit IS the authorization — so
// unapproved credit must never offset AP (spec AD-7).
var appliableStatuses = map[string]bool{"APPV": true, "APPL": true}

type lockedCredit struct {
	internalID int
	vendorID   int
	statusCode string
	grandTotal float64
}

// lockVendorCreditForUpdate loads + row-locks a live vendor credit by uuid
// inside tx. It is the FIRST lock taken on any apply path: the global lock
// order is vendor_credit < vendor_bill, which keeps vendor_bill always last
// and so makes a deadlock cycle impossible (spec AD-6).
//
// Deliberately two queries, not one JOIN: Postgres's EvalPlanQual row-lock
// re-check does not re-run a JOIN when a blocked FOR UPDATE unblocks after a
// concurrent transaction changes the locked row's join key -- here that's
// vendor_credit_status, which recomputeCredit updates on every apply. A
// single locked JOIN query would then spuriously report zero rows for the
// second of two concurrent Apply calls instead of returning the row with its
// now-current status. Locking status_id bare, then resolving its code as a
// second, unlocked query, sidesteps the re-check entirely.
func lockVendorCreditForUpdate(ctx context.Context, tx pgx.Tx, creditUUID string) (lockedCredit, error) {
	var lc lockedCredit
	var statusID int
	err := tx.QueryRow(ctx, `
		SELECT vc.vendor_credit_id, vc.vendor_credit_vendor_id, vc.vendor_credit_status, vc.vendor_credit_grand_total
		FROM vendor_credit vc
		WHERE vc.vendor_credit_uuid = $1 AND vc.vendor_credit_deleted_at IS NULL
		FOR UPDATE OF vc`, creditUUID,
	).Scan(&lc.internalID, &lc.vendorID, &statusID, &lc.grandTotal)
	if errors.Is(err, pgx.ErrNoRows) {
		return lockedCredit{}, ErrNotFound
	}
	if err != nil {
		return lockedCredit{}, fmt.Errorf("lock vendor credit: %w", err)
	}
	if err := tx.QueryRow(ctx,
		`SELECT record_status_code FROM lkp_record_status WHERE record_status_id = $1`, statusID,
	).Scan(&lc.statusCode); err != nil {
		return lockedCredit{}, fmt.Errorf("resolve vendor credit status: %w", err)
	}
	return lc, nil
}

// recomputeCredit recomputes applied_total/unapplied_amount from the live
// vendor_credit_application rows and re-derives the credit's own status
// between APPV and APPL.
//
// APPL is derived, never user-directed: a vendor credit is Applied exactly
// when its credit is fully consumed, and drops back to APPV the moment any of
// it is returned by a reversal. VOID is left alone — the void cascade owns it.
func recomputeCredit(ctx context.Context, tx pgx.Tx, lc lockedCredit, action string, actorEmployeeID int) error {
	var applied float64
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(application_amount), 0) FROM vendor_credit_application
		WHERE vendor_credit_id = $1 AND application_deleted_at IS NULL`, lc.internalID).Scan(&applied); err != nil {
		return fmt.Errorf("sum vendor credit applications: %w", err)
	}
	applied = round2(applied)
	unapplied := round2(lc.grandTotal - applied)
	if unapplied < 0 {
		unapplied = 0
	}

	toCode := lc.statusCode
	if lc.statusCode == "APPV" || lc.statusCode == "APPL" {
		if unapplied <= 0.005 {
			toCode = "APPL"
		} else {
			toCode = "APPV"
		}
	}

	var typeID int
	if err := tx.QueryRow(ctx, `SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'VCRD'`).Scan(&typeID); err != nil {
		return fmt.Errorf("resolve VCRD type: %w", err)
	}
	var fromStatusID, toStatusID int
	if err := tx.QueryRow(ctx, `SELECT record_status_id FROM lkp_record_status WHERE record_status_record_type = $1 AND record_status_code = $2`,
		typeID, lc.statusCode).Scan(&fromStatusID); err != nil {
		return fmt.Errorf("resolve vendor credit from-status: %w", err)
	}
	if err := tx.QueryRow(ctx, `SELECT record_status_id FROM lkp_record_status WHERE record_status_record_type = $1 AND record_status_code = $2`,
		typeID, toCode).Scan(&toStatusID); err != nil {
		return fmt.Errorf("resolve vendor credit to-status: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE vendor_credit SET vendor_credit_applied_total = $1, vendor_credit_unapplied_amount = $2,
			vendor_credit_status = $3, vendor_credit_updated_at = NOW(), vendor_credit_updated_by = $4,
			vendor_credit_record_version = vendor_credit_record_version + 1
		WHERE vendor_credit_id = $5`,
		applied, unapplied, toStatusID, nullableInt(actorEmployeeID), lc.internalID); err != nil {
		return fmt.Errorf("update vendor credit rollup: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO vendor_credit_history (vendor_credit_id, from_status_id, to_status_id, action, actor_employee_id)
		VALUES ($1, $2, $3, $4, $5)`,
		lc.internalID, fromStatusID, toStatusID, action, nullableInt(actorEmployeeID)); err != nil {
		return fmt.Errorf("insert vendor credit %s history: %w", action, err)
	}
	return nil
}

// Apply allocates amount of creditUUID's unapplied credit to billUUID,
// reducing that vendor bill's balance_due via its vendor_bill_credit_total
// rollup.
//
// Caps at min(credit.unapplied_amount, bill.balance_due) and rejects (400)
// rather than clamping if amount exceeds that cap (spec AD-9). Rejects (409) if
// the credit is not approved or the bill isn't in a payable status, and (400)
// on a vendor mismatch.
func Apply(ctx context.Context, pool *pgxpool.Pool, creditUUID, billUUID string, amount float64, actorEmployeeID int) (*VendorCredit, error) {
	if amount <= 0 {
		return nil, ClientError{Msg: "amount must be positive."}
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin apply: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	lc, err := lockVendorCreditForUpdate(ctx, tx, creditUUID) // lock order: vendor_credit first
	if err != nil {
		return nil, err
	}
	if !appliableStatuses[lc.statusCode] {
		return nil, ClientError{Msg: "Cannot apply a " + lc.statusCode + " vendor credit; it must be approved first."}
	}
	li, err := vendorbill.LockForUpdate(ctx, tx, billUUID) // then vendor bill, always last
	if err != nil {
		return nil, err
	}
	if li.VendorID != lc.vendorID {
		return nil, ClientError{Msg: "Vendor bill belongs to a different vendor than the vendor credit."}
	}
	if !vendorbill.PayableStatuses[li.StatusCode] {
		return nil, ClientError{Msg: "Cannot apply credit to a " + li.StatusCode + " vendor bill; it must be approved first."}
	}

	var applied float64
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(application_amount),0) FROM vendor_credit_application
		WHERE vendor_credit_id = $1 AND application_deleted_at IS NULL`, lc.internalID).Scan(&applied); err != nil {
		return nil, fmt.Errorf("sum vendor credit applications: %w", err)
	}
	unapplied := round2(lc.grandTotal - applied)
	capAmt := unapplied
	if b := li.BalanceDue(); b < capAmt {
		capAmt = b
	}
	if amount > capAmt+0.001 {
		return nil, ClientError{Msg: "Amount exceeds available credit or vendor bill balance."}
	}

	// uq_vcrd_app_live_pair permits one live row per (credit, bill), so a
	// re-apply increments the existing row rather than inserting a second.
	var existingID int
	err = tx.QueryRow(ctx, `
		SELECT application_id FROM vendor_credit_application
		WHERE vendor_credit_id = $1 AND vendor_bill_id = $2 AND application_deleted_at IS NULL`,
		lc.internalID, li.InternalID).Scan(&existingID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		if _, err := tx.Exec(ctx, `
			INSERT INTO vendor_credit_application (vendor_credit_id, vendor_bill_id, application_amount, application_created_by)
			VALUES ($1,$2,$3,$4)`, lc.internalID, li.InternalID, round2(amount), nullableInt(actorEmployeeID)); err != nil {
			return nil, fmt.Errorf("insert vendor credit application: %w", err)
		}
	case err != nil:
		return nil, fmt.Errorf("check existing application: %w", err)
	default:
		if _, err := tx.Exec(ctx, `
			UPDATE vendor_credit_application
			SET application_amount = application_amount + $1, application_record_version = application_record_version + 1
			WHERE application_id = $2`, round2(amount), existingID); err != nil {
			return nil, fmt.Errorf("increase vendor credit application: %w", err)
		}
	}

	if err := recomputeCredit(ctx, tx, lc, "apply", actorEmployeeID); err != nil {
		return nil, err
	}
	if err := vendorbill.RecomputeBalance(ctx, tx, li, "credit", actorEmployeeID); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit apply: %w", err)
	}
	return Get(ctx, pool, creditUUID)
}

// Reverse reverses the live application between creditUUID and billUUID
// (soft-deletes it), recomputing both rollups. No bill-status gate: a
// reversal must be possible regardless of the bill's current status.
func Reverse(ctx context.Context, pool *pgxpool.Pool, creditUUID, billUUID string, actorEmployeeID int) (*VendorCredit, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin reverse: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	lc, err := lockVendorCreditForUpdate(ctx, tx, creditUUID)
	if err != nil {
		return nil, err
	}
	li, err := vendorbill.LockForUpdate(ctx, tx, billUUID)
	if err != nil {
		return nil, err
	}

	tag, err := tx.Exec(ctx, `
		UPDATE vendor_credit_application SET application_deleted_at = NOW(), application_deleted_by = $1
		WHERE vendor_credit_id = $2 AND vendor_bill_id = $3 AND application_deleted_at IS NULL`,
		actorOrSystem(actorEmployeeID), lc.internalID, li.InternalID)
	if err != nil {
		return nil, fmt.Errorf("reverse: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ClientError{Msg: "No live application between this vendor credit and vendor bill."}
	}

	if err := recomputeCredit(ctx, tx, lc, "reverse", actorEmployeeID); err != nil {
		return nil, err
	}
	if err := vendorbill.RecomputeBalance(ctx, tx, li, "uncredit", actorEmployeeID); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit reverse: %w", err)
	}
	return Get(ctx, pool, creditUUID)
}
