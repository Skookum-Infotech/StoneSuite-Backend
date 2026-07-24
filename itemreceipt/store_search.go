// itemreceipt/store_search.go
package itemreceipt

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
// employee_id, mirroring purchaseorder.employeeIDByIdentity.
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

// Search lists item receipts under the caller's RBAC scope with filter/sort/
// global search + keyset pagination. Scope × filter is ANDed — a filter can
// only narrow the permitted set, never widen it. List rows omit line items to
// avoid an N+1 join.
func Search(ctx context.Context, pool *pgxpool.Pool, scope, actorIdentityID string, req query.Request) (Page, error) {
	where := []string{"ir.item_receipt_deleted_at IS NULL"}
	var args []any
	nextIdx := 1
	if scope != string(authz.ScopeAll) {
		empID, found := employeeIDByIdentity(ctx, pool, actorIdentityID)
		if !found {
			return Page{}, nil
		}
		where = append(where, fmt.Sprintf("ir.item_receipt_owner_id = $%d", nextIdx))
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

	q := irSelect + " WHERE " + strings.Join(where, " AND ") +
		" ORDER BY " + built.OrderBy + " LIMIT " + strconv.Itoa(built.EffLimit+1)

	rows, err := pool.Query(ctx, q, args...)
	if err != nil {
		return Page{}, fmt.Errorf("search item receipts: %w", err)
	}
	defer rows.Close()
	out := []ItemReceipt{}
	for rows.Next() {
		r, err := scanItemReceipt(rows)
		if err != nil {
			return Page{}, err
		}
		out = append(out, *r)
	}
	if err := rows.Err(); err != nil {
		return Page{}, fmt.Errorf("search item receipts: %w", err)
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

// ForPurchaseOrder lists every live receipt raised against one purchase order,
// newest first. It backs GET /api/tenant/purchase-orders/{uuid}/receipts,
// whose RBAC and IDOR guard are the purchase order's, not the receipt's — so
// there is deliberately no scope filter here.
func ForPurchaseOrder(ctx context.Context, pool *pgxpool.Pool, poUUID string) ([]ItemReceipt, error) {
	rows, err := pool.Query(ctx, irSelect+`
		WHERE ir.item_receipt_deleted_at IS NULL AND po.purchase_order_uuid = $1
		ORDER BY ir.item_receipt_created_at DESC, ir.item_receipt_id DESC`, poUUID)
	if err != nil {
		return nil, fmt.Errorf("list receipts for purchase order: %w", err)
	}
	defer rows.Close()
	out := []ItemReceipt{}
	for rows.Next() {
		r, err := scanItemReceipt(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

// sortValue reads the effective sort field's value from a receipt to mint the
// next cursor.
//
// Every key in resolver.go's sortableFields must appear here. A missing case
// falls through to created_at, which mints a cursor from the wrong column and
// silently corrupts page 2 onward — the failure is invisible in any single-page
// test. TestSortValueCoversEverySortableField guards the correspondence.
func sortValue(r ItemReceipt, field string) any {
	switch field {
	case "updated_at":
		return r.UpdatedAt
	case "receipt_date":
		return r.ReceiptDate
	case "warehouse_id":
		return r.WarehouseID
	case "document_number", "record_number":
		return r.Number
	default: // created_at (default)
		return r.CreatedAt
	}
}
