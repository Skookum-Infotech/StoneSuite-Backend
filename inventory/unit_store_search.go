package inventory

// unit_store_search.go — list, remnant picker and history for physical units.

import (
	"context"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/query"
)

// SearchUnits lists live units with filter/sort/global-search + keyset
// pagination through the shared query engine.
//
// Like the item catalogue, physical stock is tenant-global with no per-row
// owner, so there is no RBAC scope clause to AND in — only the resource-level
// inventory_unit:read grant checked by the caller.
func SearchUnits(ctx context.Context, pool *pgxpool.Pool, req query.Request) (UnitPage, error) {
	built, err := query.Build(req, unitResolver{}, 1)
	if err != nil {
		return UnitPage{}, err
	}
	where := "s.slab_deleted_at IS NULL"
	if built.Where != "" {
		where += " AND " + built.Where
	}
	if built.Keyset != "" {
		where += " AND " + built.Keyset
	}
	q := unitSelect + " WHERE " + where +
		" ORDER BY " + built.OrderBy + " LIMIT " + strconv.Itoa(built.EffLimit+1)

	rows, err := pool.Query(ctx, q, built.Args...)
	if err != nil {
		return UnitPage{}, fmt.Errorf("search inventory units: %w", err)
	}
	defer rows.Close()
	out := []Unit{}
	for rows.Next() {
		u, err := scanUnit(rows)
		if err != nil {
			return UnitPage{}, fmt.Errorf("scan inventory unit: %w", err)
		}
		out = append(out, *u)
	}
	if err := rows.Err(); err != nil {
		return UnitPage{}, fmt.Errorf("search inventory units: %w", err)
	}

	page := UnitPage{Records: out}
	if len(out) > built.EffLimit {
		page.HasMore = true
		last := out[built.EffLimit-1]
		page.Records = out[:built.EffLimit]
		page.NextCursor = query.NextCursor(last.ID, built.Sort, unitSortValue(last, built.Sort.Field))
	}
	return page, nil
}

// unitSortValue reads the effective sort field to mint the next cursor. It must
// stay in step with unitSortableFields — a sortable field missing here would
// mint the cursor from the wrong column and silently skip or repeat rows.
func unitSortValue(u Unit, field string) any {
	switch field {
	case "updated_at":
		return u.UpdatedAt
	case "serial":
		return u.Serial
	case "area":
		return u.Area
	case "status":
		return u.Status
	default: // created_at (default)
		return u.CreatedAt
	}
}

// UsableRemnants returns available offcuts worth using, largest first.
//
// Only units flagged usable at cut time are returned. Sub-threshold offcuts
// were scrapped when they were created and never entered available stock,
// which is what keeps the yard from accumulating thousands of worthless pieces
// that inflate on-hand area and make this picker useless.
//
// Backed by idx_slab_remnant_pick, so this is one index scan.
func UsableRemnants(ctx context.Context, pool *pgxpool.Pool, itemUUID string, minArea float64) ([]Unit, error) {
	where := `s.slab_deleted_at IS NULL
	          AND s.slab_unit_kind = '` + UnitKindRemnant + `'
	          AND s.slab_is_usable_remnant = TRUE
	          AND s.slab_status = 'available'`
	args := []any{}
	if itemUUID != "" {
		args = append(args, itemUUID)
		where += fmt.Sprintf(" AND ii.inventory_item_uuid = $%d", len(args))
	}
	if minArea > 0 {
		args = append(args, minArea)
		where += fmt.Sprintf(" AND s.slab_area >= $%d", len(args))
	}

	rows, err := pool.Query(ctx, unitSelect+" WHERE "+where+
		" ORDER BY s.slab_area DESC LIMIT 200", args...)
	if err != nil {
		if isInvalidTextRepresentation(err) {
			return nil, ClientError{Msg: "Unknown inventory item."}
		}
		return nil, fmt.Errorf("load remnants: %w", err)
	}
	defer rows.Close()
	out := []Unit{}
	for rows.Next() {
		u, err := scanUnit(rows)
		if err != nil {
			return nil, fmt.Errorf("scan remnant: %w", err)
		}
		out = append(out, *u)
	}
	return out, rows.Err()
}

// UnitHistoryEntry is one recorded operational event for a physical unit.
type UnitHistoryEntry struct {
	Action   string `json:"action"`
	Field    string `json:"field"`
	OldValue string `json:"oldValue"`
	NewValue string `json:"newValue"`
	FromBin  string `json:"fromBin,omitempty"`
	ToBin    string `json:"toBin,omitempty"`
	Reason   string `json:"reason,omitempty"`
	Note     string `json:"note,omitempty"`
	At       string `json:"at"`
	ByName   string `json:"byName"`
}

// UnitHistory returns the operational trail for one unit, newest first.
func UnitHistory(ctx context.Context, pool *pgxpool.Pool, uuid string) ([]UnitHistoryEntry, error) {
	u, err := unitByUUID(ctx, pool, uuid, false)
	if err != nil {
		return nil, err
	}
	rows, err := pool.Query(ctx, `
		SELECT h.history_action, h.history_field, h.history_old_value, h.history_new_value,
		       COALESCE(fb.bin_path,''), COALESCE(tb.bin_path,''),
		       COALESCE(rs.inventory_reason_name,''), h.history_note,
		       to_char(h.history_at, 'YYYY-MM-DD"T"HH24:MI:SS'),
		       COALESCE(NULLIF(TRIM(COALESCE(e.employee_first_name,'') || ' ' ||
		                            COALESCE(e.employee_last_name,'')), ''), 'System')
		FROM inventory_unit_history h
		LEFT JOIN inventory_bin fb ON fb.inventory_bin_id = h.from_bin_id
		LEFT JOIN inventory_bin tb ON tb.inventory_bin_id = h.to_bin_id
		LEFT JOIN lkp_inventory_reason rs ON rs.inventory_reason_id = h.inventory_reason_id
		LEFT JOIN employee e ON e.employee_id = h.history_by
		WHERE h.inventory_slab_id = $1
		ORDER BY h.history_at DESC, h.inventory_unit_history_id DESC
		LIMIT 200`, u.id)
	if err != nil {
		return nil, fmt.Errorf("load unit history: %w", err)
	}
	defer rows.Close()

	out := []UnitHistoryEntry{}
	for rows.Next() {
		var e UnitHistoryEntry
		if err := rows.Scan(&e.Action, &e.Field, &e.OldValue, &e.NewValue,
			&e.FromBin, &e.ToBin, &e.Reason, &e.Note, &e.At, &e.ByName); err != nil {
			return nil, fmt.Errorf("scan unit history: %w", err)
		}
		out = append(out, e)
	}
	return out, rows.Err()
}
