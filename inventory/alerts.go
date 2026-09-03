package inventory

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// StockLevel is one (item, warehouse) stock row with enough raw numbers for
// the Inventory alerts dashboard widget to classify and rank -- see
// controllers/dashboard_inventory.go's classifyStockAlert. Only rows that
// show some sign of trouble are returned by StockAlertCandidates below; a
// fully healthy stock row is never fetched at all, so the widget doesn't
// have to pull the entire catalogue to find the handful that matter.
type StockLevel struct {
	ItemID        string
	ItemName      string
	WarehouseName string
	OnHand        float64
	Allocated     float64
	ReorderPoint  float64
}

// StockAlertCandidates loads every active, non-deleted item's stock row
// (across every warehouse it's stocked in) that shows a sign of trouble:
// nothing on hand, more allocated to open sales orders than is on hand, or
// on-hand at or below a configured reorder point (0 = not configured, so it
// never trips this branch on its own -- see classifyStockAlert). Allocated
// uses the same reserved/partially_fulfilled definition the Sales Order
// Inventory tab already reports from (salesorder.InventoryForOrder) -- a
// literal here, not a shared call, since a widget-only concern like this
// doesn't warrant a new exported cross-package function on that side.
//
// Inventory is tenant-global reference data with no per-row owner, matching
// Search's existing convention (see store_search.go) -- there is no RBAC
// scope parameter here; the caller's inventory_item:read grant is checked
// once by the controller, not per row.
func StockAlertCandidates(ctx context.Context, pool *pgxpool.Pool) ([]StockLevel, error) {
	q := `
		SELECT ii.inventory_item_uuid, ii.inventory_item_name, w.warehouse_name,
		       s.quantity_on_hand, COALESCE(alloc.allocated, 0), s.reorder_point
		FROM inventory_stock s
		JOIN inventory_item ii ON ii.inventory_item_id = s.inventory_item_id
		JOIN lkp_warehouse w ON w.warehouse_id = s.warehouse_id
		LEFT JOIN (
			SELECT inventory_item_id, warehouse_id, SUM(allocated_quantity) AS allocated
			FROM inventory_allocation
			WHERE allocation_status IN ('reserved','partially_fulfilled')
			GROUP BY inventory_item_id, warehouse_id
		) alloc ON alloc.inventory_item_id = s.inventory_item_id AND alloc.warehouse_id = s.warehouse_id
		WHERE ii.inventory_item_deleted_at IS NULL
		  AND ii.inventory_item_is_active
		  AND (
		    s.quantity_on_hand <= 0
		    OR COALESCE(alloc.allocated, 0) > s.quantity_on_hand
		    OR (s.reorder_point > 0 AND s.quantity_on_hand <= s.reorder_point)
		  )`

	rows, err := pool.Query(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("query stock alert candidates: %w", err)
	}
	defer rows.Close()

	out := []StockLevel{}
	for rows.Next() {
		var lv StockLevel
		if err := rows.Scan(&lv.ItemID, &lv.ItemName, &lv.WarehouseName, &lv.OnHand, &lv.Allocated, &lv.ReorderPoint); err != nil {
			return nil, fmt.Errorf("scan stock alert candidate: %w", err)
		}
		out = append(out, lv)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("query stock alert candidates: %w", err)
	}
	return out, nil
}
