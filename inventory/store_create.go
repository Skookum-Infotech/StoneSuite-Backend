package inventory

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/approvalchain"
	"stonesuite-backend/linedim"
)

// Create inserts a new inventory item and records the creation in history.
// SKU uniqueness (case-insensitive, among live rows) is enforced by
// uq_inventory_item_sku_active; barcode uniqueness by uq_inv_item_barcode_active.
// Name uniqueness has no index — see checkItemNameFree.
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

	newUUID, err := createInTx(ctx, tx, in, custom, actorEmployeeID)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit create inventory item: %w", err)
	}
	item, err := Get(ctx, pool, newUUID)
	if err != nil {
		return nil, err
	}
	approvalchain.NotifyActorCreated(ctx, pool, approvalchain.ActorCreated{
		EventType: createdEventType, Resource: createdResource, RecordUUID: newUUID,
		ActorEmployeeID: actorEmployeeID,
		Title:           "Inventory item created: " + in.Name,
		Body:            "SKU " + in.SKU + " was added to inventory.",
	})
	return item, nil
}

// CreateTx inserts a new inventory item inside the caller's transaction (no
// Begin, Commit, read-back or notification) and returns its external uuid.
// The caller owns commit and rollback; read the row back with GetQ on the tx.
func CreateTx(ctx context.Context, tx pgx.Tx, in CreateItemInput, actorEmployeeID int) (string, error) {
	if err := validateItemInput(&in); err != nil {
		return "", err
	}
	custom := in.CustomFields
	if custom == nil {
		custom = map[string]any{}
	}
	if err := validateCustom(ctx, tx, custom); err != nil {
		return "", err
	}
	return createInTx(ctx, tx, in, custom, actorEmployeeID)
}

// createInTx runs the name check, insert, history and kit writes on tx.
func createInTx(ctx context.Context, tx pgx.Tx, in CreateItemInput, custom map[string]any, actorEmployeeID int) (string, error) {
	if err := applyUnitTracking(ctx, tx, &in); err != nil {
		return "", err
	}
	if err := checkItemNameFree(ctx, tx, in.Name, ""); err != nil {
		return "", err
	}

	dimensionUnit := in.DimensionUnit
	if dimensionUnit == "" {
		dimensionUnit = linedim.DefaultUnit
	}
	var (
		newUUID string
		newID   int
	)
	err := tx.QueryRow(ctx, `
		INSERT INTO inventory_item (
			inventory_item_sku, inventory_item_name, inventory_item_description,
			inventory_item_unit_id, inventory_item_unit_price, inventory_item_currency_id,
			inventory_item_tax_rate_id, inventory_item_custom_fields,
			inventory_item_tracking, inventory_item_material_id, inventory_item_color_id,
			inventory_item_finish_id, inventory_item_thickness_mm, inventory_item_origin_country_id,
			inventory_item_barcode, inventory_item_default_warehouse_id, inventory_item_track_stock,
			inventory_item_created_by, inventory_item_purchase_unit_price, inventory_item_dimension_unit
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)
		RETURNING inventory_item_uuid, inventory_item_id`,
		in.SKU, in.Name, in.Description, in.UnitID, *in.UnitPrice,
		nullableIntPtr(in.CurrencyID), nullableIntPtr(in.TaxRateID), custom,
		in.Tracking, nullableIntPtr(in.MaterialID), nullableIntPtr(in.ColorID),
		nullableIntPtr(in.FinishID), in.ThicknessMM, nullableIntPtr(in.OriginCountryID),
		in.Barcode, nullableIntPtr(in.DefaultWarehouseID), in.TrackStock == nil || *in.TrackStock,
		nullableInt(actorEmployeeID), *in.PurchaseUnitPrice, dimensionUnit,
	).Scan(&newUUID, &newID)
	if err != nil {
		return "", mapItemWriteErr(err, "insert")
	}

	if err := writeItemHistory(ctx, tx, newID, "create", "", "", in.SKU, actorEmployeeID); err != nil {
		return "", err
	}
	if in.KitIDs != nil {
		if err := setItemKits(ctx, tx, newID, in.KitIDs, actorEmployeeID); err != nil {
			return "", err
		}
	}
	return newUUID, nil
}

// createdEventType and createdResource label the "item created" email and its
// in-app bell entry.
const (
	createdEventType = "inventory_item.created"
	createdResource  = "inventory_item"
)
