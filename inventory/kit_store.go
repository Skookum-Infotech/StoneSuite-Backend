package inventory

// kit_store.go — reading kits: the shared projection, a single kit with its
// members, and the ids the write paths resolve. Writes live in
// kit_store_write.go, the list path in kit_store_search.go.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// kitSelect is the canonical kit projection, with the live member count as a
// correlated subquery so a list needs no second round trip. It selects from
// inventory_kit unqualified (like itemSelect) so the query engine's resolver can
// use bare column names.
const kitSelect = `
	SELECT inventory_kit_uuid::text, inventory_kit_name, inventory_kit_description, inventory_kit_is_active,
	       (SELECT COUNT(*) FROM inventory_kit_item ki
	          JOIN inventory_item i ON i.inventory_item_id = ki.inventory_item_id
	         WHERE ki.inventory_kit_id = inventory_kit.inventory_kit_id
	           AND i.inventory_item_deleted_at IS NULL),
	       inventory_kit_created_at, inventory_kit_updated_at
	FROM inventory_kit`

func scanKit(row pgx.Row) (*Kit, error) {
	var k Kit
	if err := row.Scan(
		&k.ID, &k.Name, &k.Description, &k.IsActive, &k.ItemCount, &k.CreatedAt, &k.UpdatedAt,
	); err != nil {
		return nil, err
	}
	return &k, nil
}

// GetKit loads one live kit with its members, in the order the kit lists them.
// A member whose item has since been deleted is left out, and counted out.
func GetKit(ctx context.Context, pool *pgxpool.Pool, uuid string) (*Kit, error) {
	k, err := scanKit(pool.QueryRow(ctx, kitSelect+`
		WHERE inventory_kit_uuid = $1 AND inventory_kit_deleted_at IS NULL`, uuid))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		// A malformed uuid reaches the driver as a type error, not ErrNoRows;
		// like items it is a 404, never a 500.
		if isInvalidTextRepresentation(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get inventory kit: %w", err)
	}

	members, err := loadKitMembers(ctx, pool, uuid)
	if err != nil {
		return nil, err
	}
	k.Members = members
	return k, nil
}

// loadKitMembers returns a kit's live members. It runs in two steps — the
// membership order and quantities, then the items themselves through itemSelect —
// so a member is scanned by exactly the code that scans every other item and the
// two cannot drift apart.
func loadKitMembers(ctx context.Context, pool *pgxpool.Pool, kitUUID string) ([]KitMember, error) {
	rows, err := pool.Query(ctx, `
		SELECT i.inventory_item_uuid::text, ki.kit_item_quantity
		FROM inventory_kit_item ki
		JOIN inventory_kit k ON k.inventory_kit_id = ki.inventory_kit_id
		JOIN inventory_item i ON i.inventory_item_id = ki.inventory_item_id
		WHERE k.inventory_kit_uuid = $1 AND i.inventory_item_deleted_at IS NULL
		ORDER BY ki.kit_item_position, ki.inventory_kit_item_id`, kitUUID)
	if err != nil {
		return nil, fmt.Errorf("load kit members: %w", err)
	}
	defer rows.Close()

	type slot struct {
		uuid string
		qty  float64
	}
	var slots []slot
	for rows.Next() {
		var s slot
		if err := rows.Scan(&s.uuid, &s.qty); err != nil {
			return nil, fmt.Errorf("scan kit member: %w", err)
		}
		slots = append(slots, s)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load kit members: %w", err)
	}
	if len(slots) == 0 {
		return []KitMember{}, nil
	}

	uuids := make([]string, len(slots))
	for i, s := range slots {
		uuids[i] = s.uuid
	}
	itemRows, err := pool.Query(ctx, itemSelect+`
		WHERE inventory_item_uuid::text = ANY($1::text[]) AND inventory_item_deleted_at IS NULL`, uuids)
	if err != nil {
		return nil, fmt.Errorf("load kit member items: %w", err)
	}
	defer itemRows.Close()
	byUUID := make(map[string]Item, len(slots))
	for itemRows.Next() {
		it, err := scanItem(itemRows)
		if err != nil {
			return nil, fmt.Errorf("scan kit member item: %w", err)
		}
		byUUID[it.ID] = *it
	}
	if err := itemRows.Err(); err != nil {
		return nil, fmt.Errorf("load kit member items: %w", err)
	}

	out := make([]KitMember, 0, len(slots))
	for _, s := range slots {
		if it, ok := byUUID[s.uuid]; ok {
			out = append(out, KitMember{Quantity: s.qty, Item: it})
		}
	}
	return out, nil
}
