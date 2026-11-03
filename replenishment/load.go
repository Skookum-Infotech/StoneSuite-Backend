package replenishment

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/inventory"
)

// The record statuses that take a purchase order out of "on order" are named
// as an exclusion list, not an allow-list of open ones, so a status this
// package has never heard of (a future addition) counts as open rather than
// silently vanishing from on-order and getting the item ordered twice. CLSD and
// CANC are the only terminal states in purchaseorder/transitions.go. A draft is
// deliberately open: a draft PO created from this page must stop the item
// showing up as short.
const (
	poStatusClosed    = "CLSD"
	poStatusCancelled = "CANC"
)

// monthLabelFormat is how a history month is named in the response and in SQL
// (to_char 'YYYY-MM').
const monthLabelFormat = "2006-01"

// historyWindow returns the [start, end) span of the HistoryMonths full
// calendar months before the one `now` is in, and the label of each month,
// oldest first.
func historyWindow(now time.Time) (start, end time.Time, months []string) {
	end = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, time.UTC)
	start = end.AddDate(0, -HistoryMonths, 0)
	months = make([]string, HistoryMonths)
	for i := range months {
		months[i] = start.AddDate(0, i, 0).Format(monthLabelFormat)
	}
	return start, end, months
}

// LoadItems works out every active, stock-tracked item that is short for next
// month. `now` fixes which months are "the last three"; callers pass the
// current time, tests a fixed one.
func LoadItems(ctx context.Context, pool *pgxpool.Pool, now time.Time) (Result, error) {
	start, end, months := historyWindow(now)

	facts, err := loadItemFacts(ctx, pool)
	if err != nil {
		return Result{}, err
	}
	if err := loadUsage(ctx, pool, facts, start, end, months); err != nil {
		return Result{}, err
	}
	if err := loadOnHand(ctx, pool, facts); err != nil {
		return Result{}, err
	}
	if err := loadOnOrder(ctx, pool, facts); err != nil {
		return Result{}, err
	}

	list := make([]itemFacts, 0, len(facts))
	for _, f := range facts {
		list = append(list, *f)
	}
	return Result{Months: months, Items: computeItems(list, months)}, nil
}

// loadItemFacts returns one row per active, stock-tracked item, keyed by its
// internal id, with zeroed numbers for the follow-up queries to fill in.
func loadItemFacts(ctx context.Context, pool *pgxpool.Pool) (map[int]*itemFacts, error) {
	rows, err := pool.Query(ctx, `
		SELECT ii.inventory_item_id, ii.inventory_item_uuid::text, ii.inventory_item_sku,
		       ii.inventory_item_name, ii.inventory_item_tracking, COALESCE(u.unit_code, '')
		FROM inventory_item ii
		LEFT JOIN lkp_unit u ON u.unit_id = ii.inventory_item_unit_id
		WHERE ii.inventory_item_deleted_at IS NULL
		  AND ii.inventory_item_is_active
		  AND ii.inventory_item_track_stock`)
	if err != nil {
		return nil, fmt.Errorf("load reorder items: %w", err)
	}
	defer rows.Close()

	out := map[int]*itemFacts{}
	for rows.Next() {
		var id int
		f := &itemFacts{usage: make([]float64, HistoryMonths)}
		if err := rows.Scan(&id, &f.id, &f.sku, &f.name, &f.tracking, &f.unit); err != nil {
			return nil, fmt.Errorf("scan reorder item: %w", err)
		}
		if f.tracking == inventory.TrackingSerialized {
			f.unit = SlabUnit
		}
		out[id] = f
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load reorder items: %w", err)
	}
	return out, nil
}

// loadUsage fills each item's per-month usage.
//
// A slab-tracked item counts the distinct FULL slabs consumed in the month: a
// slab is consumed once (the ledger enforces it), and a remnant cut from one is
// the same stone, so counting it again when the remnant is used would inflate
// demand. Any other item sums the bulk ledger's 'consumed' rows, which are
// negative deltas.
func loadUsage(ctx context.Context, pool *pgxpool.Pool, facts map[int]*itemFacts, start, end time.Time, months []string) error {
	monthIndex := make(map[string]int, len(months))
	for i, m := range months {
		monthIndex[m] = i
	}

	slabRows, err := pool.Query(ctx, `
		SELECT sl.inventory_item_id, to_char(date_trunc('month', sl.occurred_at), 'YYYY-MM'),
		       COUNT(DISTINCT sl.inventory_slab_id)
		FROM inventory_slab_ledger sl
		JOIN inventory_slab s ON s.inventory_slab_id = sl.inventory_slab_id
		WHERE sl.event = $1 AND s.slab_form = 'full'
		  AND sl.occurred_at >= $2 AND sl.occurred_at < $3
		GROUP BY 1, 2`, inventory.EventConsumed, start, end)
	if err != nil {
		return fmt.Errorf("load slab usage: %w", err)
	}
	if err := addUsage(slabRows, facts, monthIndex, inventory.TrackingSerialized); err != nil {
		return err
	}

	bulkRows, err := pool.Query(ctx, `
		SELECT il.inventory_item_id, to_char(date_trunc('month', il.occurred_at), 'YYYY-MM'),
		       SUM(-il.quantity_delta)
		FROM inventory_ledger il
		WHERE il.event = $1 AND il.occurred_at >= $2 AND il.occurred_at < $3
		GROUP BY 1, 2`, inventory.EventConsumed, start, end)
	if err != nil {
		return fmt.Errorf("load bulk usage: %w", err)
	}
	return addUsage(bulkRows, facts, monthIndex, inventory.TrackingQuantity)
}

// addUsage drains a (item id, month, amount) result into the facts. Rows for an
// item that is not loaded (inactive, untracked, deleted) or of the other
// tracking mode are ignored.
func addUsage(rows pgx.Rows, facts map[int]*itemFacts, monthIndex map[string]int, tracking string) error {
	defer rows.Close()
	for rows.Next() {
		var (
			itemID int
			month  string
			used   float64
		)
		if err := rows.Scan(&itemID, &month, &used); err != nil {
			return fmt.Errorf("scan usage: %w", err)
		}
		row, ok := facts[itemID]
		idx, inWindow := monthIndex[month]
		if !ok || !inWindow || row.tracking != tracking {
			continue
		}
		row.usage[idx] += used
	}
	return rows.Err()
}

// loadOnHand fills each item's stock in reorder terms. A slab-tracked item
// counts the full slabs it physically holds: everything that is not consumed,
// scrapped or merely promised by an open order. Reserved slabs stay counted —
// see computeItems. Remnants are not counted, matching how usage is counted.
// Any other item reads its summed stock level.
func loadOnHand(ctx context.Context, pool *pgxpool.Pool, facts map[int]*itemFacts) error {
	slabRows, err := pool.Query(ctx, `
		SELECT inventory_item_id, COUNT(*)
		FROM inventory_slab
		WHERE slab_deleted_at IS NULL AND slab_form = 'full'
		  AND slab_status NOT IN ('consumed', 'scrapped', 'on_order')
		GROUP BY 1`)
	if err != nil {
		return fmt.Errorf("load slab stock: %w", err)
	}
	defer slabRows.Close()
	for slabRows.Next() {
		var (
			itemID int
			n      float64
		)
		if err := slabRows.Scan(&itemID, &n); err != nil {
			return fmt.Errorf("scan slab stock: %w", err)
		}
		if row, ok := facts[itemID]; ok && row.tracking == inventory.TrackingSerialized {
			row.onHand = n
		}
	}
	if err := slabRows.Err(); err != nil {
		return fmt.Errorf("load slab stock: %w", err)
	}

	stockRows, err := pool.Query(ctx, `
		SELECT inventory_item_id, SUM(quantity_on_hand) FROM inventory_stock GROUP BY 1`)
	if err != nil {
		return fmt.Errorf("load bulk stock: %w", err)
	}
	defer stockRows.Close()
	for stockRows.Next() {
		var (
			itemID int
			qty    float64
		)
		if err := stockRows.Scan(&itemID, &qty); err != nil {
			return fmt.Errorf("scan bulk stock: %w", err)
		}
		if row, ok := facts[itemID]; ok && row.tracking == inventory.TrackingQuantity {
			row.onHand = qty
		}
	}
	if err := stockRows.Err(); err != nil {
		return fmt.Errorf("load bulk stock: %w", err)
	}
	return nil
}

// loadOnOrder fills each item's outstanding purchase order quantity, and the
// orders behind it, from every purchase order line that is not on a closed or
// cancelled order (drafts included).
func loadOnOrder(ctx context.Context, pool *pgxpool.Pool, facts map[int]*itemFacts) error {
	avgArea, err := loadAverageSlabArea(ctx, pool)
	if err != nil {
		return err
	}

	rows, err := pool.Query(ctx, `
		SELECT poi.inventory_item_id, po.purchase_order_uuid::text, COALESCE(po.purchase_order_number, ''),
		       rs.record_status_name, poi.quantity, poi.qty_received, poi.expected_slabs,
		       (poi.dim_length IS NOT NULL AND poi.dim_width IS NOT NULL),
		       COUNT(s.inventory_slab_id),
		       COUNT(s.inventory_slab_id) FILTER (WHERE s.slab_status = 'on_order')
		FROM purchase_order_item poi
		JOIN purchase_order po ON po.purchase_order_id = poi.purchase_order_id
		JOIN lkp_record_status rs ON rs.record_status_id = po.purchase_order_status
		LEFT JOIN inventory_slab s ON s.purchase_order_item_id = poi.purchase_order_item_id
		                          AND s.slab_deleted_at IS NULL
		WHERE poi.item_deleted_at IS NULL AND po.purchase_order_deleted_at IS NULL
		  AND poi.inventory_item_id IS NOT NULL
		  AND rs.record_status_code NOT IN ($1, $2)
		GROUP BY poi.purchase_order_item_id, po.purchase_order_id, rs.record_status_id
		ORDER BY po.purchase_order_date, po.purchase_order_id`, poStatusClosed, poStatusCancelled)
	if err != nil {
		return fmt.Errorf("load open purchase order lines: %w", err)
	}
	defer rows.Close()

	byOrder := map[int]map[string]int{} // item id -> PO uuid -> index in OpenOrders
	for rows.Next() {
		var (
			itemID int
			order  OpenOrder
			line   lineRemaining
		)
		if err := rows.Scan(&itemID, &order.ID, &order.Number, &order.Status,
			&line.quantity, &line.received, &line.expected, &line.sized, &line.mintedAll, &line.mintedOpen); err != nil {
			return fmt.Errorf("scan open purchase order line: %w", err)
		}
		row, ok := facts[itemID]
		if !ok {
			continue
		}
		line.serialized = row.tracking == inventory.TrackingSerialized
		line.avgSlabArea = avgArea[itemID]
		left := line.remaining()
		if left <= stockEpsilon {
			continue
		}
		row.onOrder += left

		idx, seen := byOrder[itemID][order.ID]
		if !seen {
			order.Remaining = left
			row.orders = append(row.orders, order)
			if byOrder[itemID] == nil {
				byOrder[itemID] = map[string]int{}
			}
			byOrder[itemID][order.ID] = len(row.orders) - 1
			continue
		}
		row.orders[idx].Remaining += left
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("load open purchase order lines: %w", err)
	}
	return nil
}

// loadAverageSlabArea returns, per slab-tracked item, the average area of its
// full slabs on record in the item's own unit — what an unsized purchase order
// line's area is measured against. Items with no slabs are absent.
func loadAverageSlabArea(ctx context.Context, pool *pgxpool.Pool) (map[int]float64, error) {
	rows, err := pool.Query(ctx, `
		SELECT inventory_item_id, AVG(slab_area)
		FROM inventory_slab
		WHERE slab_deleted_at IS NULL AND slab_form = 'full'
		GROUP BY 1`)
	if err != nil {
		return nil, fmt.Errorf("load average slab area: %w", err)
	}
	defer rows.Close()
	out := map[int]float64{}
	for rows.Next() {
		var (
			itemID int
			area   float64
		)
		if err := rows.Scan(&itemID, &area); err != nil {
			return nil, fmt.Errorf("scan average slab area: %w", err)
		}
		out[itemID] = area
	}
	return out, rows.Err()
}
