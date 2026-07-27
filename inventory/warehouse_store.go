package inventory

// warehouse_store.go — CRUD for lkp_warehouse.
//
// The table has existed since migration 027 with its own uuid, but no package,
// controller or route ever served it. Warehouses are addressed by
// warehouse_uuid, never by the SERIAL.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Warehouse is a physical site holding stock.
type Warehouse struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Code      string `json:"code"`
	AddrLine1 string `json:"addrLine1"`
	AddrLine2 string `json:"addrLine2"`
	AddrCity  string `json:"addrCity"`
	AddrState *int   `json:"addrStateId,omitempty"`
	AddrZip   string `json:"addrZip"`
	IsDefault bool   `json:"isDefault"`
	IsActive  bool   `json:"isActive"`
	IsSystem  bool   `json:"isSystem"`
}

// WarehouseInput is the write shape.
type WarehouseInput struct {
	Name      string `json:"name"`
	Code      string `json:"code"`
	AddrLine1 string `json:"addrLine1"`
	AddrLine2 string `json:"addrLine2"`
	AddrCity  string `json:"addrCity"`
	AddrState *int   `json:"addrStateId,omitempty"`
	AddrZip   string `json:"addrZip"`
	IsActive  bool   `json:"isActive"`
}

const warehouseSelect = `
	SELECT warehouse_uuid, warehouse_name, warehouse_code,
	       warehouse_addr_line1, warehouse_addr_line2, warehouse_addr_city,
	       warehouse_addr_state, warehouse_addr_zip,
	       warehouse_is_default, warehouse_is_active, warehouse_is_system
	FROM lkp_warehouse`

func scanWarehouse(row pgx.Row) (*Warehouse, error) {
	var w Warehouse
	if err := row.Scan(&w.ID, &w.Name, &w.Code, &w.AddrLine1, &w.AddrLine2,
		&w.AddrCity, &w.AddrState, &w.AddrZip, &w.IsDefault, &w.IsActive, &w.IsSystem); err != nil {
		return nil, err
	}
	return &w, nil
}

// ListWarehouses returns live warehouses, default first.
func ListWarehouses(ctx context.Context, pool *pgxpool.Pool, includeInactive bool) ([]Warehouse, error) {
	q := warehouseSelect + " WHERE warehouse_deleted_at IS NULL"
	if !includeInactive {
		q += " AND warehouse_is_active = TRUE"
	}
	q += " ORDER BY warehouse_is_default DESC, warehouse_name ASC"

	rows, err := pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("list warehouses: %w", err)
	}
	defer rows.Close()
	out := []Warehouse{}
	for rows.Next() {
		w, err := scanWarehouse(rows)
		if err != nil {
			return nil, fmt.Errorf("scan warehouse: %w", err)
		}
		out = append(out, *w)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list warehouses: %w", err)
	}
	return out, nil
}

// GetWarehouse loads one live warehouse by uuid.
func GetWarehouse(ctx context.Context, pool *pgxpool.Pool, uuid string) (*Warehouse, error) {
	w, err := scanWarehouse(pool.QueryRow(ctx, warehouseSelect+`
		WHERE warehouse_uuid = $1 AND warehouse_deleted_at IS NULL`, uuid))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		if isInvalidTextRepresentation(err) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("get warehouse: %w", err)
	}
	return w, nil
}

func validateWarehouse(in *WarehouseInput) error {
	if strings.TrimSpace(in.Name) == "" {
		return ClientError{Msg: "Warehouse name is required."}
	}
	if strings.TrimSpace(in.Code) == "" {
		return ClientError{Msg: "Warehouse code is required."}
	}
	if len(in.Code) > 20 {
		return ClientError{Msg: "Warehouse code cannot be longer than 20 characters."}
	}
	return nil
}

// CreateWarehouse inserts a warehouse. It is never created as the default —
// SetDefaultWarehouse is the only way to move the default, so the
// uq_warehouse_default partial index can only ever see one transition at a time.
func CreateWarehouse(ctx context.Context, pool *pgxpool.Pool, in WarehouseInput, actorEmployeeID int) (*Warehouse, error) {
	if err := validateWarehouse(&in); err != nil {
		return nil, err
	}
	var newUUID string
	err := pool.QueryRow(ctx, `
		INSERT INTO lkp_warehouse (
			warehouse_name, warehouse_code, warehouse_addr_line1, warehouse_addr_line2,
			warehouse_addr_city, warehouse_addr_state, warehouse_addr_zip,
			warehouse_is_active, warehouse_created_by
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
		RETURNING warehouse_uuid`,
		in.Name, in.Code, in.AddrLine1, in.AddrLine2, in.AddrCity,
		nullableIntPtr(in.AddrState), in.AddrZip, in.IsActive, nullableInt(actorEmployeeID),
	).Scan(&newUUID)
	if err != nil {
		if isUniqueViolation(err) {
			return nil, ClientError{Msg: "A warehouse with this code already exists."}
		}
		if isFKViolation(err) {
			return nil, ClientError{Msg: "Unknown state or country."}
		}
		return nil, fmt.Errorf("insert warehouse: %w", err)
	}
	return GetWarehouse(ctx, pool, newUUID)
}

// UpdateWarehouse overwrites the editable fields.
func UpdateWarehouse(ctx context.Context, pool *pgxpool.Pool, uuid string, in WarehouseInput, actorEmployeeID int) error {
	if err := validateWarehouse(&in); err != nil {
		return err
	}
	tag, err := pool.Exec(ctx, `
		UPDATE lkp_warehouse SET
			warehouse_name = $2, warehouse_code = $3,
			warehouse_addr_line1 = $4, warehouse_addr_line2 = $5, warehouse_addr_city = $6,
			warehouse_addr_state = $7, warehouse_addr_zip = $8, warehouse_is_active = $9,
			warehouse_record_version = warehouse_record_version + 1
		WHERE warehouse_uuid = $1 AND warehouse_deleted_at IS NULL`,
		uuid, in.Name, in.Code, in.AddrLine1, in.AddrLine2, in.AddrCity,
		nullableIntPtr(in.AddrState), in.AddrZip, in.IsActive)
	if err != nil {
		if isUniqueViolation(err) {
			return ClientError{Msg: "A warehouse with this code already exists."}
		}
		return fmt.Errorf("update warehouse: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetDefaultWarehouse moves the default flag.
//
// Clearing the old default and setting the new one MUST happen in one
// transaction: uq_warehouse_default is a partial unique index over
// warehouse_is_default = TRUE, so two rows briefly holding it — which is what
// two separate statements would produce — is rejected outright.
func SetDefaultWarehouse(ctx context.Context, pool *pgxpool.Pool, uuid string) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin set default warehouse: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var id int
	err = tx.QueryRow(ctx, `
		SELECT warehouse_id FROM lkp_warehouse
		WHERE warehouse_uuid = $1 AND warehouse_deleted_at IS NULL
		  AND warehouse_is_active = TRUE`, uuid).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		if isInvalidTextRepresentation(err) {
			return ErrNotFound
		}
		return fmt.Errorf("resolve warehouse: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE lkp_warehouse SET warehouse_is_default = FALSE
		WHERE warehouse_is_default = TRUE AND warehouse_id <> $1`, id); err != nil {
		return fmt.Errorf("clear previous default warehouse: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE lkp_warehouse SET warehouse_is_default = TRUE WHERE warehouse_id = $1`, id); err != nil {
		return fmt.Errorf("set default warehouse: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit set default warehouse: %w", err)
	}
	return nil
}

// DeleteWarehouse soft-deletes a warehouse, refusing while it still holds
// stock, units or bins — the same reasoning as item delete: orphaned stock
// pointing at an invisible warehouse is worse than a refused delete.
func DeleteWarehouse(ctx context.Context, pool *pgxpool.Pool, uuid string, actorEmployeeID int) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin delete warehouse: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var (
		id        int
		isDefault bool
		isSystem  bool
	)
	err = tx.QueryRow(ctx, `
		SELECT warehouse_id, warehouse_is_default, warehouse_is_system FROM lkp_warehouse
		WHERE warehouse_uuid = $1 AND warehouse_deleted_at IS NULL`, uuid).Scan(&id, &isDefault, &isSystem)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		if isInvalidTextRepresentation(err) {
			return ErrNotFound
		}
		return fmt.Errorf("resolve warehouse: %w", err)
	}
	if isSystem {
		return ClientError{Msg: "The system warehouse cannot be deleted."}
	}
	if isDefault {
		return ClientError{Msg: "Make another warehouse the default before deleting this one."}
	}

	var onHand float64
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(quantity_on_hand),0) FROM inventory_stock WHERE warehouse_id = $1`, id).Scan(&onHand); err != nil {
		return fmt.Errorf("check warehouse stock: %w", err)
	}
	if onHand != 0 {
		return ClientError{Msg: "This warehouse still holds stock. Transfer or adjust it to zero first."}
	}

	if _, err := tx.Exec(ctx, `
		UPDATE lkp_warehouse SET warehouse_deleted_at = NOW(), warehouse_deleted_by = $2
		WHERE warehouse_id = $1`, id, nullableInt(actorEmployeeID)); err != nil {
		return fmt.Errorf("delete warehouse: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit delete warehouse: %w", err)
	}
	return nil
}
