package salesorder

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// InventoryRow is one line of the Inventory tab: on-hand/available/allocated
// stock for an item referenced by this order, plus the quantity this order
// itself demands (spec §9, §10 GET .../inventory).
type InventoryRow struct {
	ItemID             string  `json:"itemId"`
	SKU                string  `json:"sku"`
	OnHand             float64 `json:"onHand"`
	Available          float64 `json:"available"`
	Allocated          float64 `json:"allocated"`
	SalesOrderQuantity float64 `json:"salesOrderQuantity"`
}

// Reserve reserves qty of an inventory item at a warehouse against a specific
// order line, row-locking the stock row so concurrent reservations can't
// oversell. Returns ClientError when qty exceeds currently available stock.
// Not yet wired to an HTTP action — a primitive for a future fulfillment/
// picking flow to call inside its own transaction (spec §9, Task 6.2).
func Reserve(ctx context.Context, tx pgx.Tx, itemID, warehouseID, orderID, orderItemID int, qty float64) error {
	var onHand float64
	err := tx.QueryRow(ctx, `
		SELECT quantity_on_hand FROM inventory_stock
		WHERE inventory_item_id = $1 AND warehouse_id = $2
		FOR UPDATE`, itemID, warehouseID).Scan(&onHand)
	if errors.Is(err, pgx.ErrNoRows) {
		return ClientError{Msg: "No stock record for this item at the selected warehouse."}
	}
	if err != nil {
		return fmt.Errorf("lock inventory stock: %w", err)
	}
	var allocated float64
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(allocated_quantity),0) FROM inventory_allocation
		WHERE inventory_item_id = $1 AND warehouse_id = $2
		  AND allocation_status IN ('reserved','partially_fulfilled')`, itemID, warehouseID).Scan(&allocated); err != nil {
		return fmt.Errorf("sum open allocations: %w", err)
	}
	if qty > onHand-allocated {
		return ClientError{Msg: "Requested quantity exceeds available stock."}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO inventory_allocation (
			inventory_item_id, warehouse_id, sales_order_id, sales_order_item_id, allocated_quantity
		) VALUES ($1,$2,$3,$4,$5)`, itemID, warehouseID, orderID, orderItemID, qty); err != nil {
		return fmt.Errorf("insert inventory allocation: %w", err)
	}
	return nil
}

// InventoryForOrder loads the Inventory tab: for every distinct catalog item
// referenced by the order's live lines, on-hand/available/allocated stock
// (derived, never stored — spec §9) plus how much of that item this order
// itself ordered. Free-text lines (no inventory_item_id) are excluded — they
// have no catalog stock to report.
func InventoryForOrder(ctx context.Context, pool *pgxpool.Pool, uuid string) ([]InventoryRow, error) {
	rows, err := pool.Query(ctx, `
		WITH order_items AS (
			SELECT soi.inventory_item_id, SUM(soi.quantity) AS so_qty
			FROM sales_order_item soi
			JOIN sales_order so ON so.sales_order_id = soi.sales_order_id
			WHERE so.sales_order_uuid = $1 AND soi.item_deleted_at IS NULL AND soi.inventory_item_id IS NOT NULL
			GROUP BY soi.inventory_item_id
		),
		stock AS (
			SELECT inventory_item_id, COALESCE(SUM(quantity_on_hand),0) AS on_hand
			FROM inventory_stock GROUP BY inventory_item_id
		),
		alloc AS (
			SELECT inventory_item_id, COALESCE(SUM(allocated_quantity),0) AS allocated
			FROM inventory_allocation
			WHERE allocation_status IN ('reserved','partially_fulfilled')
			GROUP BY inventory_item_id
		)
		SELECT ii.inventory_item_uuid, ii.inventory_item_sku,
		       COALESCE(stock.on_hand,0), COALESCE(stock.on_hand,0) - COALESCE(alloc.allocated,0),
		       COALESCE(alloc.allocated,0), order_items.so_qty
		FROM order_items
		JOIN inventory_item ii ON ii.inventory_item_id = order_items.inventory_item_id
		LEFT JOIN stock ON stock.inventory_item_id = order_items.inventory_item_id
		LEFT JOIN alloc ON alloc.inventory_item_id = order_items.inventory_item_id
		ORDER BY ii.inventory_item_sku`, uuid)
	if err != nil {
		return nil, fmt.Errorf("load inventory tab: %w", err)
	}
	defer rows.Close()
	out := []InventoryRow{}
	for rows.Next() {
		var r InventoryRow
		if err := rows.Scan(&r.ItemID, &r.SKU, &r.OnHand, &r.Available, &r.Allocated, &r.SalesOrderQuantity); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
