package inventory

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Update overwrites an item's editable fields in place (SKU included — the
// active-row unique index still guards collisions) and records a field-level
// diff in history.
func Update(ctx context.Context, pool *pgxpool.Pool, uuid string, in CreateItemInput, actorEmployeeID int) error {
	if err := validateItemInput(&in); err != nil {
		return err
	}
	custom := in.CustomFields
	if custom == nil {
		custom = map[string]any{}
	}
	if err := validateCustom(ctx, pool, custom); err != nil {
		return err
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin update inventory item: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Lock the row and read the pre-image in one statement, so the diff written
	// to history is the state this update actually replaced rather than a value
	// a concurrent writer changed in between.
	var (
		itemID int
		before Item
	)
	err = tx.QueryRow(ctx, `
		SELECT inventory_item_id, inventory_item_sku, inventory_item_name, inventory_item_unit_price,
		       inventory_item_tracking, inventory_item_thickness_mm, inventory_item_is_active
		FROM inventory_item
		WHERE inventory_item_uuid = $1 AND inventory_item_deleted_at IS NULL
		FOR UPDATE`, uuid).Scan(
		&itemID, &before.SKU, &before.Name, &before.UnitPrice,
		&before.Tracking, &before.ThicknessMM, &before.IsActive)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		if isInvalidTextRepresentation(err) {
			return ErrNotFound
		}
		return fmt.Errorf("lock inventory item: %w", err)
	}

	tag, err := tx.Exec(ctx, `
		UPDATE inventory_item SET
			inventory_item_sku = $2, inventory_item_name = $3, inventory_item_description = $4,
			inventory_item_unit_id = $5, inventory_item_unit_price = $6,
			inventory_item_currency_id = $7, inventory_item_tax_rate_id = $8,
			inventory_item_custom_fields = $9,
			inventory_item_tracking = $10, inventory_item_material_id = $11,
			inventory_item_color_id = $12, inventory_item_finish_id = $13,
			inventory_item_thickness_mm = $14, inventory_item_origin_country_id = $15,
			inventory_item_barcode = $16, inventory_item_default_warehouse_id = $17,
			inventory_item_updated_at = NOW(), inventory_item_updated_by = $18,
			inventory_item_record_version = inventory_item_record_version + 1
		WHERE inventory_item_uuid = $1 AND inventory_item_deleted_at IS NULL`,
		uuid, in.SKU, in.Name, in.Description, in.UnitID, in.UnitPrice,
		nullableIntPtr(in.CurrencyID), nullableIntPtr(in.TaxRateID), custom,
		in.Tracking, nullableIntPtr(in.MaterialID), nullableIntPtr(in.ColorID),
		nullableIntPtr(in.FinishID), in.ThicknessMM, nullableIntPtr(in.OriginCountryID),
		in.Barcode, nullableIntPtr(in.DefaultWarehouseID),
		nullableInt(actorEmployeeID))
	if err != nil {
		return mapItemWriteErr(err, "update")
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}

	for _, d := range itemDiff(before, in) {
		if err := writeItemHistory(ctx, tx, itemID, "update", d.field, d.old, d.new, actorEmployeeID); err != nil {
			return err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit update inventory item: %w", err)
	}
	return nil
}

type fieldDiff struct{ field, old, new string }

// itemDiff reports the user-meaningful field changes between the pre-image and
// the submitted input. Only the fields worth an audit line are compared —
// description and custom fields churn on every save and would bury the rest.
func itemDiff(before Item, in CreateItemInput) []fieldDiff {
	var out []fieldDiff
	add := func(field, oldV, newV string) {
		if oldV != newV {
			out = append(out, fieldDiff{field, oldV, newV})
		}
	}
	add("sku", before.SKU, in.SKU)
	add("name", before.Name, in.Name)
	add("unitPrice", strconv.FormatFloat(before.UnitPrice, 'f', 2, 64),
		strconv.FormatFloat(in.UnitPrice, 'f', 2, 64))
	add("tracking", before.Tracking, in.Tracking)
	add("thicknessMm", strconv.FormatFloat(before.ThicknessMM, 'f', 2, 64),
		strconv.FormatFloat(in.ThicknessMM, 'f', 2, 64))
	return out
}

// SetActive flips an item's availability for new transactions and records it.
func SetActive(ctx context.Context, pool *pgxpool.Pool, uuid string, active bool, actorEmployeeID int) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin set item active: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	id, err := itemIDByUUID(ctx, tx, uuid)
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		UPDATE inventory_item
		SET inventory_item_is_active = $2, inventory_item_updated_at = NOW(),
		    inventory_item_updated_by = $3,
		    inventory_item_record_version = inventory_item_record_version + 1
		WHERE inventory_item_id = $1`, id, active, nullableInt(actorEmployeeID)); err != nil {
		return fmt.Errorf("set item active: %w", err)
	}
	action := "deactivate"
	if active {
		action = "activate"
	}
	if err := writeItemHistory(ctx, tx, id, action, "isActive", strconv.FormatBool(!active), strconv.FormatBool(active), actorEmployeeID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit set item active: %w", err)
	}
	return nil
}
