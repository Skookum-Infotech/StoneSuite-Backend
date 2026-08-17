// expense/store_update.go
package expense

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Update replaces a live expense claim's header fields and lines
// (recomputing the total) inside one transaction. Allowed only at DRFT --
// once submitted the claim is visible to an approver; recall it to draft
// (SUBM->DRFT) or wait for a rejection (RJCT->DRFT) to revise. The claimant
// is never changed by Update -- it is fixed at Create (AD-2).
func Update(ctx context.Context, pool *pgxpool.Pool, uuid string, in UpdateExpenseInput, actorEmployeeID int) (*Expense, error) {
	if err := validateCustom(ctx, pool, in.CustomFields); err != nil {
		return nil, err
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin update expense: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var internalID int
	var statusCode string
	err = tx.QueryRow(ctx, `
		SELECT exp.expense_id, rs.record_status_code
		FROM expense exp JOIN lkp_record_status rs ON rs.record_status_id = exp.expense_status
		WHERE exp.expense_uuid = $1 AND exp.expense_deleted_at IS NULL
		FOR UPDATE OF exp`, uuid,
	).Scan(&internalID, &statusCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load expense for update: %w", err)
	}
	if statusCode != draftStatusCode {
		return nil, ClientError{Msg: "Only a draft expense claim can be edited. Recall it to draft first."}
	}

	lines, err := resolveLines(ctx, tx, in.Items)
	if err != nil {
		return nil, err
	}
	amounts := make([]float64, len(lines))
	for i, l := range lines {
		amounts[i] = l.amount
	}
	total := ComputeHeaderTotal(amounts)

	// expense_custom_fields is NOT NULL DEFAULT '{}'; a nil map encodes as
	// SQL NULL and violates the constraint.
	custom := in.CustomFields
	if custom == nil {
		custom = map[string]any{}
	}

	cv := []colVal{
		{"expense_department", in.Department, ""},
		{"expense_memo", in.Memo, ""},
		{"expense_total", total, ""},
		{"expense_custom_fields", custom, ""},
		{"expense_updated_by", nullableInt(actorEmployeeID), ""},
	}

	updateSQL, updateArgs := buildUpdateSet("expense", []any{uuid}, cv,
		[]string{"expense_updated_at = NOW()", "expense_record_version = expense_record_version + 1"},
		"expense_uuid = $1 AND expense_deleted_at IS NULL")
	if _, err := tx.Exec(ctx, updateSQL, updateArgs...); err != nil {
		return nil, fmt.Errorf("update expense: %w", err)
	}

	if _, err := tx.Exec(ctx,
		`UPDATE expense_item SET item_deleted_at = NOW() WHERE expense_id = $1 AND item_deleted_at IS NULL`,
		internalID); err != nil {
		return nil, fmt.Errorf("clear previous expense items: %w", err)
	}
	if err := insertLines(ctx, tx, internalID, lines, actorEmployeeID); err != nil {
		return nil, err
	}

	writeHistory(ctx, tx, internalID, "update", nil, nil, actorEmployeeID)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit update expense: %w", err)
	}
	return Get(ctx, pool, uuid)
}

// SoftDelete marks a live expense claim deleted. Only a DRFT claim may be
// deleted -- once submitted it's visible to an approver and keeps its trail
// (recall it to draft first, then delete).
func SoftDelete(ctx context.Context, pool *pgxpool.Pool, uuid string, actorEmployeeID int) error {
	tag, err := pool.Exec(ctx, `
		UPDATE expense exp
		SET expense_deleted_at = NOW(), expense_deleted_by = $2
		FROM lkp_record_status rs
		WHERE exp.expense_uuid = $1 AND exp.expense_deleted_at IS NULL
		  AND rs.record_status_id = exp.expense_status
		  AND rs.record_status_code = 'DRFT'`,
		uuid, actorOrSystem(actorEmployeeID))
	if err != nil {
		return fmt.Errorf("delete expense: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// Distinguish "not found" from "found but not deletable" for the 400/404 split.
		var exists bool
		if qerr := pool.QueryRow(ctx, `
			SELECT EXISTS(SELECT 1 FROM expense
				WHERE expense_uuid = $1 AND expense_deleted_at IS NULL)`, uuid).Scan(&exists); qerr == nil && exists {
			return ClientError{Msg: "Only a draft expense claim can be deleted."}
		}
		return ErrNotFound
	}
	return nil
}
