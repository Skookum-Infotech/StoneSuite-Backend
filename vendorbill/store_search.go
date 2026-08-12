// vendorbill/store_search.go
package vendorbill

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
// employee_id.
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

// Search lists vendor bills under the caller's RBAC scope with filter/sort/
// global search + keyset pagination. Scope x filter is ANDed -- a filter can
// only narrow the permitted set. List rows include lines already loaded by
// scanVendorBill's Items=[]Line{} default (empty, not nil) -- Search does not
// N+1-load lines, mirroring purchaseorder.Search.
func Search(ctx context.Context, pool *pgxpool.Pool, scope, actorIdentityID string, req query.Request) (Page, error) {
	where := []string{"vb.vendor_bill_deleted_at IS NULL"}
	var args []any
	nextIdx := 1
	if scope != string(authz.ScopeAll) {
		empID, found := employeeIDByIdentity(ctx, pool, actorIdentityID)
		if !found {
			return Page{}, nil
		}
		where = append(where, fmt.Sprintf("vb.vendor_bill_owner_id = $%d", nextIdx))
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

	q := vbSelect + " WHERE " + strings.Join(where, " AND ") +
		" ORDER BY " + built.OrderBy + " LIMIT " + strconv.Itoa(built.EffLimit+1)

	rows, err := pool.Query(ctx, q, args...)
	if err != nil {
		return Page{}, fmt.Errorf("search vendor bills: %w", err)
	}
	defer rows.Close()
	out := []VendorBill{}
	metas := []vbMeta{}
	for rows.Next() {
		b, meta, err := scanVendorBill(rows)
		if err != nil {
			return Page{}, err
		}
		out = append(out, *b)
		metas = append(metas, meta)
	}
	if err := rows.Err(); err != nil {
		return Page{}, fmt.Errorf("search vendor bills: %w", err)
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

// sortValue reads the effective sort field's value from a vendor bill to
// mint the next cursor. Every key in resolver.go's sortFields must appear
// here, or the cursor is built from the wrong column and every page after
// the first is wrong.
func sortValue(b VendorBill, meta vbMeta, field string) any {
	switch field {
	case "bill_date":
		return b.BillDate
	case "grand_total":
		return b.GrandTotal
	case "balance_due":
		return b.BalanceDue
	case "document_number", "record_number":
		return b.Number
	case "status":
		return meta.statusID
	case "vendor_id":
		return meta.vendorID
	default: // created_at (default)
		return b.CreatedAt
	}
}
