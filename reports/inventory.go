package reports

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Inventory reports. Stock is tenant-global reference data with no per-row
// owner (see inventory.StockAlertCandidates), so there is no scope filter here:
// the caller's report_inventory:read grant is the only gate. Definitions reuse
// the Inventory module's own: allocated = open sales-order reservations, value
// = on hand x the item's CURRENT purchasing price.

// openAllocations mirrors inventory.openAllocationsSQL (reserved or partially
// fulfilled sales-order allocations) -- kept as a literal here so this package
// does not import the inventory package for one query.
const openAllocations = `
	SELECT inventory_item_id, warehouse_id, SUM(allocated_quantity - fulfilled_quantity) AS allocated
	FROM inventory_allocation
	WHERE allocation_status IN ('reserved','partially_fulfilled')
	GROUP BY inventory_item_id, warehouse_id`

// SlabStatuses are the live slab states shown by Slab Availability, in column
// order. Consumed and scrapped slabs are gone from the yard and are left out.
var SlabStatuses = []string{"available", "reserved", "received", "on_order", "in_transit"}

// SlabStatusLabels name SlabStatuses, in the same order.
var SlabStatusLabels = []string{"Available", "Reserved", "Received (no bin)", "On order", "In transit"}

// StockRow is one item's stock at one location.
type StockRow struct {
	ItemUUID  string  `json:"itemId"`
	SKU       string  `json:"sku"`
	Name      string  `json:"name"`
	Tracking  string  `json:"tracking"`
	Unit      string  `json:"unit"`
	Location  string  `json:"location"`
	OnHand    float64 `json:"onHand"`
	Allocated float64 `json:"allocated"`
	Available float64 `json:"available"`
	UnitCost  float64 `json:"unitCost"`
	Value     float64 `json:"value"`
}

// StockTotals summarises the valuation.
type StockTotals struct {
	Value     float64 `json:"value"`
	Items     int     `json:"items"`
	Locations int     `json:"locations"`
	Rows      int     `json:"rows"`
}

// availableQty is what can still be promised: on hand less reservations,
// never negative (an over-allocated row has nothing available).
func availableQty(onHand, allocated float64) float64 {
	if a := onHand - allocated; a > 0 {
		return a
	}
	return 0
}

func finishStockRow(r StockRow) StockRow {
	r.Available = availableQty(r.OnHand, r.Allocated)
	r.Value = r.OnHand * r.UnitCost
	return r
}

func sumStock(rows []StockRow) StockTotals {
	items, locations := map[string]bool{}, map[string]bool{}
	var t StockTotals
	for _, r := range rows {
		t.Value += r.Value
		items[r.ItemUUID] = true
		locations[r.Location] = true
	}
	t.Items, t.Locations, t.Rows = len(items), len(locations), len(rows)
	return t
}

// StockOnHand lists every active item's stock at each location that holds any,
// with its value, biggest value first.
func StockOnHand(ctx context.Context, pool *pgxpool.Pool) ([]StockRow, StockTotals, error) {
	rows := []StockRow{}
	q := fmt.Sprintf(`
		SELECT ii.inventory_item_uuid::text, ii.inventory_item_sku, ii.inventory_item_name, ii.inventory_item_tracking,
		       COALESCE(u.unit_code, ''), w.name, s.quantity_on_hand, COALESCE(alloc.allocated, 0),
		       ii.inventory_item_purchase_unit_price
		FROM inventory_stock s
		JOIN inventory_item ii ON ii.inventory_item_id = s.inventory_item_id
		JOIN company_location w ON w.company_location_id = s.warehouse_id
		LEFT JOIN lkp_unit u ON u.unit_id = ii.inventory_item_unit_id
		LEFT JOIN (%s) alloc ON alloc.inventory_item_id = s.inventory_item_id AND alloc.warehouse_id = s.warehouse_id
		WHERE ii.inventory_item_deleted_at IS NULL AND ii.inventory_item_is_active AND s.quantity_on_hand > 0
		ORDER BY s.quantity_on_hand * ii.inventory_item_purchase_unit_price DESC, ii.inventory_item_name, w.name
		LIMIT %d`, openAllocations, maxReportRows)
	r, err := pool.Query(ctx, q)
	if err != nil {
		return nil, StockTotals{}, fmt.Errorf("query stock on hand: %w", err)
	}
	defer r.Close()
	for r.Next() {
		var row StockRow
		if err := r.Scan(&row.ItemUUID, &row.SKU, &row.Name, &row.Tracking, &row.Unit, &row.Location,
			&row.OnHand, &row.Allocated, &row.UnitCost); err != nil {
			return nil, StockTotals{}, fmt.Errorf("scan stock on hand: %w", err)
		}
		rows = append(rows, finishStockRow(row))
	}
	if err := r.Err(); err != nil {
		return nil, StockTotals{}, fmt.Errorf("query stock on hand: %w", err)
	}
	return rows, sumStock(rows), nil
}

// SlabRow is one material's live slabs split by status; Counts and Areas follow
// SlabStatuses.
type SlabRow struct {
	ItemUUID   string    `json:"itemId"`
	SKU        string    `json:"sku"`
	Name       string    `json:"name"`
	Unit       string    `json:"unit"`
	Counts     []int     `json:"counts"`
	Areas      []float64 `json:"areas"`
	TotalSlabs int       `json:"totalSlabs"`
	TotalArea  float64   `json:"totalArea"`
}

// SlabTotals sums every material, per status.
type SlabTotals struct {
	Counts     []int     `json:"counts"`
	Areas      []float64 `json:"areas"`
	TotalSlabs int       `json:"totalSlabs"`
}

func newSlabRow() SlabRow {
	return SlabRow{Counts: make([]int, len(SlabStatuses)), Areas: make([]float64, len(SlabStatuses))}
}

func finishSlabRow(r SlabRow) SlabRow {
	for i := range r.Counts {
		r.TotalSlabs += r.Counts[i]
		r.TotalArea += r.Areas[i]
	}
	return r
}

func sumSlabs(rows []SlabRow) SlabTotals {
	t := SlabTotals{Counts: make([]int, len(SlabStatuses)), Areas: make([]float64, len(SlabStatuses))}
	for _, r := range rows {
		for i := range r.Counts {
			t.Counts[i] += r.Counts[i]
			t.Areas[i] += r.Areas[i]
		}
		t.TotalSlabs += r.TotalSlabs
	}
	return t
}

// SlabAvailability counts the live serialized slabs of each material by
// status, with their area in the material's own unit, largest available area
// first.
func SlabAvailability(ctx context.Context, pool *pgxpool.Pool) ([]SlabRow, SlabTotals, error) {
	rows := []SlabRow{}
	var cols, in []string
	for _, s := range SlabStatuses {
		cols = append(cols,
			fmt.Sprintf("COUNT(*) FILTER (WHERE sl.slab_status = '%s')", s),
			fmt.Sprintf("COALESCE(SUM(sl.slab_area) FILTER (WHERE sl.slab_status = '%s'), 0)", s))
		in = append(in, "'"+s+"'")
	}
	q := fmt.Sprintf(`
		SELECT ii.inventory_item_uuid::text, ii.inventory_item_sku, ii.inventory_item_name, COALESCE(u.unit_code, ''),
		       %s
		FROM inventory_slab sl
		JOIN inventory_item ii ON ii.inventory_item_id = sl.inventory_item_id
		LEFT JOIN lkp_unit u ON u.unit_id = sl.slab_area_unit_id
		WHERE sl.slab_deleted_at IS NULL AND ii.inventory_item_deleted_at IS NULL AND sl.slab_status IN (%s)
		GROUP BY ii.inventory_item_uuid, ii.inventory_item_sku, ii.inventory_item_name, u.unit_code
		ORDER BY COALESCE(SUM(sl.slab_area) FILTER (WHERE sl.slab_status = 'available'), 0) DESC, ii.inventory_item_name
		LIMIT %d`, strings.Join(cols, ", "), strings.Join(in, ", "), maxReportRows)
	r, err := pool.Query(ctx, q)
	if err != nil {
		return nil, SlabTotals{}, fmt.Errorf("query slab availability: %w", err)
	}
	defer r.Close()
	for r.Next() {
		row := newSlabRow()
		dest := []any{&row.ItemUUID, &row.SKU, &row.Name, &row.Unit}
		for i := range SlabStatuses {
			dest = append(dest, &row.Counts[i], &row.Areas[i])
		}
		if err := r.Scan(dest...); err != nil {
			return nil, SlabTotals{}, fmt.Errorf("scan slab availability: %w", err)
		}
		rows = append(rows, finishSlabRow(row))
	}
	if err := r.Err(); err != nil {
		return nil, SlabTotals{}, fmt.Errorf("query slab availability: %w", err)
	}
	return rows, sumSlabs(rows), nil
}

// Low-stock states.
const (
	StateOutOfStock = "out"
	StateBelow      = "below"
)

// LowStockRow is one (item, location) at or below its reorder point.
type LowStockRow struct {
	ItemUUID     string  `json:"itemId"`
	SKU          string  `json:"sku"`
	Name         string  `json:"name"`
	Unit         string  `json:"unit"`
	Location     string  `json:"location"`
	OnHand       float64 `json:"onHand"`
	Allocated    float64 `json:"allocated"`
	Available    float64 `json:"available"`
	ReorderPoint float64 `json:"reorderPoint"`
	OnOrder      float64 `json:"onOrder"`
	Suggested    float64 `json:"suggestedOrder"`
	State        string  `json:"state"`
}

// LowStockTotals summarises the low-stock list.
type LowStockTotals struct {
	Rows       int `json:"rows"`
	OutOfStock int `json:"outOfStock"`
	Items      int `json:"items"`
}

// suggestedOrder is how much more to order to climb back to the reorder point,
// counting what is already on order; never negative.
func suggestedOrder(reorderPoint, onHand, onOrder float64) float64 {
	if s := reorderPoint - onHand - onOrder; s > 0 {
		return s
	}
	return 0
}

func lowStockState(onHand float64) string {
	if onHand <= 0 {
		return StateOutOfStock
	}
	return StateBelow
}

func finishLowStockRow(r LowStockRow) LowStockRow {
	r.Available = availableQty(r.OnHand, r.Allocated)
	r.Suggested = suggestedOrder(r.ReorderPoint, r.OnHand, r.OnOrder)
	r.State = lowStockState(r.OnHand)
	return r
}

func sumLowStock(rows []LowStockRow) LowStockTotals {
	items := map[string]bool{}
	t := LowStockTotals{Rows: len(rows)}
	for _, r := range rows {
		items[r.ItemUUID] = true
		if r.State == StateOutOfStock {
			t.OutOfStock++
		}
	}
	t.Items = len(items)
	return t
}

// LowStock lists active items whose on-hand quantity at a location is at or
// below that location's reorder point (only items with a reorder point set),
// most depleted first. On order is the open purchase-order quantity not yet
// received, across all locations (purchase orders have no location).
func LowStock(ctx context.Context, pool *pgxpool.Pool) ([]LowStockRow, LowStockTotals, error) {
	rows := []LowStockRow{}
	q := fmt.Sprintf(`
		SELECT ii.inventory_item_uuid::text, ii.inventory_item_sku, ii.inventory_item_name, COALESCE(u.unit_code, ''),
		       w.name, s.quantity_on_hand, COALESCE(alloc.allocated, 0), s.reorder_point, COALESCE(inc.on_order, 0)
		FROM inventory_stock s
		JOIN inventory_item ii ON ii.inventory_item_id = s.inventory_item_id
		JOIN company_location w ON w.company_location_id = s.warehouse_id
		LEFT JOIN lkp_unit u ON u.unit_id = ii.inventory_item_unit_id
		LEFT JOIN (%[1]s) alloc ON alloc.inventory_item_id = s.inventory_item_id AND alloc.warehouse_id = s.warehouse_id
		LEFT JOIN (
			SELECT l.inventory_item_id, SUM(GREATEST(l.quantity - l.qty_received, 0)) AS on_order
			FROM purchase_order_item l
			JOIN purchase_order po ON po.purchase_order_id = l.purchase_order_id AND po.purchase_order_deleted_at IS NULL
			JOIN lkp_record_status rs ON rs.record_status_id = po.purchase_order_status
			WHERE l.item_deleted_at IS NULL AND l.inventory_item_id IS NOT NULL AND rs.record_status_code = ANY($1)
			GROUP BY l.inventory_item_id
		) inc ON inc.inventory_item_id = s.inventory_item_id
		WHERE ii.inventory_item_deleted_at IS NULL AND ii.inventory_item_is_active
		  AND s.reorder_point > 0 AND s.quantity_on_hand <= s.reorder_point
		ORDER BY s.quantity_on_hand / s.reorder_point ASC, ii.inventory_item_name, w.name
		LIMIT %[2]d`, openAllocations, maxReportRows)
	r, err := pool.Query(ctx, q, poOpenCodes)
	if err != nil {
		return nil, LowStockTotals{}, fmt.Errorf("query low stock: %w", err)
	}
	defer r.Close()
	for r.Next() {
		var row LowStockRow
		if err := r.Scan(&row.ItemUUID, &row.SKU, &row.Name, &row.Unit, &row.Location,
			&row.OnHand, &row.Allocated, &row.ReorderPoint, &row.OnOrder); err != nil {
			return nil, LowStockTotals{}, fmt.Errorf("scan low stock: %w", err)
		}
		rows = append(rows, finishLowStockRow(row))
	}
	if err := r.Err(); err != nil {
		return nil, LowStockTotals{}, fmt.Errorf("query low stock: %w", err)
	}
	return rows, sumLowStock(rows), nil
}

// MovementRow is one item's stock movement in the window, in the item's unit.
// Quantities are signed: receipts and returns add, consumption and scrap
// subtract; Adjusted covers counts, adjustments and transfers.
type MovementRow struct {
	ItemUUID  string  `json:"itemId"`
	SKU       string  `json:"sku"`
	Name      string  `json:"name"`
	Unit      string  `json:"unit"`
	Received  float64 `json:"received"`
	Returned  float64 `json:"returned"`
	Consumed  float64 `json:"consumed"`
	Scrapped  float64 `json:"scrapped"`
	Adjusted  float64 `json:"adjusted"`
	Net       float64 `json:"net"`
	Movements int     `json:"movements"`
}

// MovementTotals summarises the movement list. Quantities are not summed
// across items because their units differ.
type MovementTotals struct {
	Items     int `json:"items"`
	Movements int `json:"movements"`
	Growing   int `json:"growing"`
	Shrinking int `json:"shrinking"`
}

func sumMovements(rows []MovementRow) MovementTotals {
	t := MovementTotals{Items: len(rows)}
	for _, r := range rows {
		t.Movements += r.Movements
		switch {
		case r.Net > 0:
			t.Growing++
		case r.Net < 0:
			t.Shrinking++
		}
	}
	return t
}

// StockMovement sums each item's stock movements dated in the window, from
// both ledgers (quantity items and serialized slabs), most active first.
func StockMovement(ctx context.Context, pool *pgxpool.Pool, from, to time.Time) ([]MovementRow, MovementTotals, error) {
	rows := []MovementRow{}
	args := []any{}
	dates := dateClause("mv.occurred_at", from, to, &args)
	q := fmt.Sprintf(`
		WITH mv AS (
			SELECT inventory_item_id, event, quantity_delta, occurred_at FROM inventory_ledger
			UNION ALL
			SELECT inventory_item_id, event, quantity_delta, occurred_at FROM inventory_slab_ledger
		)
		SELECT ii.inventory_item_uuid::text, ii.inventory_item_sku, ii.inventory_item_name, COALESCE(u.unit_code, ''),
		       COALESCE(SUM(mv.quantity_delta) FILTER (WHERE mv.event = 'received'), 0),
		       COALESCE(SUM(mv.quantity_delta) FILTER (WHERE mv.event IN ('returned', 'recovered')), 0),
		       COALESCE(SUM(mv.quantity_delta) FILTER (WHERE mv.event = 'consumed'), 0),
		       COALESCE(SUM(mv.quantity_delta) FILTER (WHERE mv.event = 'scrapped'), 0),
		       COALESCE(SUM(mv.quantity_delta) FILTER (WHERE mv.event = 'adjusted'), 0),
		       COALESCE(SUM(mv.quantity_delta), 0), COUNT(*)
		FROM mv
		JOIN inventory_item ii ON ii.inventory_item_id = mv.inventory_item_id
		LEFT JOIN lkp_unit u ON u.unit_id = ii.inventory_item_unit_id
		WHERE ii.inventory_item_deleted_at IS NULL%s
		GROUP BY ii.inventory_item_uuid, ii.inventory_item_sku, ii.inventory_item_name, u.unit_code
		ORDER BY COUNT(*) DESC, ii.inventory_item_name
		LIMIT %d`, dates, maxReportRows)
	r, err := pool.Query(ctx, q, args...)
	if err != nil {
		return nil, MovementTotals{}, fmt.Errorf("query stock movement: %w", err)
	}
	defer r.Close()
	for r.Next() {
		var row MovementRow
		if err := r.Scan(&row.ItemUUID, &row.SKU, &row.Name, &row.Unit, &row.Received, &row.Returned,
			&row.Consumed, &row.Scrapped, &row.Adjusted, &row.Net, &row.Movements); err != nil {
			return nil, MovementTotals{}, fmt.Errorf("scan stock movement: %w", err)
		}
		rows = append(rows, row)
	}
	if err := r.Err(); err != nil {
		return nil, MovementTotals{}, fmt.Errorf("query stock movement: %w", err)
	}
	return rows, sumMovements(rows), nil
}
