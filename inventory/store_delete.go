package inventory

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// SoftDelete marks an item deleted; it is excluded from Get/Search thereafter
// but existing sales_order_item snapshots (which do not cascade) still
// reference it for historical display.
//
// Deletion is refused while the item still holds stock or physical units. The
// alternative — allowing it — leaves inventory_stock rows and inventory_slab
// rows pointing at a catalogue entry the UI no longer shows, so the yard holds
// slabs of an item nobody can look up.
func SoftDelete(ctx context.Context, pool *pgxpool.Pool, uuid string, actorEmployeeID int) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin delete inventory item: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	id, err := itemIDByUUID(ctx, tx, uuid)
	if err != nil {
		return err
	}

	var onHand float64
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(quantity_on_hand), 0) FROM inventory_stock
		WHERE inventory_item_id = $1`, id).Scan(&onHand); err != nil {
		return fmt.Errorf("check item stock before delete: %w", err)
	}
	if onHand != 0 {
		return ClientError{Msg: "This item still has stock on hand. Adjust it to zero before deleting."}
	}

	var liveUnits int
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*) FROM inventory_slab
		WHERE inventory_item_id = $1 AND slab_deleted_at IS NULL
		  AND slab_status <> 'consumed' AND slab_status <> 'scrapped'`, id).Scan(&liveUnits); err != nil {
		return fmt.Errorf("check item units before delete: %w", err)
	}
	if liveUnits > 0 {
		return ClientError{Msg: "This item still has physical units in inventory. Scrap or consume them before deleting."}
	}

	tag, err := tx.Exec(ctx, `
		UPDATE inventory_item
		SET inventory_item_deleted_at = NOW(), inventory_item_deleted_by = $2
		WHERE inventory_item_id = $1 AND inventory_item_deleted_at IS NULL`,
		id, actorOrSystem(actorEmployeeID))
	if err != nil {
		return fmt.Errorf("delete inventory item: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	if err := writeItemHistory(ctx, tx, id, "delete", "", "", "", actorEmployeeID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit delete inventory item: %w", err)
	}
	return nil
}
