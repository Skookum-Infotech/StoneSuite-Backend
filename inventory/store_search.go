package inventory

import (
	"context"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/query"
)

// Search lists live inventory items with filter/sort/global-search + keyset
// pagination via the shared query engine. Inventory is tenant-global
// reference data (like lookups), so unlike Sales Order this has no per-row
// RBAC scope to AND in — only the resource-level inventory_item:read grant
// checked by the caller.
func Search(ctx context.Context, pool *pgxpool.Pool, req query.Request) (Page, error) {
	built, err := query.Build(req, resolver{}, 1)
	if err != nil {
		return Page{}, err
	}
	where := "inventory_item_deleted_at IS NULL"
	if built.Where != "" {
		where += " AND " + built.Where
	}
	if built.Keyset != "" {
		where += " AND " + built.Keyset
	}
	q := itemSelect + " WHERE " + where +
		" ORDER BY " + built.OrderBy + " LIMIT " + strconv.Itoa(built.EffLimit+1)

	rows, err := pool.Query(ctx, q, built.Args...)
	if err != nil {
		return Page{}, fmt.Errorf("search inventory items: %w", err)
	}
	defer rows.Close()
	out := []Item{}
	for rows.Next() {
		it, err := scanItem(rows)
		if err != nil {
			return Page{}, err
		}
		out = append(out, *it)
	}
	if err := rows.Err(); err != nil {
		return Page{}, fmt.Errorf("search inventory items: %w", err)
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

// sortValue reads the effective sort field's value from an item to mint the
// next cursor. It must stay in step with resolver.SortExpr: a field that is
// sortable but missing here would mint a cursor from the wrong column and
// silently skip or repeat rows across pages.
func sortValue(it Item, field string) any {
	switch field {
	case "updated_at":
		return it.UpdatedAt
	case "sku":
		return it.SKU
	case "name":
		return it.Name
	default: // created_at (default)
		return it.CreatedAt
	}
}
