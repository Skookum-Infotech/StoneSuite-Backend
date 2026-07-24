package salesorder

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/query"
)

// ----- Search ----------------------------------------------------------------

// Search lists orders under the caller's RBAC scope with filter/sort/global
// search + keyset pagination, mirroring relationalStore.SearchRecords. List
// rows omit line items (spec: avoid an N+1 join on list).
func Search(ctx context.Context, pool *pgxpool.Pool, scope, actorIdentityID string, req query.Request) (Page, error) {
	where := []string{"so.sales_order_deleted_at IS NULL"}
	var args []any
	nextIdx := 1
	if scope != string(authz.ScopeAll) {
		empID, found := employeeIDByIdentity(ctx, pool, actorIdentityID)
		if !found {
			return Page{}, nil
		}
		where = append(where, fmt.Sprintf("so.sales_order_owner_id = $%d", nextIdx))
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

	q := orderSelect + " WHERE " + strings.Join(where, " AND ") +
		" ORDER BY " + built.OrderBy + " LIMIT " + strconv.Itoa(built.EffLimit+1)

	rows, err := pool.Query(ctx, q, args...)
	if err != nil {
		return Page{}, fmt.Errorf("search sales orders: %w", err)
	}
	defer rows.Close()
	out := []Order{}
	metas := []orderMeta{}
	for rows.Next() {
		o, meta, err := scanOrder(rows)
		if err != nil {
			return Page{}, err
		}
		out = append(out, *o)
		metas = append(metas, meta)
	}
	if err := rows.Err(); err != nil {
		return Page{}, fmt.Errorf("search sales orders: %w", err)
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

// sortValue reads the effective sort field's value from an order to mint the
// next cursor. Every key in resolver.go's sortableFields must appear here, or
// the cursor is minted from the wrong column and pages after the first are
// wrong. `status` and `customer_id` sort on internal numeric ids the response
// struct does not carry — orderMeta supplies them.
func sortValue(o Order, meta orderMeta, field string) any {
	switch field {
	case "updated_at":
		return o.UpdatedAt
	case "grand_total":
		return o.GrandTotal
	case "order_date":
		return o.OrderDate
	case "document_number", "record_number":
		return o.Number
	case "status":
		return meta.statusID
	case "customer_id":
		return meta.customerID
	default: // created_at (default)
		return o.CreatedAt
	}
}

// employeeIDByIdentity resolves a control-plane identity to a tenant
// employee_id, mirroring crmstore.relationalStore.employeeIDByIdentity.
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
