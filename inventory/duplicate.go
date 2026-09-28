package inventory

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"stonesuite-backend/duplicate"
)

// checkItemNameFree returns a *duplicate.Error when another live item — active
// or inactive — already has `name`. SKU and barcode uniqueness are enforced
// separately by their unique indexes (mapItemWriteErr); this is the name rule,
// which has no index. excludeUUID skips the item being renamed ("" on create).
func checkItemNameFree(ctx context.Context, q pgxQuerier, name, excludeUUID string) error {
	if duplicate.Key(name) == "" {
		return nil
	}
	d := duplicate.Error{Entity: duplicate.EntityItem, Name: name}
	err := q.QueryRow(ctx, `
		SELECT i.inventory_item_uuid::text, i.inventory_item_name,
		       CASE WHEN i.inventory_item_is_active THEN 'Active' ELSE 'Inactive' END
		FROM inventory_item i
		WHERE i.inventory_item_deleted_at IS NULL
		  AND `+duplicate.NormalizeSQL("i.inventory_item_name")+` = `+duplicate.NormalizeSQL("$1::text")+`
		  AND ($2::text = '' OR i.inventory_item_uuid::text <> $2::text)
		ORDER BY i.inventory_item_id
		LIMIT 1`, name, excludeUUID,
	).Scan(&d.ExistingID, &d.ExistingName, &d.ExistingStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("check duplicate item name: %w", err)
	}
	return &d
}
