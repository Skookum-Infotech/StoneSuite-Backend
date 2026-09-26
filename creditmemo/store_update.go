package creditmemo

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/approvalchain"
)

// editableMoneyStatuses are the statuses in which lines and money may still be
// changed. Once approved a memo is an authorized instrument and applications
// may exist against it, so correcting one means voiding and reissuing
// (spec AD-15).
var editableMoneyStatuses = map[string]bool{"DRFT": true}

// terminalStatuses can't be edited at all.
var terminalStatuses = map[string]bool{"APPL": true, "VOID": true}

func internalIDByUUID(ctx context.Context, pool *pgxpool.Pool, id string) (int, string, error) {
	var internalID int
	var statusCode string
	err := pool.QueryRow(ctx, `
		SELECT cm.credit_memo_id, rs.record_status_code
		FROM credit_memo cm
		JOIN lkp_record_status rs ON rs.record_status_id = cm.credit_memo_status
		WHERE cm.credit_memo_uuid = $1 AND cm.credit_memo_deleted_at IS NULL`, id,
	).Scan(&internalID, &statusCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", ErrNotFound
	}
	if err != nil {
		return 0, "", fmt.Errorf("resolve credit memo: %w", err)
	}
	return internalID, statusCode, nil
}

// Update edits a credit memo. Non-monetary fields are editable in any
// non-terminal status; lines, tax and adjustment only while DRFT (spec AD-15).
func Update(ctx context.Context, pool *pgxpool.Pool, id string, in UpdateCreditMemoInput, actorEmployeeID int) (*CreditMemo, error) {
	internalID, statusCode, err := internalIDByUUID(ctx, pool, id)
	if err != nil {
		return nil, err
	}
	if terminalStatuses[statusCode] {
		return nil, ClientError{Msg: "Cannot edit a " + statusCode + " credit memo."}
	}

	if err := validateUpdateMoney(in.Amount, in.Lines); err != nil {
		return nil, err
	}
	wantsMoneyChange := in.Lines != nil || in.Amount != nil || in.SalesTaxPercent != nil || in.Adjustment != nil
	if wantsMoneyChange && !editableMoneyStatuses[statusCode] {
		return nil, ClientError{Msg: "Lines and amounts can only be changed while a credit memo is Draft; void it and issue a new one."}
	}

	// The column is NOT NULL DEFAULT '{}'. A nil Go map encodes as SQL NULL, so
	// every PATCH that omits customFields would 500 without this guard.
	custom := in.CustomFields
	if custom == nil {
		custom = map[string]any{}
	}
	if err := validateCustom(ctx, pool, custom); err != nil {
		return nil, err
	}

	// Load current money inputs so a partial PATCH keeps the untouched ones.
	var curTaxPercent, curAdjustment, curApplied, curSubtotal float64
	if err := pool.QueryRow(ctx, `
		SELECT credit_memo_sales_tax_percent, credit_memo_adjustment, credit_memo_applied_total, credit_memo_subtotal
		FROM credit_memo WHERE credit_memo_id = $1`, internalID,
	).Scan(&curTaxPercent, &curAdjustment, &curApplied, &curSubtotal); err != nil {
		return nil, fmt.Errorf("load credit memo money: %w", err)
	}
	taxPercent := curTaxPercent
	if in.SalesTaxPercent != nil {
		taxPercent = *in.SalesTaxPercent
	}
	if taxPercent < 0 || taxPercent > 100 {
		return nil, ClientError{Msg: "salesTaxPercent must be between 0 and 100."}
	}
	adjustment := curAdjustment
	if in.Adjustment != nil {
		adjustment = *in.Adjustment
	}

	var resolved []resolvedLine
	var lineMoney []LineMoney
	if in.Lines != nil {
		if len(in.Lines) == 0 {
			return nil, ClientError{Msg: "a credit memo needs at least one line."}
		}
		for _, li := range in.Lines {
			rl, err := resolveLine(ctx, pool, li, taxPercent)
			if err != nil {
				return nil, err
			}
			resolved = append(resolved, rl)
			lineMoney = append(lineMoney, rl.money)
		}
	} else if in.Amount != nil {
		lineMoney = []LineMoney{ComputeAmountLine(*in.Amount, taxPercent)}
	} else {
		rows, err := pool.Query(ctx, `
			SELECT line_subtotal, line_discount, line_tax, line_total
			FROM credit_memo_item WHERE credit_memo_id = $1 AND item_deleted_at IS NULL`, internalID)
		if err != nil {
			return nil, fmt.Errorf("load existing line money: %w", err)
		}
		defer rows.Close()
		for rows.Next() {
			var m LineMoney
			if err := rows.Scan(&m.Subtotal, &m.Discount, &m.Tax, &m.Total); err != nil {
				return nil, fmt.Errorf("scan existing line money: %w", err)
			}
			lineMoney = append(lineMoney, m)
		}
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("load existing line money: %w", err)
		}
		if len(lineMoney) == 0 {
			// An amount-only memo has no lines to sum; its stored subtotal is its amount.
			lineMoney = []LineMoney{ComputeAmountLine(curSubtotal, taxPercent)}
		}
	}
	money := ComputeHeader(lineMoney, adjustment, curApplied)

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin update credit memo: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Lock order is credit_memo < payment: take the memo, then the payment it was
	// issued from, so a raise is checked against what is really left of it.
	var sourcePaymentID *int
	var oldGrandTotal float64
	if err := tx.QueryRow(ctx, `
		SELECT credit_memo_source_payment_id, credit_memo_grand_total
		FROM credit_memo WHERE credit_memo_id = $1 FOR UPDATE`, internalID,
	).Scan(&sourcePaymentID, &oldGrandTotal); err != nil {
		return nil, fmt.Errorf("lock credit memo for update: %w", err)
	}
	if sourcePaymentID != nil {
		room, err := lockLinkedPayment(ctx, tx, *sourcePaymentID)
		if err != nil {
			return nil, err
		}
		// Only a raise needs headroom; lowering or a notes-only edit never does.
		// The room already counts this memo's own current total as taken.
		if money.GrandTotal > oldGrandTotal+creditTolerance {
			if err := checkPaymentRoom(money.GrandTotal, room+oldGrandTotal); err != nil {
				return nil, err
			}
		}
	}

	if _, err := tx.Exec(ctx, `
		UPDATE credit_memo SET
			credit_memo_reference_number = $1,
			credit_memo_date = COALESCE(NULLIF($2,'')::date, credit_memo_date),
			credit_memo_reason = $3,
			credit_memo_sales_tax_percent = $4,
			credit_memo_adjustment = $5,
			credit_memo_price_level = COALESCE($6, credit_memo_price_level),
			credit_memo_currency = COALESCE($7, credit_memo_currency),
			credit_memo_owner_id = COALESCE($8, credit_memo_owner_id),
			credit_memo_sales_rep_id = COALESCE($9, credit_memo_sales_rep_id),
			credit_memo_memo = $10,
			credit_memo_notes = $11,
			credit_memo_internal_notes = $12,
			credit_memo_custom_fields = $13,
			credit_memo_subtotal = $14,
			credit_memo_discount_total = $15,
			credit_memo_tax_total = $16,
			credit_memo_grand_total = $17,
			credit_memo_unapplied_amount = $18,
			credit_memo_updated_at = NOW(),
			credit_memo_updated_by = $19,
			credit_memo_record_version = credit_memo_record_version + 1
		WHERE credit_memo_id = $20`,
		in.ReferenceNumber, in.CreditMemoDate, in.Reason,
		taxPercent, adjustment,
		in.PriceLevelID, in.CurrencyID, in.OwnerEmployeeID, in.SalesRepID,
		in.Memo, in.Notes, in.InternalNotes,
		custom,
		money.Subtotal, money.DiscountTotal, money.TaxTotal, money.GrandTotal, money.UnappliedAmount,
		nullableInt(actorEmployeeID), internalID); err != nil {
		return nil, fmt.Errorf("update credit memo: %w", err)
	}

	if in.Lines != nil || in.Amount != nil {
		// Replace lines by soft-delete + re-insert (an amount edit re-inserts none). uq_cmi_line_active is unique
		// among LIVE rows only, so a re-inserted line may reuse its line_number.
		if _, err := tx.Exec(ctx, `
			UPDATE credit_memo_item SET item_deleted_at = NOW()
			WHERE credit_memo_id = $1 AND item_deleted_at IS NULL`, internalID); err != nil {
			return nil, fmt.Errorf("soft-delete credit memo lines: %w", err)
		}
		for _, rl := range resolved {
			if _, err := tx.Exec(ctx, `
				INSERT INTO credit_memo_item (
					credit_memo_id, line_number, inventory_item_id,
					item_name, sku, description, unit_id, unit_code,
					quantity, unit_price, discount_percent, tax_rate_id, tax_percent,
					line_subtotal, line_discount, line_tax, line_total, item_created_by
				) VALUES ($1,$2,$3, $4,$5,$6,$7,$8, $9,$10,$11,$12,$13, $14,$15,$16,$17,$18)`,
				internalID, rl.in.LineNumber, rl.invItemID,
				rl.itemName, rl.sku, rl.in.Description, rl.unitID, rl.unitCode,
				rl.in.Quantity, rl.in.UnitPrice, rl.in.DiscountPercent, rl.in.TaxRateID, rl.taxPercent,
				rl.money.Subtotal, rl.money.Discount, rl.money.Tax, rl.money.Total,
				nullableInt(actorEmployeeID)); err != nil {
				return nil, fmt.Errorf("insert credit memo line: %w", err)
			}
		}
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO credit_memo_history (credit_memo_id, from_status_id, to_status_id, action, actor_employee_id)
		SELECT credit_memo_id, credit_memo_status, credit_memo_status, 'update', $2
		FROM credit_memo WHERE credit_memo_id = $1`,
		internalID, nullableInt(actorEmployeeID)); err != nil {
		return nil, fmt.Errorf("insert credit memo update history: %w", err)
	}
	if sourcePaymentID != nil {
		if err := recomputePaymentCredited(ctx, tx, *sourcePaymentID, actorEmployeeID); err != nil {
			return nil, err
		}
	}
	// Saving an edit is how a rejected credit memo goes back to its approvers.
	resubmitted, err := approvalchain.ResubmitAfterEdit(ctx, tx, moduleConfig(), internalID)
	if err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit update credit memo: %w", err)
	}
	resubmitted.Notify(ctx, pool, id, actorEmployeeID)
	return Get(ctx, pool, id)
}

// SoftDelete marks a credit memo deleted (paired deleted_at/deleted_by).
// Blocked while any live credit_memo_application references it — unapply (or
// void, which cascades) first, so every visible ledger row's parent memo stays
// resolvable (spec AD-16).
func SoftDelete(ctx context.Context, pool *pgxpool.Pool, id string, actorEmployeeID int) error {
	internalID, _, err := internalIDByUUID(ctx, pool, id)
	if err != nil {
		return err
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin delete credit memo: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Lock order is credit_memo < payment. A memo issued from a payment gives its
	// money back to that payment when it goes, so the payment is locked and its
	// rollup recomputed in the same transaction.
	var sourcePaymentID *int
	if err := tx.QueryRow(ctx, `
		SELECT credit_memo_source_payment_id FROM credit_memo
		WHERE credit_memo_id = $1 AND credit_memo_deleted_at IS NULL FOR UPDATE`, internalID,
	).Scan(&sourcePaymentID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("lock credit memo for delete: %w", err)
	}
	var liveApplications int
	if err := tx.QueryRow(ctx,
		`SELECT COUNT(*) FROM credit_memo_application WHERE credit_memo_id = $1 AND application_deleted_at IS NULL`,
		internalID).Scan(&liveApplications); err != nil {
		return fmt.Errorf("count live credit applications: %w", err)
	}
	if liveApplications > 0 {
		return ClientError{Msg: "Cannot delete a credit memo with live applications; unapply them first."}
	}
	if sourcePaymentID != nil {
		if _, err := lockLinkedPayment(ctx, tx, *sourcePaymentID); err != nil {
			return err
		}
	}

	tag, err := tx.Exec(ctx, `
		UPDATE credit_memo
		SET credit_memo_deleted_at = NOW(), credit_memo_deleted_by = $1
		WHERE credit_memo_id = $2 AND credit_memo_deleted_at IS NULL`,
		actorOrSystem(actorEmployeeID), internalID)
	if err != nil {
		return fmt.Errorf("delete credit memo: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if sourcePaymentID != nil {
		if err := recomputePaymentCredited(ctx, tx, *sourcePaymentID, actorEmployeeID); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit delete credit memo: %w", err)
	}
	return nil
}
