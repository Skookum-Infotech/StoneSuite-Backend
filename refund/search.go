package refund

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

// Search lists live refunds with server-side filter/sort/global-search +
// keyset pagination. Note: Search returns headers only (Applications is
// always an empty slice on each record).
func Search(ctx context.Context, pool *pgxpool.Pool, scope, actorIdentityID string, req query.Request) (Page, error) {
	where := []string{"rfnd.refund_deleted_at IS NULL"}
	args := []any{}
	nextIdx := 1
	if scope == string(authz.ScopeOwn) {
		empID, found := workflow.EmployeeIDByIdentity(ctx, pool, actorIdentityID)
		if !found {
			return Page{}, nil
		}
		where = append(where, fmt.Sprintf("rfnd.refund_owner_id = $%d", nextIdx))
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
		return Page{}, fmt.Errorf("search refunds: %w", err)
	}
	defer rows.Close()
	out := []Refund{}
	metas := []refundMeta{}
	for rows.Next() {
		rf, meta, err := scanRefund(rows)
		if err != nil {
			return Page{}, fmt.Errorf("scan refund: %w", err)
		}
		out = append(out, *rf)
		metas = append(metas, meta)
	}
	if err := rows.Err(); err != nil {
		return Page{}, fmt.Errorf("search refunds: %w", err)
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

func sortValue(rf Refund, meta refundMeta, field string) any {
	switch field {
	case "updated_at":
		return rf.UpdatedAt
	case "document_number", "record_number":
		return rf.Number
	case "refund_date":
		return rf.RefundDate
	case "amount":
		return rf.Amount
	case "unapplied_amount":
		return rf.UnappliedAmount
	case "status":
		return meta.statusID
	case "customer_id":
		return meta.customerID
	default: // created_at (default)
		return rf.CreatedAt
	}
}
