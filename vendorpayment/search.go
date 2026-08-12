package vendorpayment

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

// Search lists live vendor payments with server-side filter/sort/global-search
// + keyset pagination. The RBAC scope clause and the caller's filter compose
// with AND — a filter can only narrow the caller's permitted set, never widen
// it. Note: Search returns headers only (Applications/Refunds are always
// empty slices on each record).
func Search(ctx context.Context, pool *pgxpool.Pool, scope, actorIdentityID string, req query.Request) (Page, error) {
	where := []string{"vp.vendor_payment_deleted_at IS NULL"}
	args := []any{}
	nextIdx := 1
	if scope != string(authz.ScopeAll) {
		empID, found := workflow.EmployeeIDByIdentity(ctx, pool, actorIdentityID)
		if !found {
			return Page{}, nil
		}
		where = append(where, fmt.Sprintf("vp.vendor_payment_owner_id = $%d", nextIdx))
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

	q := headerSelect + " WHERE " + strings.Join(where, " AND ") +
		" ORDER BY " + built.OrderBy + " LIMIT " + strconv.Itoa(built.EffLimit+1)

	rows, err := pool.Query(ctx, q, args...)
	if err != nil {
		return Page{}, fmt.Errorf("search vendor payments: %w", err)
	}
	defer rows.Close()
	out := []VendorPayment{}
	metas := []paymentMeta{}
	for rows.Next() {
		p, meta, err := scanVendorPayment(rows)
		if err != nil {
			return Page{}, fmt.Errorf("scan vendor payment: %w", err)
		}
		out = append(out, *p)
		metas = append(metas, meta)
	}
	if err := rows.Err(); err != nil {
		return Page{}, fmt.Errorf("search vendor payments: %w", err)
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

// sortValue extracts the value of the effective sort field from a scanned
// record, for minting the next keyset cursor.
func sortValue(p VendorPayment, meta paymentMeta, field string) any {
	switch field {
	case "updated_at":
		return p.UpdatedAt
	case "document_number", "record_number":
		return p.Number
	case "payment_date":
		return p.PaymentDate
	case "amount":
		return p.Amount
	case "unapplied_amount":
		return p.UnappliedAmount
	case "status":
		return meta.statusID
	case "vendor_id":
		return meta.vendorID
	default: // created_at (default)
		return p.CreatedAt
	}
}
