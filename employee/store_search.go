package employee

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/query"
)

// Search lists live employees with filter/sort/global-search + keyset
// pagination. Employees are tenant-wide configuration data, not owned records,
// so there is no per-row scope: the employee:read grant covers the whole list.
func Search(ctx context.Context, pool *pgxpool.Pool, req query.Request) (Page, error) {
	where := []string{empLive}

	built, err := query.Build(req, resolver{}, 1)
	if err != nil {
		return Page{}, err
	}
	if built.Where != "" {
		where = append(where, built.Where)
	}
	if built.Keyset != "" {
		where = append(where, built.Keyset)
	}

	q := empSelect + " WHERE " + strings.Join(where, " AND ") +
		" ORDER BY " + built.OrderBy + " LIMIT " + strconv.Itoa(built.EffLimit+1)

	rows, err := pool.Query(ctx, q, built.Args...)
	if err != nil {
		return Page{}, fmt.Errorf("search employees: %w", err)
	}
	defer rows.Close()
	out := []Employee{}
	metas := []empMeta{}
	for rows.Next() {
		e, meta, err := scanEmployee(rows)
		if err != nil {
			return Page{}, fmt.Errorf("scan employee: %w", err)
		}
		out = append(out, *e)
		metas = append(metas, meta)
	}
	if err := rows.Err(); err != nil {
		return Page{}, fmt.Errorf("search employees: %w", err)
	}

	page := Page{Records: out}
	if len(out) > built.EffLimit {
		page.HasMore = true
		page.Records = out[:built.EffLimit]
		lastIdx := built.EffLimit - 1
		page.NextCursor = query.NextCursor(page.Records[lastIdx].ID, built.Sort,
			sortValue(page.Records[lastIdx], metas[lastIdx], built.Sort.Field))
	}
	return page, nil
}

// sortValue reads the effective sort field's value to mint the next cursor.
// Every key in resolver.go's sortFields must appear here, or the cursor is built
// from the wrong column and every page after the first is wrong.
func sortValue(e Employee, meta empMeta, field string) any {
	switch field {
	case "name":
		return strings.ToLower(strings.TrimSpace(e.FirstName + " " + e.LastName))
	case "email":
		return strings.ToLower(e.Email)
	case "document_number", "record_number":
		return e.Number
	case "status":
		return meta.statusID
	case "updated_at":
		return e.UpdatedAt
	default: // created_at (default)
		return e.CreatedAt
	}
}

// History lists an employee's status trail, newest first.
func History(ctx context.Context, pool *pgxpool.Pool, uuid string) ([]HistoryEntry, error) {
	var internalID int
	if err := pool.QueryRow(ctx,
		`SELECT employee_id FROM employee e WHERE e.employee_uuid = $1 AND `+empLive, uuid).Scan(&internalID); err != nil {
		return nil, ErrNotFound
	}
	rows, err := pool.Query(ctx, `
		SELECT h.action, COALESCE(fs.record_status_name,''), COALESCE(ts.record_status_name,''),
		       COALESCE(NULLIF(TRIM(a.employee_first_name || ' ' || a.employee_last_name),''), a.employee_email, ''),
		       h.at
		FROM employee_history h
		LEFT JOIN lkp_record_status fs ON fs.record_status_id = h.from_status_id
		LEFT JOIN lkp_record_status ts ON ts.record_status_id = h.to_status_id
		LEFT JOIN employee a ON a.employee_id = h.actor_employee_id
		WHERE h.employee_id = $1
		ORDER BY h.at DESC, h.employee_history_id DESC`, internalID)
	if err != nil {
		return nil, fmt.Errorf("load employee history: %w", err)
	}
	defer rows.Close()
	out := []HistoryEntry{}
	for rows.Next() {
		var h HistoryEntry
		if err := rows.Scan(&h.Action, &h.FromStatus, &h.ToStatus, &h.ActorName, &h.At); err != nil {
			return nil, fmt.Errorf("scan employee history: %w", err)
		}
		out = append(out, h)
	}
	return out, rows.Err()
}
