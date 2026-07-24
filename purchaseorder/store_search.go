// purchaseorder/store_search.go
package purchaseorder

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
// employee_id, mirroring estimate.employeeIDByIdentity.
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

// Search lists purchase orders under the caller's RBAC scope with filter/
// sort/global search + keyset pagination. Scope × filter is ANDed — a filter
// can only narrow the permitted set. List rows omit line items to avoid an
// N+1 join.
func Search(ctx context.Context, pool *pgxpool.Pool, scope, actorIdentityID string, req query.Request) (Page, error) {
	where := []string{"po.purchase_order_deleted_at IS NULL"}
	var args []any
	nextIdx := 1
	if scope != string(authz.ScopeAll) {
		empID, found := employeeIDByIdentity(ctx, pool, actorIdentityID)
		if !found {
			return Page{}, nil
		}
		where = append(where, fmt.Sprintf("po.purchase_order_owner_id = $%d", nextIdx))
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

	q := poSelect + " WHERE " + strings.Join(where, " AND ") +
		" ORDER BY " + built.OrderBy + " LIMIT " + strconv.Itoa(built.EffLimit+1)

	rows, err := pool.Query(ctx, q, args...)
	if err != nil {
		return Page{}, fmt.Errorf("search purchase orders: %w", err)
	}
	defer rows.Close()
	out := []PurchaseOrder{}
	for rows.Next() {
		p, err := scanPurchaseOrder(rows)
		if err != nil {
			return Page{}, err
		}
		out = append(out, *p)
	}
	if err := rows.Err(); err != nil {
		return Page{}, fmt.Errorf("search purchase orders: %w", err)
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

// sortValue reads the effective sort field's value from a purchase order to
// mint the next cursor.
func sortValue(p PurchaseOrder, field string) any {
	switch field {
	case "updated_at":
		return p.UpdatedAt
	case "grand_total":
		return p.GrandTotal
	case "order_date":
		return p.OrderDate
	case "document_number", "record_number":
		return p.Number
	default: // created_at (default)
		return p.CreatedAt
	}
}
