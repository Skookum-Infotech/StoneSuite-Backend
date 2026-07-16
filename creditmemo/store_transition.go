package creditmemo

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/invoice"
)

// Transition moves a credit memo to toStatusCode after validating the move
// against the static transition map. Moving to VOID first reverses every live
// application on this memo, restoring each affected invoice's balance.
//
// VOID is only reachable from DRFT/APPV (spec AD-14) — an APPL memo must be
// unapplied first — so the cascade below only ever runs on a partially applied
// APPV memo.
func Transition(ctx context.Context, pool *pgxpool.Pool, id, toStatusCode string, actorEmployeeID int) (*CreditMemo, error) {
	internalID, curStatusCode, err := internalIDByUUID(ctx, pool, id)
	if err != nil {
		return nil, err
	}
	if err := ValidateTransition(curStatusCode, toStatusCode); err != nil {
		return nil, err
	}
	typeID, err := typeIDByCode(ctx, pool, "CRDT")
	if err != nil {
		return nil, err
	}
	fromStatusID, err := statusIDByCode(ctx, pool, typeID, curStatusCode)
	if err != nil {
		return nil, err
	}
	toStatusID, err := statusIDByCode(ctx, pool, typeID, toStatusCode)
	if err != nil {
		return nil, ClientError{Msg: "Unknown target status: " + toStatusCode}
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transition: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Lock the memo first — the global order is credit_memo < payment < invoice.
	if _, err := tx.Exec(ctx,
		`SELECT credit_memo_id FROM credit_memo WHERE credit_memo_id = $1 FOR UPDATE`, internalID); err != nil {
		return nil, fmt.Errorf("lock credit memo for transition: %w", err)
	}

	if toStatusCode == "VOID" {
		// ORDER BY invoice_id fixes a global lock order across invoices so two
		// concurrent VOID cascades touching the same two invoices can't lock
		// them in opposite orders and deadlock.
		rows, err := tx.Query(ctx, `
			SELECT invoice_id FROM credit_memo_application
			WHERE credit_memo_id = $1 AND application_deleted_at IS NULL ORDER BY invoice_id`, internalID)
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
			li, err := invoice.LockForUpdateByID(ctx, tx, invInternalID)
			if err != nil {
				return nil, err
			}
			if _, err := tx.Exec(ctx, `
				UPDATE credit_memo_application SET application_deleted_at = NOW(), application_deleted_by = $1
				WHERE credit_memo_id = $2 AND invoice_id = $3 AND application_deleted_at IS NULL`,
				actorOrSystem(actorEmployeeID), internalID, invInternalID); err != nil {
				return nil, fmt.Errorf("cascade-unapply: %w", err)
			}
			if err := invoice.RecomputeBalance(ctx, tx, li, "uncredit", actorEmployeeID); err != nil {
				return nil, err
			}
		}
		if len(invoiceInternalIDs) > 0 {
			// Every live application on this memo was just reversed, so its own
			// rollup needs recomputing. Do it directly rather than via
			// recomputeMemo: that would re-derive the status back to APPV, and
			// this transition is on its way to VOID.
			if _, err := tx.Exec(ctx, `
				UPDATE credit_memo SET credit_memo_applied_total = 0,
					credit_memo_unapplied_amount = credit_memo_grand_total
				WHERE credit_memo_id = $1`, internalID); err != nil {
				return nil, fmt.Errorf("reset credit memo rollup on void: %w", err)
			}
		}
	}

	if _, err := tx.Exec(ctx, `
		UPDATE credit_memo SET credit_memo_status = $1, credit_memo_updated_at = NOW(),
			credit_memo_updated_by = $2, credit_memo_record_version = credit_memo_record_version + 1
		WHERE credit_memo_id = $3`,
		toStatusID, nullableInt(actorEmployeeID), internalID); err != nil {
		return nil, fmt.Errorf("update credit memo status: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO credit_memo_history (credit_memo_id, from_status_id, to_status_id, action, actor_employee_id)
		VALUES ($1, $2, $3, 'transition', $4)`,
		internalID, fromStatusID, toStatusID, nullableInt(actorEmployeeID)); err != nil {
		return nil, fmt.Errorf("insert credit memo transition history: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit transition: %w", err)
	}
	return Get(ctx, pool, id)
}
