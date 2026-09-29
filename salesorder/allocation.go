package salesorder

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// InventoryRow is one line of the Inventory tab: on-hand/available/allocated
// stock for an item referenced by this order, plus the quantity this order
// itself demands (spec §9, §10 GET .../inventory).
//
// Allocated is what every open order still holds (its reservation less what has
// been fulfilled), ReservedForOrder is this order's share of that, and
// Available is what is still free to promise: OnHand - Allocated. So an order
// that holds all the stock there is shows Available 0, not a shortage.
type InventoryRow struct {
	ItemID             string  `json:"itemId"`
	SKU                string  `json:"sku"`
	OnHand             float64 `json:"onHand"`
	Available          float64 `json:"available"`
	Allocated          float64 `json:"allocated"`
	ReservedForOrder   float64 `json:"reservedForOrder"`
	SalesOrderQuantity float64 `json:"salesOrderQuantity"`
	// Tracked is false for an item whose stock is not checked or reserved (a
	// service, say); the figures above are then not meaningful.
	Tracked bool `json:"tracked"`
}

// InventoryForOrder loads the Inventory tab: for every distinct catalog item
// referenced by the order's live lines, on-hand/available/allocated stock
// (derived, never stored — spec §9) plus how much of that item this order
// itself ordered and holds. Free-text lines (no inventory_item_id) are excluded
// — they have no catalog stock to report.
//
// Stock is reserved when an order is saved (see reserveStock), not by any
// separate action, so Allocated and ReservedForOrder reflect real holds.
func InventoryForOrder(ctx context.Context, pool *pgxpool.Pool, uuid string) ([]InventoryRow, error) {
	rows, err := pool.Query(ctx, `
		WITH this_order AS (
			SELECT sales_order_id FROM sales_order WHERE sales_order_uuid = $1
		),
		order_items AS (
			SELECT soi.inventory_item_id, SUM(soi.quantity) AS so_qty
			FROM sales_order_item soi
			JOIN this_order o ON o.sales_order_id = soi.sales_order_id
			WHERE soi.item_deleted_at IS NULL AND soi.inventory_item_id IS NOT NULL
			GROUP BY soi.inventory_item_id
		),
		stock AS (
			SELECT inventory_item_id, COALESCE(SUM(quantity_on_hand),0) AS on_hand
			FROM inventory_stock GROUP BY inventory_item_id
		),
		alloc AS (
			SELECT a.inventory_item_id,
			       COALESCE(SUM(a.allocated_quantity - a.fulfilled_quantity),0) AS allocated,
			       COALESCE(SUM(a.allocated_quantity - a.fulfilled_quantity)
			                FILTER (WHERE a.sales_order_id = (SELECT sales_order_id FROM this_order)),0) AS mine
			FROM inventory_allocation a
			WHERE a.allocation_status IN ('reserved','partially_fulfilled')
			GROUP BY a.inventory_item_id
		)
		SELECT ii.inventory_item_uuid, ii.inventory_item_sku,
		       COALESCE(stock.on_hand,0), COALESCE(stock.on_hand,0) - COALESCE(alloc.allocated,0),
		       COALESCE(alloc.allocated,0), COALESCE(alloc.mine,0), order_items.so_qty,
		       (ii.inventory_item_tracking = 'serialized' OR ii.inventory_item_track_stock)
		FROM order_items
		JOIN inventory_item ii ON ii.inventory_item_id = order_items.inventory_item_id
			AND ii.inventory_item_deleted_at IS NULL
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
		if err := rows.Scan(&r.ItemID, &r.SKU, &r.OnHand, &r.Available, &r.Allocated,
			&r.ReservedForOrder, &r.SalesOrderQuantity, &r.Tracked); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
