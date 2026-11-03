package inventory

// stock.go — the read model over inventory_stock: how much of an item is held at
// each location, how much of that open sales orders still hold, and the
// reorder point that decides when it is "low".
//
// Stock is never written here. inventory_stock moves only through the ledger
// writers in ledger.go; the one thing this file writes is the reorder point,
// which is a setting on the row, not a movement of goods.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// openAllocationsSQL is the one definition of "held by open sales orders": per
// (item, location), what is reserved less what has been fulfilled. The Inventory
// alerts widget and the Sales Order Inventory tab read it the same way, so the
// figure cannot differ between screens.
const openAllocationsSQL = `
	SELECT inventory_item_id, warehouse_id, SUM(allocated_quantity - fulfilled_quantity) AS allocated
	FROM inventory_allocation
	WHERE allocation_status IN ('reserved','partially_fulfilled')
	GROUP BY inventory_item_id, warehouse_id`

// StockRow is one item's stock at one location. All quantities are in the
// item's own unit.
type StockRow struct {
	ItemID        string  `json:"itemId"`
	SKU           string  `json:"sku"`
	ItemName      string  `json:"itemName"`
	Tracking      string  `json:"tracking"`
	UnitCode      string  `json:"unitCode"`
	WarehouseID   int     `json:"warehouseId"`
	WarehouseName string  `json:"warehouseName"`
	OnHand        float64 `json:"onHand"`
	Allocated     float64 `json:"allocated"`
	// Available is on hand less allocated, never negative.
	Available float64 `json:"available"`
	// ReorderPoint of 0 means none is set.
	ReorderPoint float64 `json:"reorderPoint"`
}

// StockTotal is one item's stock summed over every location it is held in.
type StockTotal struct {
	UnitCode  string  `json:"unitCode"`
	OnHand    float64 `json:"onHand"`
	Allocated float64 `json:"allocated"`
	// Available sums each location's own available figure: stock held in one
	// location cannot cover an order held against another.
	Available    float64 `json:"available"`
	Locations    int     `json:"locations"`
	BelowReorder bool    `json:"belowReorder"`
}

const stockRowSelect = `
	SELECT ii.inventory_item_uuid::text, ii.inventory_item_sku, ii.inventory_item_name, ii.inventory_item_tracking,
	       COALESCE(u.unit_code,''), w.company_location_id, w.name,
	       s.quantity_on_hand, COALESCE(alloc.allocated, 0),
	       GREATEST(s.quantity_on_hand - COALESCE(alloc.allocated, 0), 0), s.reorder_point
	FROM inventory_stock s
	JOIN inventory_item ii ON ii.inventory_item_id = s.inventory_item_id
	JOIN company_location w ON w.company_location_id = s.warehouse_id
	LEFT JOIN lkp_unit u ON u.unit_id = ii.inventory_item_unit_id
	LEFT JOIN (` + openAllocationsSQL + `) alloc
	       ON alloc.inventory_item_id = s.inventory_item_id AND alloc.warehouse_id = s.warehouse_id`

func scanStockRows(rows pgx.Rows) ([]StockRow, error) {
	defer rows.Close()
	out := []StockRow{}
	for rows.Next() {
		var r StockRow
		if err := rows.Scan(&r.ItemID, &r.SKU, &r.ItemName, &r.Tracking, &r.UnitCode, &r.WarehouseID, &r.WarehouseName,
			&r.OnHand, &r.Allocated, &r.Available, &r.ReorderPoint); err != nil {
			return nil, fmt.Errorf("scan stock row: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read stock rows: %w", err)
	}
	return out, nil
}

// itemInternalID resolves a live item's uuid to its id. The uuid is compared as
// text, so a malformed one is "not found" rather than a cast error.
func itemInternalID(ctx context.Context, pool *pgxpool.Pool, itemUUID string) (int, error) {
	var id int
	err := pool.QueryRow(ctx, `
		SELECT inventory_item_id FROM inventory_item
		WHERE inventory_item_uuid::text = $1 AND inventory_item_deleted_at IS NULL`, strings.TrimSpace(itemUUID)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("resolve item for stock: %w", err)
	}
	return id, nil
}

// StockForItem is one item's stock at each location, by location name. An item
// held nowhere yet has no rows — an empty list, not ErrNotFound; only an
// unknown, deleted or malformed uuid is not found.
func StockForItem(ctx context.Context, pool *pgxpool.Pool, itemUUID string) ([]StockRow, error) {
	itemID, err := itemInternalID(ctx, pool, itemUUID)
	if err != nil {
		return nil, err
	}
	rows, err := pool.Query(ctx, stockRowSelect+` WHERE s.inventory_item_id = $1 ORDER BY w.name, w.company_location_id`, itemID)
	if err != nil {
		return nil, fmt.Errorf("query item stock: %w", err)
	}
	return scanStockRows(rows)
}

// StockTotals sums the stock of the listed items across their locations. Every
// live item among them has an entry — an item held nowhere reads as all zeros,
// so a newly created item shows 0 instead of vanishing from the list. Unknown,
// deleted and malformed ids have no entry.
func StockTotals(ctx context.Context, pool *pgxpool.Pool, itemUUIDs []string) (map[string]StockTotal, error) {
	out := map[string]StockTotal{}
	if len(itemUUIDs) == 0 {
		return out, nil
	}
	rows, err := pool.Query(ctx, `
		SELECT ii.inventory_item_uuid::text, COALESCE(u.unit_code,''),
		       COALESCE(SUM(s.quantity_on_hand), 0),
		       COALESCE(SUM(COALESCE(alloc.allocated, 0)), 0),
		       COALESCE(SUM(GREATEST(s.quantity_on_hand - COALESCE(alloc.allocated, 0), 0)), 0),
		       COUNT(s.inventory_stock_id),
		       COALESCE(BOOL_OR(s.reorder_point > 0 AND s.quantity_on_hand <= s.reorder_point), false)
		FROM inventory_item ii
		LEFT JOIN lkp_unit u ON u.unit_id = ii.inventory_item_unit_id
		LEFT JOIN inventory_stock s ON s.inventory_item_id = ii.inventory_item_id
		LEFT JOIN (`+openAllocationsSQL+`) alloc
		       ON alloc.inventory_item_id = s.inventory_item_id AND alloc.warehouse_id = s.warehouse_id
		WHERE ii.inventory_item_uuid::text = ANY($1) AND ii.inventory_item_deleted_at IS NULL
		GROUP BY ii.inventory_item_uuid, u.unit_code`, itemUUIDs)
	if err != nil {
		return nil, fmt.Errorf("query stock totals: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var t StockTotal
		if err := rows.Scan(&id, &t.UnitCode, &t.OnHand, &t.Allocated, &t.Available, &t.Locations, &t.BelowReorder); err != nil {
			return nil, fmt.Errorf("scan stock total: %w", err)
		}
		out[id] = t
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read stock totals: %w", err)
	}
	return out, nil
}

// SetReorderPoint sets the reorder point for an item at a location, creating the
// stock row (with nothing on hand) if the item has never been held there, and
// returns the refreshed row. A negative value, or a location that doesn't exist,
// is a ClientError.
func SetReorderPoint(ctx context.Context, pool *pgxpool.Pool, itemUUID string, warehouseID int, value float64) (StockRow, error) {
	if value < 0 {
		return StockRow{}, ClientError{Msg: "A reorder point cannot be negative."}
	}
	itemID, err := itemInternalID(ctx, pool, itemUUID)
	if err != nil {
		return StockRow{}, err
	}
	var exists bool
	if err := pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM company_location WHERE company_location_id = $1)`, warehouseID).Scan(&exists); err != nil {
		return StockRow{}, fmt.Errorf("check stock location: %w", err)
	}
	if !exists {
		return StockRow{}, ClientError{Msg: "That location does not exist."}
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO inventory_stock (inventory_item_id, warehouse_id, reorder_point)
		VALUES ($1, $2, $3)
		ON CONFLICT (inventory_item_id, warehouse_id) DO UPDATE
		SET reorder_point = EXCLUDED.reorder_point,
		    stock_updated_at = NOW(),
		    stock_record_version = inventory_stock.stock_record_version + 1`, itemID, warehouseID, value); err != nil {
		return StockRow{}, fmt.Errorf("set reorder point: %w", err)
	}
	rows, err := pool.Query(ctx, stockRowSelect+` WHERE s.inventory_item_id = $1 AND s.warehouse_id = $2`, itemID, warehouseID)
	if err != nil {
		return StockRow{}, fmt.Errorf("reload stock row: %w", err)
	}
	got, err := scanStockRows(rows)
	if err != nil {
		return StockRow{}, err
	}
	if len(got) != 1 {
		return StockRow{}, fmt.Errorf("reload stock row: expected 1 row, got %d", len(got))
	}
	return got[0], nil
}
