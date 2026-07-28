package inventorycount

// store_search.go — list/search, the resolver whitelist and the status trail.

import (
	"context"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/query"
)

type resolved struct {
	expr string
	dt   query.DataType
}

// countResolver implements query.FieldResolver (+ SortResolver +
// SearchResolver). A key not in this map is a *query.InvalidFilterError, which
// controllers map to 400 — the whitelist is the boundary, so an unrecognised
// key can never reach the database as an identifier.
type countResolver struct{}

var countFields = map[string]resolved{
	"id":           {"c.inventory_count_uuid::text", query.TypeString},
	"number":       {"c.count_number", query.TypeString},
	"status":       {"rs.record_status_code", query.TypeString},
	"warehouse_id": {"c.warehouse_id", query.TypeNumber},
	"bin_id":       {"b.inventory_bin_uuid::text", query.TypeString},
	"bin_path":     {"COALESCE(b.bin_path,'')", query.TypeString},
	"owner_id":     {"c.count_owner_id", query.TypeNumber},
	"date":         {"c.count_date", query.TypeDate},
	"frozen_at":    {"c.count_frozen_at", query.TypeDate},
	"posted_at":    {"c.count_posted_at", query.TypeDate},
	"created_at":   {"c.count_created_at", query.TypeDate},
	"updated_at":   {"c.count_updated_at", query.TypeDate},
}

func (countResolver) Resolve(key string) (string, query.DataType, bool) {
	if s, ok := countFields[key]; ok {
		return s.expr, s.dt, true
	}
	return "", "", false
}

// countSortable is restricted to NOT NULL columns: frozen_at, posted_at and
// count_number are nullable, and a keyset cursor over a nullable column skips
// or repeats rows.
var countSortable = map[string]resolved{
	"date":   {"c.count_date", query.TypeDate},
	"status": {"rs.record_status_code", query.TypeString},
}

func (countResolver) SortExpr(key string) (string, query.DataType, bool) {
	if s, ok := countSortable[key]; ok {
		return s.expr, s.dt, true
	}
	return "", "", false
}

// SearchPredicate powers the free-text box: document number, notes, warehouse
// or the bin being counted.
func (countResolver) SearchPredicate(ph string) string {
	return "(COALESCE(c.count_number,'') ILIKE '%'||" + ph + "||'%' OR c.count_notes ILIKE '%'||" + ph +
		"||'%' OR w.warehouse_name ILIKE '%'||" + ph + "||'%' OR COALESCE(b.bin_path,'') ILIKE '%'||" + ph + "||'%')"
}

var _ query.FieldResolver = countResolver{}
var _ query.SortResolver = countResolver{}
var _ query.SearchResolver = countResolver{}

// Search returns a keyset-paginated page of counts, headers only.
func Search(ctx context.Context, pool *pgxpool.Pool, req query.Request) (Page, error) {
	built, err := query.Build(req, countResolver{}, 1)
	if err != nil {
		return Page{}, err
	}
	where := "c.count_deleted_at IS NULL"
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
		return Page{}, fmt.Errorf("search counts: %w", err)
	}
	defer rows.Close()
	out := []Count{}
	for rows.Next() {
		c, err := scanHeader(rows)
		if err != nil {
			return Page{}, fmt.Errorf("scan count: %w", err)
		}
		out = append(out, *c)
	}
	if err := rows.Err(); err != nil {
		return Page{}, fmt.Errorf("search counts: %w", err)
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

// sortValue must stay in step with countSortable — a sortable field missing
// here would mint the cursor from the wrong column and silently skip rows.
func sortValue(c Count, field string) any {
	switch field {
	case "updated_at":
		return c.UpdatedAt
	case "date":
		return c.Date
	case "status":
		return c.StatusCode
	default: // created_at
		return c.CreatedAt
	}
}

// History returns the document's status trail, newest first.
func History(ctx context.Context, pool *pgxpool.Pool, uuid string) ([]HistoryEntry, error) {
	rows, err := pool.Query(ctx, `
		SELECT h.action, COALESCE(fs.record_status_code,''), COALESCE(ts.record_status_code,''),
		       to_char(h.at, 'YYYY-MM-DD"T"HH24:MI:SS'),
		       COALESCE(NULLIF(TRIM(COALESCE(e.employee_first_name,'') || ' ' ||
		                            COALESCE(e.employee_last_name,'')), ''), 'System')
		FROM inventory_count_history h
		JOIN inventory_count c         ON c.inventory_count_id = h.inventory_count_id
		LEFT JOIN lkp_record_status fs ON fs.record_status_id = h.from_status_id
		LEFT JOIN lkp_record_status ts ON ts.record_status_id = h.to_status_id
		LEFT JOIN employee e           ON e.employee_id = h.actor_employee_id
		WHERE c.inventory_count_uuid = $1
		ORDER BY h.at DESC, h.inventory_count_history_id DESC
		LIMIT 200`, uuid)
	if err != nil {
		if isInvalidTextRepresentation(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("load count history: %w", err)
	}
	defer rows.Close()
	out := []HistoryEntry{}
	for rows.Next() {
		var e HistoryEntry
		if err := rows.Scan(&e.Action, &e.FromStatus, &e.ToStatus, &e.At, &e.ByName); err != nil {
			return nil, fmt.Errorf("scan count history: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
