package inventory

import (
	"context"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/query"
)

// kitResolver implements query.FieldResolver (+ SortResolver + SearchResolver)
// for inventory_kit. Like the item resolver it uses bare column names, because
// SearchKits selects from inventory_kit unqualified.
type kitResolver struct{}

var kitFields = map[string]resolved{
	"id":         {"inventory_kit_uuid::text", query.TypeString},
	"name":       {"inventory_kit_name", query.TypeString},
	"is_active":  {"inventory_kit_is_active", query.TypeBool},
	"created_at": {"inventory_kit_created_at", query.TypeDate},
	"updated_at": {"inventory_kit_updated_at", query.TypeDate},
}

func (kitResolver) Resolve(key string) (string, query.DataType, bool) {
	if f, ok := kitFields[key]; ok {
		return f.expr, f.dt, true
	}
	return "", "", false
}

// SortExpr makes the name sortable: it is NOT NULL, so keyset paging over it is
// stable. created_at / updated_at are built in.
func (kitResolver) SortExpr(key string) (string, query.DataType, bool) {
	if key == "name" {
		return "inventory_kit_name", query.TypeString, true
	}
	return "", "", false
}

// SearchPredicate powers the picker's search box: the kit's name contains the term.
func (kitResolver) SearchPredicate(ph string) string {
	return "inventory_kit_name ILIKE '%'||" + ph + "||'%'"
}

var _ query.FieldResolver = kitResolver{}
var _ query.SortResolver = kitResolver{}
var _ query.SearchResolver = kitResolver{}

// SearchKits lists live kits with filter/sort/search + keyset pagination through
// the shared query engine. Kits are tenant-global catalogue data like items, so
// there is no per-row scope to AND in — only the resource-level grant the caller
// checked. Rows carry the member count, not the members.
func SearchKits(ctx context.Context, pool *pgxpool.Pool, req query.Request) (KitPage, error) {
	built, err := query.Build(req, kitResolver{}, 1)
	if err != nil {
		return KitPage{}, err
	}
	where := "inventory_kit_deleted_at IS NULL"
	if built.Where != "" {
		where += " AND " + built.Where
	}
	if built.Keyset != "" {
		where += " AND " + built.Keyset
	}
	q := kitSelect + " WHERE " + where +
		" ORDER BY " + built.OrderBy + " LIMIT " + strconv.Itoa(built.EffLimit+1)

	rows, err := pool.Query(ctx, q, built.Args...)
	if err != nil {
		return KitPage{}, fmt.Errorf("search inventory kits: %w", err)
	}
	defer rows.Close()
	out := []Kit{}
	for rows.Next() {
		k, err := scanKit(rows)
		if err != nil {
			return KitPage{}, fmt.Errorf("scan inventory kit: %w", err)
		}
		out = append(out, *k)
	}
	if err := rows.Err(); err != nil {
		return KitPage{}, fmt.Errorf("search inventory kits: %w", err)
	}

	page := KitPage{Records: out}
	if len(out) > built.EffLimit {
		page.HasMore = true
		last := out[built.EffLimit-1]
		page.Records = out[:built.EffLimit]
		page.NextCursor = query.NextCursor(last.ID, built.Sort, kitSortValue(last, built.Sort.Field))
	}
	return page, nil
}

// kitSortValue reads the effective sort field's value from a kit to mint the next
// cursor. It must stay in step with kitResolver.SortExpr, for the same reason
// sortValue must for items.
func kitSortValue(k Kit, field string) any {
	switch field {
	case "updated_at":
		return k.UpdatedAt
	case "name":
		return k.Name
	default: // created_at (default)
		return k.CreatedAt
	}
}
