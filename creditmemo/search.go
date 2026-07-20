package creditmemo

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

// Search lists live credit memos with server-side filter/sort/global-search +
// keyset pagination. Returns headers only (Lines and Applications are always
// empty slices on each record).
//
// The scope predicate is appended BEFORE query.Build and ANDed with whatever
// the caller filtered on, so a filter can only ever narrow the caller's
// permitted set — never widen it.
func Search(ctx context.Context, pool *pgxpool.Pool, scope, actorIdentityID string, req query.Request) (Page, error) {
	where := []string{"cm.credit_memo_deleted_at IS NULL"}
	args := []any{}
	nextIdx := 1
	if scope != string(authz.ScopeAll) {
		empID, found := workflow.EmployeeIDByIdentity(ctx, pool, actorIdentityID)
		if !found {
			return Page{}, nil
		}
		where = append(where, fmt.Sprintf("cm.credit_memo_owner_id = $%d", nextIdx))
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
		return Page{}, fmt.Errorf("search credit memos: %w", err)
	}
	defer rows.Close()
	out := []CreditMemo{}
	metas := []creditMemoMeta{}
	for rows.Next() {
		cm, meta, err := scanCreditMemo(rows)
		if err != nil {
			return Page{}, fmt.Errorf("scan credit memo: %w", err)
		}
		out = append(out, *cm)
		metas = append(metas, meta)
	}
	if err := rows.Err(); err != nil {
		return Page{}, fmt.Errorf("search credit memos: %w", err)
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

func sortValue(cm CreditMemo, meta creditMemoMeta, field string) any {
	switch field {
	case "updated_at":
		return cm.UpdatedAt
	case "document_number", "record_number":
		return cm.Number
	case "credit_memo_date":
		return cm.CreditMemoDate
	case "grand_total":
		return cm.GrandTotal
	case "applied_total":
		return cm.AppliedTotal
	case "unapplied_amount":
		return cm.UnappliedAmount
	case "status":
		return meta.statusID
	case "customer_id":
		return meta.customerID
	default: // created_at (default)
		return cm.CreatedAt
	}
}
