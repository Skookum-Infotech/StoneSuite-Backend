// quote/store_search.go
package quote

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/query"
)

// employeeIDByIdentity resolves a control-plane identity to a tenant
// employee_id, mirroring salesorder.employeeIDByIdentity.
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

// Search lists quotes under the caller's RBAC scope with filter/sort/global
// search + keyset pagination. List rows omit line items to avoid an N+1 join.
func Search(ctx context.Context, pool *pgxpool.Pool, scope, actorIdentityID string, req query.Request) (Page, error) {
	where := []string{"est.quote_deleted_at IS NULL"}
	var args []any
	nextIdx := 1
	if scope == "own" || scope == "team" {
		empID, found := employeeIDByIdentity(ctx, pool, actorIdentityID)
		if !found {
			return Page{}, nil
		}
		where = append(where, fmt.Sprintf("est.quote_owner_id = $%d", nextIdx))
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

	q := quoteSelect + " WHERE " + strings.Join(where, " AND ") +
		" ORDER BY " + built.OrderBy + " LIMIT " + strconv.Itoa(built.EffLimit+1)

	rows, err := pool.Query(ctx, q, args...)
	if err != nil {
		return Page{}, fmt.Errorf("search quotes: %w", err)
	}
	defer rows.Close()
	out := []Quote{}
	for rows.Next() {
		e, err := scanQuote(rows)
		if err != nil {
			return Page{}, err
		}
		out = append(out, *e)
	}
	if err := rows.Err(); err != nil {
		return Page{}, fmt.Errorf("search quotes: %w", err)
	}

	page := Page{Records: out}
	if len(out) > built.EffLimit {
		page.HasMore = true
		last := out[built.EffLimit-1]
		page.Records = out[:built.EffLimit]
		page.NextCursor = query.NextCursor(last.ID, built.Sort, sortValue(last, built.Sort.Field))
	}
	return page, nil
}

// sortValue reads the effective sort field's value from an quote to mint
// the next cursor.
func sortValue(e Quote, field string) any {
	switch field {
	case "updated_at":
		return e.UpdatedAt
	case "grand_total":
		return e.GrandTotal
	case "quote_date":
		return e.QuoteDate
	case "document_number", "record_number":
		return e.Number
	default: // created_at (default)
		return e.CreatedAt
	}
}
