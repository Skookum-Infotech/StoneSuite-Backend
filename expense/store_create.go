// expense/store_create.go
package expense

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/workflow"
)

// resolvedLine is a line after category resolution, ready to insert.
type resolvedLine struct {
	lineNumber                 int
	categoryID                 int
	categoryCode, categoryName string
	expenseDate                string
	amount                     float64
	description                string
}

// resolveLines validates and resolves every input line against the
// lkp_expense_category lookup.
func resolveLines(ctx context.Context, q workflow.Querier, items []LineInput) ([]resolvedLine, error) {
	if len(items) == 0 {
		return nil, ClientError{Msg: "At least one expense line is required."}
	}
	out := make([]resolvedLine, 0, len(items))
	seenLine := map[int]bool{}
	for _, in := range items {
		if in.LineNumber <= 0 {
			return nil, ClientError{Msg: "Each expense line needs a positive line number."}
		}
		if seenLine[in.LineNumber] {
			return nil, ClientError{Msg: fmt.Sprintf("Duplicate line number %d.", in.LineNumber)}
		}
		seenLine[in.LineNumber] = true
		if in.Amount < 0 {
			return nil, ClientError{Msg: fmt.Sprintf("Line %d: amount cannot be negative.", in.LineNumber)}
		}
		if strings.TrimSpace(in.ExpenseDate) == "" {
			return nil, ClientError{Msg: fmt.Sprintf("Line %d: expense date is required.", in.LineNumber)}
		}
		if strings.TrimSpace(in.CategoryCode) == "" {
			return nil, ClientError{Msg: fmt.Sprintf("Line %d: category is required.", in.LineNumber)}
		}

		cat, err := categoryByCode(ctx, q, in.CategoryCode)
		if err != nil {
			return nil, err
		}

		out = append(out, resolvedLine{
			lineNumber:   in.LineNumber,
			categoryID:   cat.id,
			categoryCode: cat.code,
			categoryName: cat.name,
			expenseDate:  in.ExpenseDate,
			amount:       in.Amount,
			description:  in.Description,
		})
	}
	return out, nil
}

// insertLines bulk-inserts resolved lines as expense_item rows.
func insertLines(ctx context.Context, tx pgx.Tx, expInternalID int, lines []resolvedLine, actorEmployeeID int) error {
	for _, l := range lines {
		_, err := tx.Exec(ctx, `
			INSERT INTO expense_item (
				expense_id, line_number, category_id,
				expense_date, amount, description,
				item_created_by
			) VALUES ($1,$2,$3, $4,$5,$6, $7)`,
			expInternalID, l.lineNumber, l.categoryID,
			l.expenseDate, l.amount, l.description,
			nullableInt(actorEmployeeID),
		)
		if err != nil {
			return fmt.Errorf("insert expense item: %w", err)
		}
	}
	return nil
}

// writeHistory records one expense_history row inside the caller's transaction.
func writeHistory(ctx context.Context, tx pgx.Tx, expInternalID int, action string, fromStatusID, toStatusID *int, actorEmployeeID int) {
	_, _ = tx.Exec(ctx, `
		INSERT INTO expense_history (expense_id, from_status_id, to_status_id, action, actor_employee_id)
		VALUES ($1,$2,$3,$4,$5)`,
		expInternalID, fromStatusID, toStatusID, action, nullableInt(actorEmployeeID))
}

// Create inserts a new expense claim (header + lines) inside one
// transaction: the claimant is always the acting employee (AD-2, self-
// service), who must resolve to a real, active employee (spec AD-8 layer 2)
// before anything else is validated. Resolves and totals every line,
// assigns the EXPN number, and starts the claim at DRFT.
func Create(ctx context.Context, pool *pgxpool.Pool, in CreateExpenseInput, actorEmployeeID int) (*Expense, error) {
	if actorEmployeeID <= 0 {
		return nil, ClientError{Msg: "Unable to resolve your employee record. Contact an administrator."}
	}
	if err := validateCustom(ctx, pool, in.CustomFields); err != nil {
		return nil, err
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin create expense: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	active, err := employeeActive(ctx, tx, actorEmployeeID)
	if err != nil {
		return nil, err
	}
	if !active {
		return nil, ClientError{Msg: "Your employee record is inactive; you cannot file an expense claim."}
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

	recordTypeID, err := recordTypeIDByCode(ctx, tx, expenseRecordTypeCode)
	if err != nil {
		return nil, fmt.Errorf("resolve EXPN record type: %w", err)
	}
	draftStatusID, err := statusIDByCode(ctx, tx, recordTypeID, draftStatusCode)
	if err != nil {
		return nil, fmt.Errorf("resolve DRFT status: %w", err)
	}

	custom := in.CustomFields
	if custom == nil {
		custom = map[string]any{}
	}

	cv := []colVal{
		{"record_type", recordTypeID, ""},
		{"expense_status", draftStatusID, ""},
		{"expense_claimant_id", actorEmployeeID, ""},
		{"expense_department", in.Department, ""},
		{"expense_memo", in.Memo, ""},
		{"expense_total", total, ""},
		{"expense_custom_fields", custom, ""},
		{"expense_created_by", nullableInt(actorEmployeeID), ""},
	}

	insertSQL, insertArgs := buildInsert("expense", cv, "expense_id, expense_uuid")
	var internalID int
	var newUUID string
	if err := tx.QueryRow(ctx, insertSQL, insertArgs...).Scan(&internalID, &newUUID); err != nil {
		return nil, fmt.Errorf("insert expense: %w", err)
	}

	number := FormatNumber(int64(internalID))
	if _, err := tx.Exec(ctx,
		`UPDATE expense SET expense_number = $1 WHERE expense_id = $2`, number, internalID); err != nil {
		return nil, fmt.Errorf("set expense number: %w", err)
	}

	if err := insertLines(ctx, tx, internalID, lines, actorEmployeeID); err != nil {
		return nil, err
	}

	writeHistory(ctx, tx, internalID, "create", nil, &draftStatusID, actorEmployeeID)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit create expense: %w", err)
	}
	return Get(ctx, pool, newUUID)
}
