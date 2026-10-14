package bintransfer

// store_search.go — list/search and the status trail.

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

// Search lists live bin transfers under the caller's RBAC scope with
// filter/sort/global-search + keyset pagination. Scope x filter is ANDed -- a
// filter can only narrow the caller's permitted set, never widen it. Rows are
// headers only; a single read carries the lines.
func Search(ctx context.Context, pool *pgxpool.Pool, scope, actorIdentityID string, req query.Request) (Page, error) {
	where := []string{"bt.bin_transfer_deleted_at IS NULL"}
	var args []any
	nextIdx := 1
	if scope != string(authz.ScopeAll) {
		empID, found := workflow.EmployeeIDByIdentity(ctx, pool, actorIdentityID)
		if !found {
			return Page{}, nil
		}
		where = append(where, fmt.Sprintf("bt.bin_transfer_owner_id = $%d", nextIdx))
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
		return Page{}, fmt.Errorf("search bin transfers: %w", err)
	}
	defer rows.Close()
	out := []BinTransfer{}
	metas := []headerMeta{}
	for rows.Next() {
		t, meta, err := scanHeader(rows)
		if err != nil {
			return Page{}, fmt.Errorf("scan bin transfer: %w", err)
		}
		out = append(out, *t)
		metas = append(metas, meta)
	}
	if err := rows.Err(); err != nil {
		return Page{}, fmt.Errorf("search bin transfers: %w", err)
	}

	page := Page{Records: out}
	if len(out) > built.EffLimit {
		page.HasMore = true
		page.Records = out[:built.EffLimit]
		lastIdx := built.EffLimit - 1
		page.NextCursor = query.NextCursor(page.Records[lastIdx].ID, built.Sort,
			sortValue(page.Records[lastIdx], metas[lastIdx], built.Sort.Field))
	}

	ptrs := make([]*BinTransfer, len(page.Records))
	for i := range page.Records {
		ptrs[i] = &page.Records[i]
	}
	if err := fillNextStatusCodes(ctx, pool, ptrs...); err != nil {
		return Page{}, err
	}
	return page, nil
}

// sortValue reads the effective sort field's value to mint the next cursor.
// Every key in resolver.go's sortFields must appear here, or the cursor is built
// from the wrong column and every page after the first is wrong.
func sortValue(t BinTransfer, meta headerMeta, field string) any {
	switch field {
	case "transfer_date":
		return t.Date
	case "document_number", "record_number":
		return t.Number
	case "status":
		return meta.statusID
	case "updated_at":
		return t.UpdatedAt
	default: // created_at
		return t.CreatedAt
	}
}

// History returns the document's status trail, newest first.
func History(ctx context.Context, pool *pgxpool.Pool, uuid string) ([]HistoryEntry, error) {
	rows, err := pool.Query(ctx, `
		SELECT h.action, COALESCE(fs.record_status_code,''), COALESCE(ts.record_status_code,''),
		       to_char(h.at, 'YYYY-MM-DD"T"HH24:MI:SS'),
		       COALESCE(NULLIF(TRIM(COALESCE(e.employee_first_name,'') || ' ' ||
		                            COALESCE(e.employee_last_name,'')), ''), 'System')
		FROM bin_transfer_history h
		JOIN bin_transfer bt           ON bt.bin_transfer_id = h.bin_transfer_id
		LEFT JOIN lkp_record_status fs ON fs.record_status_id = h.from_status_id
		LEFT JOIN lkp_record_status ts ON ts.record_status_id = h.to_status_id
		LEFT JOIN employee e           ON e.employee_id = h.actor_employee_id
		WHERE bt.bin_transfer_uuid = $1 AND bt.bin_transfer_deleted_at IS NULL
		ORDER BY h.at DESC, h.bin_transfer_history_id DESC
		LIMIT 200`, uuid)
	if err != nil {
		if isInvalidText(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("load bin transfer history: %w", err)
	}
	defer rows.Close()
	out := []HistoryEntry{}
	for rows.Next() {
		var e HistoryEntry
		if err := rows.Scan(&e.Action, &e.FromStatus, &e.ToStatus, &e.At, &e.ByName); err != nil {
			return nil, fmt.Errorf("scan bin transfer history: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
