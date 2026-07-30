package inventory

// lookups.go — the vocabulary registry.
//
// Before this file there was no endpoint anywhere in the app returning
// lkp_unit, lkp_warehouse or lkp_tax_rate, yet inventory_item_unit_id is
// NOT NULL — so an item form could not populate its unit dropdown and was
// literally unsubmittable. This closes that.
//
// Every table reachable here is named in lookupTables. A {kind} from the URL is
// resolved through that map and never interpolated into SQL, so an unknown kind
// is a 400 rather than an injection surface.

import (
	"context"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5/pgxpool"
)

// LookupItem is one row of any vocabulary, in a shape every dropdown can use.
type LookupItem struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Code     string `json:"code"`
	IsActive bool   `json:"isActive"`
	IsSystem bool   `json:"isSystem"`
	// Extra carries the few vocabulary-specific columns a UI needs — a colour
	// swatch, whether a material must be sealed, which direction a reason may
	// move stock. Kept out of the common fields so the shape stays uniform.
	Extra map[string]any `json:"extra,omitempty"`
}

// lookupTable describes one vocabulary well enough to read and write it
// generically. Every identifier here is developer-authored; none comes from a
// request.
type lookupTable struct {
	table     string
	idCol     string
	nameCol   string
	codeCol   string
	activeCol string
	systemCol string
	deletedAt string
	// writable is false for vocabularies the API exposes read-only.
	writable bool
	// extraCols are selected as a JSON object into LookupItem.Extra.
	extraCols map[string]string
}

// lookupTables is the whitelist. A kind absent from this map does not exist as
// far as the API is concerned.
var lookupTables = map[string]lookupTable{
	"materials": {
		table: "lkp_material", idCol: "material_id", nameCol: "material_name",
		codeCol: "material_code", activeCol: "material_is_active",
		systemCol: "material_is_system", deletedAt: "material_deleted_at",
		writable:  true,
		extraCols: map[string]string{"isPorous": "material_is_porous"},
	},
	"colors": {
		table: "lkp_color", idCol: "color_id", nameCol: "color_name",
		codeCol: "color_code", activeCol: "color_is_active",
		systemCol: "color_is_system", deletedAt: "color_deleted_at",
		writable:  true,
		extraCols: map[string]string{"hex": "color_hex", "materialId": "color_material_id"},
	},
	"finishes": {
		table: "lkp_finish", idCol: "finish_id", nameCol: "finish_name",
		codeCol: "finish_code", activeCol: "finish_is_active",
		systemCol: "finish_is_system", deletedAt: "finish_deleted_at",
		writable: true,
	},
	"reasons": {
		table: "lkp_inventory_reason", idCol: "inventory_reason_id",
		nameCol: "inventory_reason_name", codeCol: "inventory_reason_code",
		activeCol: "inventory_reason_is_active", systemCol: "inventory_reason_is_system",
		deletedAt: "inventory_reason_deleted_at",
		writable:  true,
		extraCols: map[string]string{
			"appliesTo": "inventory_reason_applies_to",
			"direction": "inventory_reason_direction",
		},
	},
	"units": {
		table: "lkp_unit", idCol: "unit_id", nameCol: "unit_name",
		codeCol: "unit_code", activeCol: "unit_is_active",
		systemCol: "unit_is_system", deletedAt: "unit_deleted_at",
		// Read-only: unit codes are wired into area.go's conversion table, so a
		// user-invented unit would have no conversion and silently break the
		// on-hand invariant for any item using it.
		writable:  false,
		extraCols: map[string]string{"category": "unit_category"},
	},
	"tax-rates": {
		table: "lkp_tax_rate", idCol: "tax_rate_id", nameCol: "tax_rate_name",
		codeCol: "tax_rate_code", activeCol: "tax_rate_is_active",
		systemCol: "tax_rate_is_system", deletedAt: "tax_rate_deleted_at",
		writable:  false,
		extraCols: map[string]string{"percent": "tax_rate_percent"},
	},
}

// LookupKind resolves a URL segment to a whitelisted vocabulary.
func LookupKind(kind string) (lookupTable, error) {
	t, ok := lookupTables[kind]
	if !ok {
		return lookupTable{}, ClientError{Msg: fmt.Sprintf("Unknown lookup %q.", kind)}
	}
	return t, nil
}

// selectSQL builds the projection for a vocabulary. All identifiers come from
// the registry above, never from a request.
func (t lookupTable) selectSQL() string {
	q := fmt.Sprintf(
		`SELECT %s, %s, %s, %s, %s`,
		t.idCol, t.nameCol, t.codeCol, t.activeCol, t.systemCol)
	if len(t.extraCols) == 0 {
		return q + ", NULL::jsonb FROM " + t.table
	}
	// Sorted so the emitted SQL is identical on every call and pgx's
	// prepared-statement cache actually hits.
	keys := make([]string, 0, len(t.extraCols))
	for k := range t.extraCols {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	obj := ""
	for _, k := range keys {
		if obj != "" {
			obj += ", "
		}
		obj += fmt.Sprintf("'%s', %s", k, t.extraCols[k])
	}
	return q + ", jsonb_build_object(" + obj + ") FROM " + t.table
}

// ListLookup returns every live row of one vocabulary, active first then by name.
func ListLookup(ctx context.Context, pool *pgxpool.Pool, kind string, includeInactive bool) ([]LookupItem, error) {
	t, err := LookupKind(kind)
	if err != nil {
		return nil, err
	}
	q := t.selectSQL() + " WHERE " + t.deletedAt + " IS NULL"
	if !includeInactive {
		q += " AND " + t.activeCol + " = TRUE"
	}
	q += " ORDER BY " + t.activeCol + " DESC, " + t.nameCol + " ASC"

	rows, err := pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", kind, err)
	}
	defer rows.Close()

	out := []LookupItem{}
	for rows.Next() {
		var (
			it    LookupItem
			extra map[string]any
		)
		if err := rows.Scan(&it.ID, &it.Name, &it.Code, &it.IsActive, &it.IsSystem, &extra); err != nil {
			return nil, fmt.Errorf("scan %s: %w", kind, err)
		}
		it.Extra = extra
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list %s: %w", kind, err)
	}
	return out, nil
}

// AllLookups returns every vocabulary in one payload — what an item, unit or
// bin form loads on open.
func AllLookups(ctx context.Context, pool *pgxpool.Pool) (map[string]any, error) {
	out := map[string]any{}
	for kind := range lookupTables {
		items, err := ListLookup(ctx, pool, kind, false)
		if err != nil {
			return nil, err
		}
		out[kind] = items
	}
	warehouses, err := ListWarehouses(ctx, pool, false)
	if err != nil {
		return nil, err
	}
	out["warehouses"] = warehouses
	// Enum vocabularies that live in CHECK constraints rather than tables. The
	// UI still needs them to build its selects, and duplicating the literals in
	// the frontend is how they drift from the constraint.
	out["binTypes"] = []string{"yard", "rack", "aframe", "aisle", "shelf", "floor", "staging"}
	out["unitKinds"] = []string{UnitKindSlab, UnitKindRemnant}
	out["trackingModes"] = []string{TrackingQuantity, TrackingSerialized}
	out["bundleStatuses"] = []string{"open", "sealed", "broken"}
	return out, nil
}
