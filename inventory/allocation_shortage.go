package inventory

import (
	"context"
	"fmt"
	"sort"
)

// OrderShortages is what the order holds that the stock cannot cover, one entry
// per short item (none when it is all covered). An order is never refused for
// want of stock, so this is how the screen learns to warn that delivery may be
// delayed and what to put on a purchase order.
//
// Orders are served first come, first served: what the order may count on is
// the on-hand left after every OLDER open order's hold (lower sales_order_id),
// so two orders over-promising the same stone do not both claim it, and a newer
// order never makes an older one look short. Stone already cut for the order has
// left on-hand and is not part of its hold, so it is not counted twice.
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

	var out []Shortage
	for _, h := range holds {
		if s, short := shortfall(h.s, h.need, onHand[h.id]-h.ahead); short {
			out = append(out, s)
		}
	}
	return out, nil
}

// shortfall fills in s for an order needing need of an item when free is what is
// left for it, reporting whether it is short at all. Rounding noise is not a
// shortage; free is never shown below zero.
func shortfall(s Shortage, need, free float64) (Shortage, bool) {
	if free < 0 {
		free = 0
	}
	if need <= free+stockEpsilon {
		return Shortage{}, false
	}
	s.Requested = roundTo(need, areaScale)
	s.Available = roundTo(free, areaScale)
	s.Short = roundTo(need-free, areaScale)
	return s, true
}
