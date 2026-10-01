package inventory

// warehouse_store.go — read access to the places stock is held.
//
// Inventory has no warehouse master of its own any more. A "warehouse" is a
// company_location (Configuration -> Company Info -> Locations), and that screen
// is the only place locations are created, edited, made the default or deleted
// (package companylocation). Every warehouse_id / *_warehouse_id column in the
// inventory tables is a foreign key to company_location(company_location_id).
//
// The names stay as they were on purpose -- Warehouse, warehouse_id, the
// /inventory/warehouses route -- so the write contracts of units, bundles,
// adjustments, transfers and counts are untouched; only what a user reads is
// called a Location.
//
// Every mutation route still addresses a location by its uuid, never by the
// SERIAL -- but every document write contract elsewhere takes the SERIAL as a
// foreign key, and needs a way to learn it. Warehouse.WarehouseID below is that:
// the only place a client can resolve uuid -> SERIAL.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/companylocation"
)

// Warehouse is one company location, as inventory sees it: the location's own
// fields (id = its uuid, name, phone, address, isDefault) plus the numeric id
// the document write contracts take.
type Warehouse struct {
	companylocation.Location
	WarehouseID int `json:"warehouseId"`
}

const warehouseSelect = `
	SELECT company_location_id, company_location_uuid, name, phone,
	       addr_line1, addr_line2, addr_suite, addr_city, addr_country, addr_state, addr_zip,
	       is_default
	FROM company_location`

func scanWarehouse(row pgx.Row) (*Warehouse, error) {
	var w Warehouse
	if err := row.Scan(&w.WarehouseID, &w.ID, &w.Name, &w.Phone,
		&w.Address.Line1, &w.Address.Line2, &w.Address.Suite, &w.Address.City, &w.Address.Country, &w.Address.State, &w.Address.Zip,
		&w.IsDefault); err != nil {
		return nil, err
	}
	return &w, nil
}

// ListWarehouses returns the tenant's live locations, default first.
func ListWarehouses(ctx context.Context, pool *pgxpool.Pool) ([]Warehouse, error) {
	rows, err := pool.Query(ctx, warehouseSelect+`
		WHERE deleted_at IS NULL
		ORDER BY is_default DESC, name ASC`)
	if err != nil {
		return nil, fmt.Errorf("list locations: %w", err)
	}
	defer rows.Close()
	out := []Warehouse{}
	for rows.Next() {
		w, err := scanWarehouse(rows)
		if err != nil {
			return nil, fmt.Errorf("scan location: %w", err)
		}
		out = append(out, *w)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list locations: %w", err)
	}
	return out, nil
}

// GetWarehouse loads one live location by uuid.
func GetWarehouse(ctx context.Context, pool *pgxpool.Pool, uuid string) (*Warehouse, error) {
	w, err := scanWarehouse(pool.QueryRow(ctx, warehouseSelect+`
		WHERE company_location_uuid = $1 AND deleted_at IS NULL`, uuid))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		if isInvalidTextRepresentation(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get location: %w", err)
	}
	return w, nil
}
