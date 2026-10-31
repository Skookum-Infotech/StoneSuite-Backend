package inventory

// unit_tracking.go — a square unit is what makes an item a slab. The unit the
// item is priced and counted in decides how it is tracked, so the two cannot
// disagree: an item in sq ft or sq m is received and held slab by slab, and an
// item in any other unit is a count.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// trackingForUnit is the tracking mode a unit of measure implies.
func trackingForUnit(unitCategory string) string {
	if unitCategory == UnitCategoryArea {
		return TrackingSerialized
	}
	return TrackingQuantity
}

// applyUnitTracking sets the item's tracking from its unit, whatever tracking
// the caller sent, and keeps stock tracking on for a slab item (as
// validateItemInput does). An unknown unit is left for the foreign key to refuse.
func applyUnitTracking(ctx context.Context, q pgxQuerier, in *CreateItemInput) error {
	var category string
	err := q.QueryRow(ctx, `SELECT unit_category FROM lkp_unit WHERE unit_id = $1`, in.UnitID).Scan(&category)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("read unit category: %w", err)
	}
	in.Tracking = trackingForUnit(category)
	if in.Tracking == TrackingSerialized {
		tracked := true
		in.TrackStock = &tracked
	}
	return nil
}

// refuseUnitChangeUnderSlabs stops an item that has slabs on record from
// leaving slab tracking: its slabs are measured in the old unit, and a count
// item has nowhere to keep them.
func refuseUnitChangeUnderSlabs(ctx context.Context, q pgxQuerier, itemID int, before, after string) error {
	if before != TrackingSerialized || after == TrackingSerialized {
		return nil
	}
	var has bool
	if err := q.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM inventory_slab WHERE inventory_item_id = $1)`, itemID).Scan(&has); err != nil {
		return fmt.Errorf("check item slabs: %w", err)
	}
	if has {
		return ClientError{Msg: "This item has slabs on record, so its unit must stay a square unit (sq ft or sq m)."}
	}
	return nil
}
