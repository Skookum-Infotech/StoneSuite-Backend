package inventory

// catalog_ensure_write.go — the writes behind EnsureCatalog: finding or
// creating each kit and item, and filing items into kits.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"stonesuite-backend/duplicate"
)

// ensureKits resolves every planned kit to a live row, creating the missing ones.
func ensureKits(ctx context.Context, tx pgx.Tx, plan catalogPlan, req CatalogRequest) (map[string]ensured, error) {
	out := make(map[string]ensured, len(plan.kitKeys))
	if len(plan.kitKeys) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx, `
		SELECT inventory_kit_id, inventory_kit_uuid::text, inventory_kit_name,
		       `+duplicate.NormalizeSQL("inventory_kit_name")+`
		FROM inventory_kit
		WHERE inventory_kit_deleted_at IS NULL
		  AND `+duplicate.NormalizeSQL("inventory_kit_name")+` = ANY($1::text[])
		ORDER BY inventory_kit_is_active DESC, inventory_kit_id`, plan.kitKeys)
	if err != nil {
		return nil, fmt.Errorf("find kits: %w", err)
	}
	for rows.Next() {
		var e ensured
		var key string
		if err := rows.Scan(&e.id, &e.ref.ID, &e.ref.Name, &key); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan kit: %w", err)
		}
		if _, dup := out[key]; !dup { // active first, then the oldest
			out[key] = e
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("find kits: %w", err)
	}

	for _, key := range plan.kitKeys {
		if _, ok := out[key]; ok {
			continue
		}
		e, err := createKitByName(ctx, tx, plan.kitNames[key], key, req)
		if err != nil {
			return nil, err
		}
		out[key] = e
	}
	return out, nil
}

// createKitByName inserts an empty kit. A kit someone else created in the
// meantime (the partial unique index fires) is found and reused instead.
func createKitByName(ctx context.Context, tx pgx.Tx, name, key string, req CatalogRequest) (ensured, error) {
	e := ensured{ref: CatalogRef{Name: name, Created: true}}
	err := tx.QueryRow(ctx, `
		INSERT INTO inventory_kit (inventory_kit_name, inventory_kit_description, inventory_kit_created_by)
		VALUES ($1, $2, $3)
		ON CONFLICT (LOWER(inventory_kit_name)) WHERE inventory_kit_deleted_at IS NULL DO NOTHING
		RETURNING inventory_kit_id, inventory_kit_uuid::text`,
		name, req.KitDescription, nullableInt(req.ActorEmployeeID)).Scan(&e.id, &e.ref.ID)
	if err == nil {
		return e, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return ensured{}, mapKitWriteErr(err, "insert")
	}
	e.ref.Created = false
	err = tx.QueryRow(ctx, `
		SELECT inventory_kit_id, inventory_kit_uuid::text, inventory_kit_name
		FROM inventory_kit
		WHERE inventory_kit_deleted_at IS NULL
		  AND `+duplicate.NormalizeSQL("inventory_kit_name")+` = $1
		ORDER BY inventory_kit_is_active DESC, inventory_kit_id
		LIMIT 1`, key).Scan(&e.id, &e.ref.ID, &e.ref.Name)
	if err != nil {
		return ensured{}, fmt.Errorf("find kit after conflict: %w", err)
	}
	return e, nil
}

// ensureItems resolves every planned item to a live row, creating the missing
// ones as placeholders for the tenant to complete.
func ensureItems(ctx context.Context, tx pgx.Tx, plan catalogPlan, req CatalogRequest) (map[string]ensured, error) {
	out := make(map[string]ensured, len(plan.itemKeys))
	if len(plan.itemKeys) == 0 {
		return out, nil
	}
	rows, err := tx.Query(ctx, `
		SELECT inventory_item_id, inventory_item_uuid::text, inventory_item_name,
		       `+duplicate.NormalizeSQL("inventory_item_name")+`
		FROM inventory_item
		WHERE inventory_item_deleted_at IS NULL
		  AND `+duplicate.NormalizeSQL("inventory_item_name")+` = ANY($1::text[])
		ORDER BY inventory_item_is_active DESC, inventory_item_id`, plan.itemKeys)
	if err != nil {
		return nil, fmt.Errorf("find items: %w", err)
	}
	for rows.Next() {
		var e ensured
		var key string
		if err := rows.Scan(&e.id, &e.ref.ID, &e.ref.Name, &key); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan item: %w", err)
		}
		if _, dup := out[key]; !dup {
			out[key] = e
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("find items: %w", err)
	}

	var unitID int
	for _, key := range plan.itemKeys {
		if _, ok := out[key]; ok {
			continue
		}
		if unitID == 0 {
			if err := tx.QueryRow(ctx, `SELECT unit_id FROM lkp_unit WHERE unit_code = $1`, ensureUnitCode).Scan(&unitID); err != nil {
				return nil, fmt.Errorf("find default unit %q: %w", ensureUnitCode, err)
			}
		}
		e, err := createPlaceholderItem(ctx, tx, plan.itemNames[key], unitID, req)
		if err != nil {
			return nil, err
		}
		out[key] = e
	}
	return out, nil
}

// createPlaceholderItem inserts a $0, unit-"Each", stock-unchecked item. The id
// is taken from the sequence first so the SKU can carry it in the one insert; a
// SKU is the prefix and the id, padded to six digits.
func createPlaceholderItem(ctx context.Context, tx pgx.Tx, name string, unitID int, req CatalogRequest) (ensured, error) {
	e := ensured{ref: CatalogRef{Name: name, Created: true}}
	var sku string
	err := tx.QueryRow(ctx, `
		WITH n AS (SELECT nextval(pg_get_serial_sequence('inventory_item', 'inventory_item_id')) AS id)
		INSERT INTO inventory_item (
			inventory_item_id, inventory_item_sku, inventory_item_name, inventory_item_description,
			inventory_item_unit_id, inventory_item_unit_price, inventory_item_purchase_unit_price,
			inventory_item_tracking, inventory_item_track_stock, inventory_item_created_by
		)
		SELECT n.id,
		       $1::text || CASE WHEN n.id < 1000000 THEN lpad(n.id::text, 6, '0') ELSE n.id::text END,
		       $2, $3, $4, 0, 0, 'quantity', FALSE, $5
		FROM n
		RETURNING inventory_item_id, inventory_item_uuid::text, inventory_item_sku`,
		req.SKUPrefix, name, req.ItemDescription, unitID, nullableInt(req.ActorEmployeeID),
	).Scan(&e.id, &e.ref.ID, &sku)
	if err != nil {
		return ensured{}, mapItemWriteErr(err, "insert")
	}
	if err := writeItemHistory(ctx, tx, e.id, "create", "", "", sku, req.ActorEmployeeID); err != nil {
		return ensured{}, err
	}
	return e, nil
}

// fileItemsInKits adds each item to the kits that list it, at quantity 1 and
// last in the kit's order, and records the change on the item's history the same
// way the item form's Kits field does (setItemKits).
func fileItemsInKits(ctx context.Context, tx pgx.Tx, plan catalogPlan, kits, items map[string]ensured, actorEmployeeID int) error {
	if len(plan.kitKeys) == 0 || len(plan.itemKeys) == 0 {
		return nil
	}
	kitIDs := make([]int, 0, len(kits))
	for _, k := range kits {
		kitIDs = append(kitIDs, k.id)
	}
	counts, err := kitMemberCounts(ctx, tx, kitIDs)
	if err != nil {
		return err
	}

	wantedBy := map[string][]string{} // item key -> kit keys
	for _, kitKey := range plan.kitKeys {
		for _, itemKey := range plan.members[kitKey] {
			wantedBy[itemKey] = append(wantedBy[itemKey], kitKey)
		}
	}

	for _, itemKey := range plan.itemKeys {
		item := items[itemKey]
		have := map[string]bool{}
		var before []KitRef
		if !item.ref.Created {
			if before, err = itemKitsByID(ctx, tx, item.id); err != nil {
				return err
			}
			for _, k := range before {
				have[k.ID] = true
			}
		}
		added := false
		for _, kitKey := range wantedBy[itemKey] {
			kit := kits[kitKey]
			if have[kit.ref.ID] || counts[kit.id] >= MaxKitMembers {
				continue
			}
			tag, err := tx.Exec(ctx, `
				INSERT INTO inventory_kit_item (inventory_kit_id, inventory_item_id, kit_item_quantity, kit_item_position)
				VALUES ($1, $2, 1, COALESCE(
					(SELECT MAX(kit_item_position) + 1 FROM inventory_kit_item WHERE inventory_kit_id = $1), 0))
				ON CONFLICT (inventory_kit_id, inventory_item_id) DO NOTHING`, kit.id, item.id)
			if err != nil {
				return fmt.Errorf("add item to kit: %w", err)
			}
			if tag.RowsAffected() == 1 {
				counts[kit.id]++
				have[kit.ref.ID] = true
				added = true
			}
		}
		if !added {
			continue
		}
		after, err := itemKitsByID(ctx, tx, item.id)
		if err != nil {
			return err
		}
		if err := writeItemHistory(ctx, tx, item.id, "update", "kits",
			joinKitNames(before), joinKitNames(after), actorEmployeeID); err != nil {
			return err
		}
	}
	return nil
}

// kitMemberCounts is how many items each kit holds now.
func kitMemberCounts(ctx context.Context, tx pgx.Tx, kitIDs []int) (map[int]int, error) {
	out := make(map[int]int, len(kitIDs))
	rows, err := tx.Query(ctx, `
		SELECT inventory_kit_id, COUNT(*) FROM inventory_kit_item
		WHERE inventory_kit_id = ANY($1::int[]) GROUP BY inventory_kit_id`, kitIDs)
	if err != nil {
		return nil, fmt.Errorf("count kit members: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id, n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, fmt.Errorf("scan kit member count: %w", err)
		}
		out[id] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("count kit members: %w", err)
	}
	return out, nil
}
