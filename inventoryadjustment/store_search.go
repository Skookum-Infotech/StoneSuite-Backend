package inventoryadjustment

// store_search.go — the list/search read, routed through query/.

import (
	"context"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/query"
)

// Search returns a keyset-paginated page of adjustments.
//
// Headers only — lines are deliberately left off a list read. A yard with a
// year of adjustments would otherwise pull tens of thousands of line rows to
// render a table that shows none of them.
func Search(ctx context.Context, pool *pgxpool.Pool, req query.Request) (Page, error) {
	built, err := query.Build(req, adjustmentResolver{}, 1)
	if err != nil {
		return Page{}, err
	}
	where := "a.adjustment_deleted_at IS NULL"
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
		return Page{}, fmt.Errorf("search adjustments: %w", err)
	}
	defer rows.Close()
	out := []Adjustment{}
	for rows.Next() {
		a, err := scanHeader(rows)
		if err != nil {
			return Page{}, fmt.Errorf("scan adjustment: %w", err)
		}
		out = append(out, *a)
	}
	if err := rows.Err(); err != nil {
		return Page{}, fmt.Errorf("search adjustments: %w", err)
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

// sortValue reads the effective sort field to mint the next cursor. It must
// stay in step with adjustmentSortable — a sortable field missing here would
// mint the cursor from the wrong column and silently skip or repeat rows.
func sortValue(a Adjustment, field string) any {
	switch field {
	case "updated_at":
		return a.UpdatedAt
	case "date":
		return a.Date
	case "status":
		return a.StatusCode
	default: // created_at
		return a.CreatedAt
	}
}

// History returns the document's status trail, newest first.
func History(ctx context.Context, pool *pgxpool.Pool, uuid string) ([]HistoryEntry, error) {
	rows, err := pool.Query(ctx, `
		SELECT h.action, COALESCE(fs.record_status_code,''), COALESCE(ts.record_status_code,''),
		       to_char(h.at, 'YYYY-MM-DD"T"HH24:MI:SS'),
		       COALESCE(NULLIF(TRIM(COALESCE(e.employee_first_name,'') || ' ' ||
		                            COALESCE(e.employee_last_name,'')), ''), 'System')
		FROM inventory_adjustment_history h
		JOIN inventory_adjustment a       ON a.inventory_adjustment_id = h.inventory_adjustment_id
		LEFT JOIN lkp_record_status fs    ON fs.record_status_id = h.from_status_id
		LEFT JOIN lkp_record_status ts    ON ts.record_status_id = h.to_status_id
		LEFT JOIN employee e              ON e.employee_id = h.actor_employee_id
		WHERE a.inventory_adjustment_uuid = $1
		ORDER BY h.at DESC, h.inventory_adjustment_history_id DESC
		LIMIT 200`, uuid)
	if err != nil {
		if isInvalidTextRepresentation(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("load adjustment history: %w", err)
	}
	defer rows.Close()
	out := []HistoryEntry{}
	for rows.Next() {
		var e HistoryEntry
		if err := rows.Scan(&e.Action, &e.FromStatus, &e.ToStatus, &e.At, &e.ByName); err != nil {
			return nil, fmt.Errorf("scan adjustment history: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// HistoryEntry is one recorded status event.
type HistoryEntry struct {
	Action     string `json:"action"`
	FromStatus string `json:"fromStatus,omitempty"`
	ToStatus   string `json:"toStatus,omitempty"`
	At         string `json:"at"`
	ByName     string `json:"byName"`
}
