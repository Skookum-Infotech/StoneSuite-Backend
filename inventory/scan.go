package inventory

// scan.go — row scanners and parameter helpers shared across the store files.

import (
	"github.com/jackc/pgx/v5"
)

// itemSelect is the canonical item projection. Every read path uses it so the
// column order and scanItem cannot drift apart.
const itemSelect = `
	SELECT inventory_item_uuid, inventory_item_sku, inventory_item_name, inventory_item_description,
	       inventory_item_unit_id, inventory_item_unit_price, inventory_item_currency_id, inventory_item_tax_rate_id,
	       inventory_item_is_active, inventory_item_custom_fields,
	       inventory_item_tracking, inventory_item_material_id, inventory_item_color_id,
	       inventory_item_finish_id, inventory_item_thickness_mm, inventory_item_origin_country_id,
	       inventory_item_barcode, inventory_item_default_warehouse_id,
	       inventory_item_created_at, inventory_item_updated_at
	FROM inventory_item`

func scanItem(row pgx.Row) (*Item, error) {
	var (
		it     Item
		custom map[string]any
	)
	if err := row.Scan(
		&it.ID, &it.SKU, &it.Name, &it.Description,
		&it.UnitID, &it.UnitPrice, &it.CurrencyID, &it.TaxRateID,
		&it.IsActive, &custom,
		&it.Tracking, &it.MaterialID, &it.ColorID,
		&it.FinishID, &it.ThicknessMM, &it.OriginCountryID,
		&it.Barcode, &it.DefaultWarehouseID,
		&it.CreatedAt, &it.UpdatedAt,
	); err != nil {
		return nil, err
	}
	// custom_fields is NOT NULL DEFAULT '{}' in the schema, but a nil map here
	// would marshal to JSON null and, on the write path, encode as SQL NULL and
	// violate the NOT NULL. Normalising in one place is what keeps every PATCH
	// that omits customFields from 500ing.
	if custom == nil {
		custom = map[string]any{}
	}
	it.CustomFields = custom
	return &it, nil
}

// nullableInt converts a non-positive id to SQL NULL, matching crmstore's
// convention (employee id 0/unresolved => NULL).
//
// NOT for *_deleted_by columns — use actorOrSystem there. See below.
func nullableInt(v int) any {
	if v <= 0 {
		return nil
	}
	return v
}

// systemEmployeeID is the seeded fallback actor.
const systemEmployeeID = 1

// actorOrSystem returns the actor, or the system employee when it is unresolved.
//
// Every *_deleted_by column in this package is paired with its *_deleted_at by
// a strict CHECK, so writing NULL there is not a lost attribution — it is a
// constraint violation, and therefore a 500 on every soft delete. That matters
// more than it looks: resolveEmployeeID returns 0 for callers whose
// employee_user_id has never been populated, which is most of them, so the
// failure is the common case rather than an edge one.
//
// This is the convention chartofaccounts adopted in 5b3e10f for exactly the
// same reason; inventory was still the odd one out.
func actorOrSystem(actorEmployeeID int) int {
	if actorEmployeeID <= 0 {
		return systemEmployeeID
	}
	return actorEmployeeID
}

// nullableIntPtr passes a nil or non-positive optional id through as SQL NULL.
// Distinct from nullableInt because the caller's zero value is a real absence
// rather than an unresolved lookup.
func nullableIntPtr(v *int) any {
	if v == nil || *v <= 0 {
		return nil
	}
	return *v
}

// trimmedOrEmpty is used for the optional VARCHAR columns that are
// NOT NULL DEFAULT ” — they must never receive SQL NULL.
func trimmedOrEmpty(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
