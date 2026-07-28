package cashtransfer

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

// Search lists live cash transfers with server-side filter/sort/global-search
// + keyset pagination.
func Search(ctx context.Context, pool *pgxpool.Pool, scope, actorIdentityID string, req query.Request) (Page, error) {
	where := []string{"ct.cash_transfer_deleted_at IS NULL"}
	args := []any{}
	nextIdx := 1
	if scope != string(authz.ScopeAll) {
		empID, found := workflow.EmployeeIDByIdentity(ctx, pool, actorIdentityID)
		if !found {
			return Page{}, nil
		}
		where = append(where, fmt.Sprintf("ct.cash_transfer_owner_id = $%d", nextIdx))
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
		return Page{}, fmt.Errorf("search cash transfers: %w", err)
	}
	defer rows.Close()
	out := []CashTransfer{}
	metas := []ctMeta{}
	for rows.Next() {
		ct, meta, err := scanCT(rows)
		if err != nil {
			return Page{}, fmt.Errorf("scan cash transfer: %w", err)
		}
		out = append(out, *ct)
		metas = append(metas, meta)
	}
	if err := rows.Err(); err != nil {
		return Page{}, fmt.Errorf("search cash transfers: %w", err)
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

func sortValue(ct CashTransfer, meta ctMeta, field string) any {
	switch field {
	case "updated_at":
		return ct.UpdatedAt
	case "document_number", "record_number":
		return ct.Number
	case "transfer_date":
		return ct.TransferDate
	case "amount":
		return ct.Amount
	case "status":
		return meta.statusID
	default: // created_at (default)
		return ct.CreatedAt
	}
}
