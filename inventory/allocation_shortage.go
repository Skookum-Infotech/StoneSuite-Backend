package inventory

import (
	"context"
	"fmt"
	"sort"
)

// onOrderStatuses are the purchase order statuses whose lines count as stock on
// its way: sent to the vendor and not yet all received. A draft, or one still
// awaiting approval, is not a promise yet; a received, closed or cancelled one
// has nothing more to bring.
var onOrderStatuses = []string{"SENT", "PART"}

// OrderShortages is what the order holds that the stock cannot cover, one entry
// per such item (none when it is all in stock). An order is never refused for
// want of stock, so this is how the screen learns to warn that delivery may be
// delayed, which purchase orders are already bringing the rest, and what is
// left to put on a new one.
//
// Orders are served first come, first served: what the order may count on is
// the on-hand left after every OLDER open order's hold (lower sales_order_id),
// so two orders over-promising the same stone do not both claim it, and a newer
// order never makes an older one look short. Stock on order is shared out the
// same way, after the older orders' own gaps. Stone already cut for the order
// has left on-hand and is not part of its hold, so it is not counted twice.
func OrderShortages(ctx context.Context, q rowQuerier, orderID int) ([]Shortage, error) {
	rows, err := q.Query(ctx, `
		WITH mine AS (
			SELECT inventory_item_id, SUM(allocated_quantity - fulfilled_quantity) AS need
			FROM inventory_allocation
			WHERE sales_order_id = $1 AND allocation_status IN `+openAllocation+`
			GROUP BY inventory_item_id
			HAVING SUM(allocated_quantity - fulfilled_quantity) > 0
		)
		SELECT ii.inventory_item_id, ii.inventory_item_uuid, ii.inventory_item_sku, ii.inventory_item_name,
		       COALESCE(u.unit_code,''), mine.need,
		       COALESCE((
		           SELECT SUM(a.allocated_quantity - a.fulfilled_quantity) FROM inventory_allocation a
		           WHERE a.inventory_item_id = mine.inventory_item_id AND a.sales_order_id < $1
		             AND a.allocation_status IN `+openAllocation+`
		       ), 0) AS ahead
		FROM mine
		JOIN inventory_item ii ON ii.inventory_item_id = mine.inventory_item_id
		LEFT JOIN lkp_unit u ON u.unit_id = ii.inventory_item_unit_id
		ORDER BY ii.inventory_item_sku`, orderID)
	if err != nil {
		return nil, fmt.Errorf("load order holds: %w", err)
	}
	type hold struct {
		id          int
		s           Shortage
		need, ahead float64
	}
	var holds []hold
	for rows.Next() {
		var h hold
		if err := rows.Scan(&h.id, &h.s.ItemID, &h.s.SKU, &h.s.Name, &h.s.UnitCode, &h.need, &h.ahead); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan order hold: %w", err)
		}
		holds = append(holds, h)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(holds) == 0 {
		return nil, nil
	}

	ids := make([]int, len(holds))
	for i, h := range holds {
		ids[i] = h.id
	}
	sort.Ints(ids)
	onHand, _, err := stockPosition(ctx, q, ids)
	if err != nil {
		return nil, err
	}
	incoming, err := onOrder(ctx, q, ids)
	if err != nil {
		return nil, err
	}

	var out []Shortage
	for _, h := range holds {
		in := incoming[h.id]
		// Older orders' own gaps take the incoming stock first.
		aheadGap := h.ahead - onHand[h.id]
		if aheadGap < 0 {
			aheadGap = 0
		}
		if s, short := shortfall(h.s, h.need, onHand[h.id]-h.ahead, in.quantity-aheadGap); short {
			if s.OnOrder > 0 {
				s.PurchaseOrders = in.orders
			}
			out = append(out, s)
		}
	}
	return out, nil
}

// incomingStock is what sent purchase orders are still to deliver of one item.
type incomingStock struct {
	quantity float64
	orders   []string // purchase order numbers, sorted
}

// onOrder is the quantity of each item still to arrive on sent purchase orders:
// each line's ordered quantity less what has been received against it.
func onOrder(ctx context.Context, q rowQuerier, ids []int) (map[int]incomingStock, error) {
	rows, err := q.Query(ctx, `
		SELECT poi.inventory_item_id,
		       SUM(poi.quantity - poi.qty_received),
		       ARRAY_AGG(DISTINCT COALESCE(po.purchase_order_number, ''))
		FROM purchase_order_item poi
		JOIN purchase_order po ON po.purchase_order_id = poi.purchase_order_id
		JOIN lkp_record_status rs ON rs.record_status_id = po.purchase_order_status
		WHERE poi.inventory_item_id = ANY($1)
		  AND poi.item_deleted_at IS NULL AND po.purchase_order_deleted_at IS NULL
		  AND rs.record_status_code = ANY($2)
		  AND poi.quantity > poi.qty_received
		GROUP BY poi.inventory_item_id`, ids, onOrderStatuses)
	if err != nil {
		return nil, fmt.Errorf("sum stock on order: %w", err)
	}
	defer rows.Close()
	out := map[int]incomingStock{}
	for rows.Next() {
		var id int
		var in incomingStock
		var numbers []string
		if err := rows.Scan(&id, &in.quantity, &numbers); err != nil {
			return nil, fmt.Errorf("scan stock on order: %w", err)
		}
		for _, n := range numbers {
			if n != "" {
				in.orders = append(in.orders, n)
			}
		}
		out[id] = in
	}
	return out, rows.Err()
}

// shortfall fills in s for an order needing need of an item when free is what is
// left for it in stock and incoming what is left for it on sent purchase orders,
// reporting whether stock falls short at all. The gap is covered by incoming
// first (OnOrder); whatever is still uncovered is Short. Rounding noise is not a
// gap; neither free nor incoming is ever counted below zero.
func shortfall(s Shortage, need, free, incoming float64) (Shortage, bool) {
	if free < 0 {
		free = 0
	}
	if need <= free+stockEpsilon {
		return Shortage{}, false
	}
	if incoming < 0 {
		incoming = 0
	}
	gap := need - free
	covered := incoming
	if covered > gap {
		covered = gap
	}
	short := gap - covered
	if short <= stockEpsilon {
		short, covered = 0, gap
	}
	s.Requested = roundTo(need, areaScale)
	s.Available = roundTo(free, areaScale)
	s.OnOrder = roundTo(covered, areaScale)
	s.Short = roundTo(short, areaScale)
	return s, true
}
