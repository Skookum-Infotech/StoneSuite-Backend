package inventorytransfer

// store_search.go — list/search and the status trail.

import (
	"context"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/query"
)

// Search returns a keyset-paginated page of transfers, headers only.
func Search(ctx context.Context, pool *pgxpool.Pool, req query.Request) (Page, error) {
	built, err := query.Build(req, transferResolver{}, 1)
	if err != nil {
		return Page{}, err
	}
	where := "t.transfer_deleted_at IS NULL"
	if built.Where != "" {
		where += " AND " + built.Where
	}
	if built.Keyset != "" {
		where += " AND " + built.Keyset
	}
	q := headerSelect + " WHERE " + where +
		" ORDER BY " + built.OrderBy + " LIMIT " + strconv.Itoa(built.EffLimit+1)

	rows, err := pool.Query(ctx, q, built.Args...)
	if err != nil {
		return Page{}, fmt.Errorf("search transfers: %w", err)
	}
	defer rows.Close()
	out := []Transfer{}
	for rows.Next() {
		t, err := scanHeader(rows)
		if err != nil {
			return Page{}, fmt.Errorf("scan transfer: %w", err)
		}
		out = append(out, *t)
	}
	if err := rows.Err(); err != nil {
		return Page{}, fmt.Errorf("search transfers: %w", err)
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

// sortValue must stay in step with transferSortable — a sortable field missing
// here would mint the cursor from the wrong column and silently skip rows.
func sortValue(t Transfer, field string) any {
	switch field {
	case "updated_at":
		return t.UpdatedAt
	case "date":
		return t.Date
	case "status":
		return t.StatusCode
	default: // created_at
		return t.CreatedAt
	}
}

// InTransit returns transfers that have shipped and not landed — the stock that
// inventory_stock deliberately does not count anywhere.
func InTransit(ctx context.Context, pool *pgxpool.Pool) ([]Transfer, error) {
	rows, err := pool.Query(ctx, headerSelect+`
		WHERE t.transfer_deleted_at IS NULL AND rs.record_status_code = $1
		ORDER BY t.transfer_shipped_at`, StatusInTransit)
	if err != nil {
		return nil, fmt.Errorf("load in-transit transfers: %w", err)
	}
	defer rows.Close()
	out := []Transfer{}
	for rows.Next() {
		t, err := scanHeader(rows)
		if err != nil {
			return nil, fmt.Errorf("scan transfer: %w", err)
		}
		out = append(out, *t)
	}
	return out, rows.Err()
}

// History returns the document's status trail, newest first.
func History(ctx context.Context, pool *pgxpool.Pool, uuid string) ([]HistoryEntry, error) {
	rows, err := pool.Query(ctx, `
		SELECT h.action, COALESCE(fs.record_status_code,''), COALESCE(ts.record_status_code,''),
		       to_char(h.at, 'YYYY-MM-DD"T"HH24:MI:SS'),
		       COALESCE(NULLIF(TRIM(COALESCE(e.employee_first_name,'') || ' ' ||
		                            COALESCE(e.employee_last_name,'')), ''), 'System')
		FROM inventory_transfer_history h
		JOIN inventory_transfer t      ON t.inventory_transfer_id = h.inventory_transfer_id
		LEFT JOIN lkp_record_status fs ON fs.record_status_id = h.from_status_id
		LEFT JOIN lkp_record_status ts ON ts.record_status_id = h.to_status_id
		LEFT JOIN employee e           ON e.employee_id = h.actor_employee_id
		WHERE t.inventory_transfer_uuid = $1
		ORDER BY h.at DESC, h.inventory_transfer_history_id DESC
		LIMIT 200`, uuid)
	if err != nil {
		if isInvalidTextRepresentation(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("load transfer history: %w", err)
	}
	defer rows.Close()
	out := []HistoryEntry{}
	for rows.Next() {
		var e HistoryEntry
		if err := rows.Scan(&e.Action, &e.FromStatus, &e.ToStatus, &e.At, &e.ByName); err != nil {
			return nil, fmt.Errorf("scan transfer history: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
