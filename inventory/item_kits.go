package inventory

// item_kits.go — the item's side of kit membership. A kit is edited as a whole
// from its own screen (kit_store_write.go); this is what the item form uses to
// file a single item into kits. Both write inventory_kit_item.

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
)

// pgxRowsQuerier is the narrow multi-row read interface satisfied by both
// *pgxpool.Pool and pgx.Tx.
type pgxRowsQuerier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// itemKitsByUUID lists the live kits an item belongs to, by name.
func itemKitsByUUID(ctx context.Context, q pgxRowsQuerier, itemUUID string) ([]KitRef, error) {
	return queryKitRefs(ctx, q, `
		SELECT k.inventory_kit_uuid::text, k.inventory_kit_name
		FROM inventory_kit_item ki
		JOIN inventory_kit k ON k.inventory_kit_id = ki.inventory_kit_id
		JOIN inventory_item i ON i.inventory_item_id = ki.inventory_item_id
		WHERE i.inventory_item_uuid = $1 AND k.inventory_kit_deleted_at IS NULL
		ORDER BY LOWER(k.inventory_kit_name), k.inventory_kit_id`, itemUUID)
}

// itemKitsByID is itemKitsByUUID for an item already resolved to its internal id.
func itemKitsByID(ctx context.Context, q pgxRowsQuerier, itemID int) ([]KitRef, error) {
	return queryKitRefs(ctx, q, `
		SELECT k.inventory_kit_uuid::text, k.inventory_kit_name
		FROM inventory_kit_item ki
		JOIN inventory_kit k ON k.inventory_kit_id = ki.inventory_kit_id
		WHERE ki.inventory_item_id = $1 AND k.inventory_kit_deleted_at IS NULL
		ORDER BY LOWER(k.inventory_kit_name), k.inventory_kit_id`, itemID)
}

func queryKitRefs(ctx context.Context, q pgxRowsQuerier, sql string, arg any) ([]KitRef, error) {
	rows, err := q.Query(ctx, sql, arg)
	if err != nil {
		return nil, fmt.Errorf("load item kits: %w", err)
	}
	defer rows.Close()
	out := []KitRef{}
	for rows.Next() {
		var r KitRef
		if err := rows.Scan(&r.ID, &r.Name); err != nil {
			return nil, fmt.Errorf("scan item kit: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load item kits: %w", err)
	}
	return out, nil
}

// setItemKits makes `kitUUIDs` exactly the live kits the item belongs to, inside
// the caller's transaction. A kit the item is already in keeps the quantity it
// had; a kit it joins starts at 1, last in that kit's order. It records a
// "kits" change in the item's history when the set actually changed.
func setItemKits(ctx context.Context, tx pgx.Tx, itemID int, kitUUIDs []string, actorEmployeeID int) error {
	want := make([]string, 0, len(kitUUIDs))
	seen := make(map[string]bool, len(kitUUIDs))
	for _, u := range kitUUIDs {
		u = strings.ToLower(strings.TrimSpace(u))
		if u == "" || seen[u] {
			continue
		}
		seen[u] = true
		want = append(want, u)
	}

	before, err := itemKitsByID(ctx, tx, itemID)
	if err != nil {
		return err
	}

	// Resolve the wanted kits; every one must be live.
	kitIDs := make([]int, 0, len(want))
	if len(want) > 0 {
		rows, err := tx.Query(ctx, `
			SELECT inventory_kit_id FROM inventory_kit
			WHERE inventory_kit_uuid::text = ANY($1::text[]) AND inventory_kit_deleted_at IS NULL`, want)
		if err != nil {
			return fmt.Errorf("resolve item kits: %w", err)
		}
		for rows.Next() {
			var id int
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return fmt.Errorf("scan item kit id: %w", err)
			}
			kitIDs = append(kitIDs, id)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return fmt.Errorf("resolve item kits: %w", err)
		}
		if len(kitIDs) != len(want) {
			return ClientError{Msg: "One of the selected kits no longer exists."}
		}
	}

	// Leave every kit that is not wanted. <> ALL over an empty array is true, so
	// an empty list removes the item from all of its kits.
	if _, err := tx.Exec(ctx, `
		DELETE FROM inventory_kit_item
		WHERE inventory_item_id = $1 AND inventory_kit_id <> ALL($2::int[])`, itemID, kitIDs); err != nil {
		return fmt.Errorf("leave kits: %w", err)
	}
	// Join the wanted ones; ON CONFLICT keeps a membership (and its quantity) that
	// already exists.
	for _, kitID := range kitIDs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO inventory_kit_item (inventory_kit_id, inventory_item_id, kit_item_quantity, kit_item_position)
			VALUES ($1, $2, 1, COALESCE(
				(SELECT MAX(kit_item_position) + 1 FROM inventory_kit_item WHERE inventory_kit_id = $1), 0))
			ON CONFLICT (inventory_kit_id, inventory_item_id) DO NOTHING`, kitID, itemID); err != nil {
			return fmt.Errorf("join kit: %w", err)
		}
	}

	after, err := itemKitsByID(ctx, tx, itemID)
	if err != nil {
		return err
	}
	oldNames, newNames := joinKitNames(before), joinKitNames(after)
	if oldNames == newNames {
		return nil
	}
	return writeItemHistory(ctx, tx, itemID, "update", "kits", oldNames, newNames, actorEmployeeID)
}

// joinKitNames renders a kit list as one comma-separated history value.
func joinKitNames(kits []KitRef) string {
	names := make([]string, len(kits))
	for i, k := range kits {
		names[i] = k.Name
	}
	return strings.Join(names, ", ")
}
