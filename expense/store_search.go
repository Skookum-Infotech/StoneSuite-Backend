// expense/store_search.go
package expense

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/query"
)

// employeeIDByIdentity resolves a control-plane identity to a tenant
// employee_id, mirroring requisition.employeeIDByIdentity.
func employeeIDByIdentity(ctx context.Context, pool *pgxpool.Pool, identityID string) (int, bool) {
	if identityID == "" {
		return 0, false
	}
	var id int
	err := pool.QueryRow(ctx, `
		SELECT e.employee_id FROM employee e
		JOIN users u ON u.id = e.employee_user_id
		WHERE u.identity_id = $1 AND e.employee_deleted_at IS NULL`, identityID).Scan(&id)
	if err != nil {
		return 0, false
	}
	return id, true
}

// Search lists expense claims under the caller's RBAC scope with filter/sort/
// global search + keyset pagination. Scope × filter is ANDed — a filter can
// only narrow the permitted set. "own" scope narrows to claims the caller
// filed (AD-2). List rows omit line items to avoid an N+1 join.
func Search(ctx context.Context, pool *pgxpool.Pool, scope, actorIdentityID string, req query.Request) (Page, error) {
	where := []string{"exp.expense_deleted_at IS NULL"}
	var args []any
	nextIdx := 1
	if scope != string(authz.ScopeAll) {
		empID, found := employeeIDByIdentity(ctx, pool, actorIdentityID)
		if !found {
			return Page{}, nil
		}
		where = append(where, fmt.Sprintf("exp.expense_claimant_id = $%d", nextIdx))
		args = append(args, empID)
		nextIdx++
	}

	built, err := query.Build(req, resolver{}, nextIdx)
	if err != nil {
		return Page{}, err
	}
	if built.Where != "" {
		where = append(where, built.Where)
	}
	if built.Keyset != "" {
		where = append(where, built.Keyset)
	}
	args = append(args, built.Args...)

	q := expSelect + " WHERE " + strings.Join(where, " AND ") +
		" ORDER BY " + built.OrderBy + " LIMIT " + strconv.Itoa(built.EffLimit+1)

	rows, err := pool.Query(ctx, q, args...)
	if err != nil {
		return Page{}, fmt.Errorf("search expenses: %w", err)
	}
	defer rows.Close()
	out := []Expense{}
	metas := []expMeta{}
	for rows.Next() {
		p, meta, err := scanExpense(rows)
		if err != nil {
			return Page{}, err
		}
		out = append(out, *p)
		metas = append(metas, meta)
	}
	if err := rows.Err(); err != nil {
		return Page{}, fmt.Errorf("search expenses: %w", err)
	}

	page := Page{Records: out}
	if len(out) > built.EffLimit {
		page.HasMore = true
		page.Records = out[:built.EffLimit]
		lastIdx := built.EffLimit - 1
		last, lastMeta := page.Records[lastIdx], metas[lastIdx]
		page.NextCursor = query.NextCursor(last.ID, built.Sort, sortValue(last, lastMeta, built.Sort.Field))
	}
	return page, nil
}

// sortValue reads the effective sort field's value from an expense claim to
// mint the next cursor.
//
// Every key in resolver.go's sortableFields must appear here. A missing case
// falls through to created_at, so the cursor is built from a different column
// than the one the query ordered by — page 1 looks right and every page after
// it is wrong. `status` sorts on an internal numeric id the response struct
// does not carry, which is what expMeta is for.
// TestSortValueCoversEverySortableField guards the correspondence.
func sortValue(p Expense, meta expMeta, field string) any {
	switch field {
	case "total":
		return p.Total
	case "document_number", "record_number":
		return p.Number
	case "status":
		return meta.statusID
	default: // created_at (default)
		return p.CreatedAt
	}
}
