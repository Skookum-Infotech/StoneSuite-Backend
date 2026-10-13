package inventory

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"

	"stonesuite-backend/duplicate"
)

// checkKitNameFree returns a *duplicate.Error when another live kit — active or
// inactive — already has `name`, compared case-insensitively with whitespace
// collapsed so "Kitchen " and "kitchen" are the same kit. excludeUUID skips the
// kit being renamed ("" on create). uq_inventory_kit_name_active is the backstop
// for two writers racing on the same new name.
func checkKitNameFree(ctx context.Context, q pgxQuerier, name, excludeUUID string) error {
	if duplicate.Key(name) == "" {
		return nil
	}
	d := duplicate.Error{Entity: duplicate.EntityKit, Name: name}
	err := q.QueryRow(ctx, `
		SELECT k.inventory_kit_uuid::text, k.inventory_kit_name,
		       CASE WHEN k.inventory_kit_is_active THEN 'Active' ELSE 'Inactive' END
		FROM inventory_kit k
		WHERE k.inventory_kit_deleted_at IS NULL
		  AND `+duplicate.NormalizeSQL("k.inventory_kit_name")+` = `+duplicate.NormalizeSQL("$1::text")+`
		  AND ($2::text = '' OR k.inventory_kit_uuid::text <> $2::text)
		ORDER BY k.inventory_kit_id
		LIMIT 1`, name, excludeUUID,
	).Scan(&d.ExistingID, &d.ExistingName, &d.ExistingStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("check duplicate kit name: %w", err)
	}
	return &d
}
