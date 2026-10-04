package inventory

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
)

// lockItems loads the items an order touches and row-locks them in id order, so
// two orders saving at once for the same stone are checked one after the other
// (the second sees the first's reservation) and cannot deadlock each other.
func lockItems(ctx context.Context, tx pgx.Tx, ids []int) (map[int]stockedItem, error) {
	rows, err := tx.Query(ctx, `
		SELECT ii.inventory_item_id, ii.inventory_item_uuid, ii.inventory_item_sku, ii.inventory_item_name,
		       COALESCE(u.unit_code,''), ii.inventory_item_tracking, ii.inventory_item_track_stock,
		       ii.inventory_item_default_warehouse_id
		FROM inventory_item ii
		LEFT JOIN lkp_unit u ON u.unit_id = ii.inventory_item_unit_id
		WHERE ii.inventory_item_id = ANY($1)
		ORDER BY ii.inventory_item_id
		FOR UPDATE OF ii`, ids)
	if err != nil {
		return nil, fmt.Errorf("lock items to reserve: %w", err)
	}
	defer rows.Close()
	out := map[int]stockedItem{}
	for rows.Next() {
		var s stockedItem
		if err := rows.Scan(&s.id, &s.uuid, &s.sku, &s.name, &s.unitCode, &s.tracking, &s.trackStock, &s.defaultWhID); err != nil {
			return nil, fmt.Errorf("scan item to reserve: %w", err)
		}
		out[s.id] = s
	}
	return out, rows.Err()
}

// stockPosition is the on-hand and the still-held quantity of each item. Called
// after the order's own reservation is released, so "held" is other orders' only.
func stockPosition(ctx context.Context, tx pgx.Tx, ids []int) (onHand, held map[int]float64, err error) {
	onHand, held = map[int]float64{}, map[int]float64{}
	rows, err := tx.Query(ctx, `
		SELECT stock.inventory_item_id, GREATEST(COALESCE(SUM(stock.quantity_on_hand), 0) - COALESCE((
 SELECT SUM(s.slab_area) FROM inventory_slab s WHERE s.inventory_item_id=stock.inventory_item_id
 AND s.inspection_status IN ('pending','rejected') AND s.slab_status IN ('available','reserved') AND s.slab_deleted_at IS NULL
 ),0),0)
 FROM inventory_stock stock WHERE stock.inventory_item_id = ANY($1) GROUP BY stock.inventory_item_id`, ids)
	if err != nil {
		return nil, nil, fmt.Errorf("sum stock on hand: %w", err)
	}
	for rows.Next() {
		var id int
		var v float64
		if err := rows.Scan(&id, &v); err != nil {
			rows.Close()
			return nil, nil, fmt.Errorf("scan stock on hand: %w", err)
		}
		onHand[id] = v
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	rows, err = tx.Query(ctx, `
		SELECT inventory_item_id, COALESCE(SUM(allocated_quantity - fulfilled_quantity), 0)
		FROM inventory_allocation
		WHERE inventory_item_id = ANY($1) AND allocation_status IN `+openAllocation+`
		GROUP BY inventory_item_id`, ids)
	if err != nil {
		return nil, nil, fmt.Errorf("sum held stock: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id int
		var v float64
		if err := rows.Scan(&id, &v); err != nil {
			return nil, nil, fmt.Errorf("scan held stock: %w", err)
		}
		held[id] = v
	}
	return onHand, held, rows.Err()
}

// fulfilledByItem is how much of each item has already been fulfilled against
// the order's open reservations.
func fulfilledByItem(ctx context.Context, tx pgx.Tx, orderID int) (map[int]float64, error) {
	rows, err := tx.Query(ctx, `
		SELECT inventory_item_id, COALESCE(SUM(fulfilled_quantity), 0)
		FROM inventory_allocation
		WHERE sales_order_id = $1 AND allocation_status IN `+openAllocation+`
		GROUP BY inventory_item_id`, orderID)
	if err != nil {
		return nil, fmt.Errorf("sum fulfilled stock: %w", err)
	}
	defer rows.Close()
	out := map[int]float64{}
	for rows.Next() {
		var id int
		var v float64
		if err := rows.Scan(&id, &v); err != nil {
			return nil, fmt.Errorf("scan fulfilled stock: %w", err)
		}
		out[id] = v
	}
	return out, rows.Err()
}

// defaultWarehouseID is the location a reservation is filed under when neither
// the line nor the item names one: the tenant's default location, else the
// oldest live one.
func defaultWarehouseID(ctx context.Context, tx pgx.Tx) (int, error) {
	var id int
	err := tx.QueryRow(ctx, `
		SELECT company_location_id FROM company_location
		WHERE deleted_at IS NULL
		ORDER BY is_default DESC, company_location_id LIMIT 1`).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ClientError{Msg: "No location is set up to hold stock for this order. Add one under Configuration → Company Info → Locations."}
	}
	if err != nil {
		return 0, fmt.Errorf("resolve default warehouse: %w", err)
	}
	return id, nil
}
