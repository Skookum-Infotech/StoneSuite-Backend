// vendorcredit/store_search.go
package vendorcredit

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/query"
	"stonesuite-backend/workflow"
)

// Search lists live vendor credits under the caller's RBAC scope with
// filter/sort/global-search + keyset pagination. Scope x filter is ANDed --
// a filter can only narrow the caller's permitted set, never widen it.
func Search(ctx context.Context, pool *pgxpool.Pool, scope, actorIdentityID string, req query.Request) (Page, error) {
	where := []string{"vc.vendor_credit_deleted_at IS NULL"}
	var args []any
	nextIdx := 1
	if scope != string(authz.ScopeAll) {
		empID, found := workflow.EmployeeIDByIdentity(ctx, pool, actorIdentityID)
		if !found {
			return Page{}, nil
		}
		where = append(where, fmt.Sprintf("vc.vendor_credit_owner_id = $%d", nextIdx))
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

	q := vcSelect + " WHERE " + strings.Join(where, " AND ") +
		" ORDER BY " + built.OrderBy + " LIMIT " + strconv.Itoa(built.EffLimit+1)

	rows, err := pool.Query(ctx, q, args...)
	if err != nil {
		return Page{}, fmt.Errorf("search vendor credits: %w", err)
	}
	defer rows.Close()
	out := []VendorCredit{}
	metas := []vcMeta{}
	for rows.Next() {
		vc, meta, err := scanVendorCredit(rows)
		if err != nil {
			return Page{}, fmt.Errorf("scan vendor credit: %w", err)
		}
		out = append(out, *vc)
		metas = append(metas, meta)
	}
	if err := rows.Err(); err != nil {
		return Page{}, fmt.Errorf("search vendor credits: %w", err)
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

// sortValue reads the effective sort field's value from a vendor credit to
// mint the next cursor. Every key in resolver.go's sortFields must appear
// here, or the cursor is built from the wrong column and every page after
// the first is wrong.
func sortValue(vc VendorCredit, meta vcMeta, field string) any {
	switch field {
	case "credit_date":
		return vc.CreditDate
	case "grand_total":
		return vc.GrandTotal
	case "unapplied_amount":
		return vc.UnappliedAmount
	case "document_number", "record_number":
		return vc.Number
	case "status":
		return meta.statusID
	case "vendor_id":
		return meta.vendorID
	case "updated_at":
		return vc.UpdatedAt
	default: // created_at (default)
		return vc.CreatedAt
	}
}
