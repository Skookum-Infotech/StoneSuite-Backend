package inventory

// unit_photo.go — the picture of a physical slab (inventory_slab.slab_photo_key).
//
// Every natural slab looks different, so the photo belongs to the unit, not the
// catalogue item. It is used on the drawing board to lay pieces over the real
// stone, and later as the texture of the 3D view. The bytes live in the tenant's
// R2 bucket; only the object key is stored here.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// UnitPhotoKey returns the stored photo key of a unit ("" when it has none).
func UnitPhotoKey(ctx context.Context, q pgxQuerier, unitUUID string) (string, error) {
	var key string
	err := q.QueryRow(ctx, `
		SELECT slab_photo_key FROM inventory_slab
		WHERE inventory_slab_uuid = $1 AND slab_deleted_at IS NULL`, unitUUID).Scan(&key)
	if errors.Is(err, pgx.ErrNoRows) || isInvalidTextRepresentation(err) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("read unit photo key: %w", err)
	}
	return key, nil
}

// SetUnitPhotoKey stores (or, with an empty key, clears) a unit's photo key.
func SetUnitPhotoKey(ctx context.Context, pool *pgxpool.Pool, unitUUID, key string, actorEmployeeID int) error {
	tag, err := pool.Exec(ctx, `
		UPDATE inventory_slab
		SET slab_photo_key = $2, slab_updated_at = NOW(), slab_updated_by = $3
		WHERE inventory_slab_uuid = $1 AND slab_deleted_at IS NULL`,
		unitUUID, key, nullableInt(actorEmployeeID))
	if isInvalidTextRepresentation(err) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("set unit photo key: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
