package inventory

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Create inserts a new inventory item and records the creation in history.
// SKU uniqueness (case-insensitive, among live rows) is enforced by
// uq_inventory_item_sku_active; barcode uniqueness by uq_inv_item_barcode_active.
func Create(ctx context.Context, pool *pgxpool.Pool, in CreateItemInput, actorEmployeeID int) (*Item, error) {
	if err := validateItemInput(&in); err != nil {
		return nil, err
	}
	custom := in.CustomFields
	if custom == nil {
		custom = map[string]any{}
	}
	if err := validateCustom(ctx, pool, custom); err != nil {
		return nil, err
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin create inventory item: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var (
		newUUID string
		newID   int
	)
	err = tx.QueryRow(ctx, `
		INSERT INTO inventory_item (
			inventory_item_sku, inventory_item_name, inventory_item_description,
			inventory_item_unit_id, inventory_item_unit_price, inventory_item_currency_id,
			inventory_item_tax_rate_id, inventory_item_custom_fields,
			inventory_item_tracking, inventory_item_material_id, inventory_item_color_id,
			inventory_item_finish_id, inventory_item_thickness_mm, inventory_item_origin_country_id,
			inventory_item_barcode, inventory_item_default_warehouse_id,
			inventory_item_created_by
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
		RETURNING inventory_item_uuid, inventory_item_id`,
		in.SKU, in.Name, in.Description, in.UnitID, in.UnitPrice,
		nullableIntPtr(in.CurrencyID), nullableIntPtr(in.TaxRateID), custom,
		in.Tracking, nullableIntPtr(in.MaterialID), nullableIntPtr(in.ColorID),
		nullableIntPtr(in.FinishID), in.ThicknessMM, nullableIntPtr(in.OriginCountryID),
		in.Barcode, nullableIntPtr(in.DefaultWarehouseID),
		nullableInt(actorEmployeeID),
	).Scan(&newUUID, &newID)
	if err != nil {
		return nil, mapItemWriteErr(err, "insert")
	}

	if err := writeItemHistory(ctx, tx, newID, "create", "", "", in.SKU, actorEmployeeID); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit create inventory item: %w", err)
	}
	return Get(ctx, pool, newUUID)
}
