// expense/store_get.go — the shared header SELECT + scan and Get. Split from
// store.go to respect the 300-line file cap (requisition/store_get.go split
// precedent).
package expense

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/workflow"
)

// expSelect is the base SELECT shared by Get and Search. Column order must
// match scanExpense's Scan(...) arg order exactly. Table alias `exp` matches
// resolver.go's field expressions. The claimant join is INNER
// (expense_claimant_id is NOT NULL and doubles as the IDOR scope owner).
const expSelect = `
	SELECT exp.expense_uuid, COALESCE(exp.expense_number,''),
	       rs.record_status_name, rs.record_status_code,
	       exp.expense_approval_status,
	       COALESCE(u.id::text,''),
	       exp.expense_claimant_id, exp.expense_department, exp.expense_memo,
	       exp.expense_approved_by, exp.expense_rejected_by, exp.expense_rejection_reason,
	       exp.expense_custom_fields,
	       exp.expense_total,
	       exp.expense_created_at, exp.expense_updated_at,
	       exp.expense_status
	FROM expense exp
	JOIN lkp_record_status rs ON rs.record_status_id = exp.expense_status
	JOIN employee ce ON ce.employee_id = exp.expense_claimant_id
	LEFT JOIN users u ON u.id = ce.employee_user_id`

// expMeta carries the internal numeric id an expense row has but the API
// response deliberately does not expose. Search needs it to mint a keyset
// cursor for the `status` sort, mirrors requisition.reqnMeta.
type expMeta struct {
	statusID int
}

func scanExpense(row pgx.Row) (*Expense, expMeta, error) {
	var p Expense
	var meta expMeta
	var customRaw []byte
	if err := row.Scan(
		&p.ID, &p.Number, &p.Status, &p.StatusCode, &p.ApprovalStatus,
		&p.OwnerUserID,
		&p.ClaimantEmployeeID, &p.Department, &p.Memo,
		&p.ApprovedByEmployeeID, &p.RejectedByEmployeeID, &p.RejectionReason,
		&customRaw,
		&p.Total,
		&p.CreatedAt, &p.UpdatedAt,
		&meta.statusID,
	); err != nil {
		return nil, expMeta{}, err
	}
	if len(customRaw) > 0 {
		_ = json.Unmarshal(customRaw, &p.CustomFields)
	}
	return &p, meta, nil
}

// itemSelect is the base SELECT for an expense claim's live lines. Column
// order must match scanLine's Scan(...) arg order exactly.
const itemSelect = `
	SELECT ei.expense_item_uuid, ei.line_number,
	       ec.expense_category_id, ec.expense_category_code, ec.expense_category_name,
	       to_char(ei.expense_date,'YYYY-MM-DD'),
	       ei.amount, ei.description
	FROM expense_item ei
	JOIN lkp_expense_category ec ON ec.expense_category_id = ei.category_id
	WHERE ei.expense_id = $1 AND ei.item_deleted_at IS NULL
	ORDER BY ei.line_number`

func scanLine(row pgx.Rows) (Line, error) {
	var l Line
	err := row.Scan(
		&l.ID, &l.LineNumber,
		&l.CategoryID, &l.CategoryCode, &l.CategoryName,
		&l.ExpenseDate,
		&l.Amount, &l.Description,
	)
	return l, err
}

// loadLines fetches an expense claim's live lines by its external uuid.
func loadLines(ctx context.Context, q workflow.Querier, uuid string) ([]Line, error) {
	var internalID int
	if err := q.QueryRow(ctx,
		`SELECT expense_id FROM expense WHERE expense_uuid = $1`, uuid).Scan(&internalID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("resolve expense id: %w", err)
	}
	rows, err := q.Query(ctx, itemSelect, internalID)
	if err != nil {
		return nil, fmt.Errorf("load expense items: %w", err)
	}
	defer rows.Close()
	out := []Line{}
	for rows.Next() {
		l, err := scanLine(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// Get loads a single live expense claim by its external uuid, including its lines.
func Get(ctx context.Context, pool *pgxpool.Pool, uuid string) (*Expense, error) {
	p, _, err := scanExpense(pool.QueryRow(ctx, expSelect+`
		WHERE exp.expense_uuid = $1 AND exp.expense_deleted_at IS NULL`, uuid))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get expense: %w", err)
	}
	items, err := loadLines(ctx, pool, uuid)
	if err != nil {
		return nil, err
	}
	p.Items = items
	return p, nil
}
