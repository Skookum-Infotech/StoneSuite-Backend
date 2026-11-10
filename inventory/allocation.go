package inventory

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
	"sort"
	"strconv"
)

// stockEpsilon is half a unit at the DECIMAL(14,3) scale every quantity is
// stored at: differences smaller than this are rounding, not stock.
const stockEpsilon = 0.0005

// openAllocation is the SQL set of reservation states that still hold stock.
const openAllocation = `('reserved','partially_fulfilled')`

// sordRecordTypeCode identifies sales orders in lkp_record_type, for the ledger
// rows a filled order writes.
const sordRecordTypeCode = "SORD"

// ReserveLine is one order line asking for stock.
type ReserveLine struct {
	// LineID is the sales_order_item_id of the live line.
	LineID int
	// ItemID is the inventory_item_id the line is for.
	ItemID int
	// WarehouseID is the line's own warehouse, if it names one.
	WarehouseID *int
	Quantity    float64
}

// Shortage is one item an order holds more of than the stock can cover. It does
// not stop the order: the order is saved and reserves the full quantity (a
// backorder), and the shortfall is reported so the screen can warn that
// delivery may be delayed and offer to order the rest (see OrderShortages).
type Shortage struct {
	ItemID   string `json:"itemId"` // inventory_item_uuid
	SKU      string `json:"sku"`
	Name     string `json:"name"`
	UnitCode string `json:"unitCode"`
	// Requested is what the order still needs; Available is what is free for it
	// (never negative); Short is the difference.
	Requested float64 `json:"requested"`
	Available float64 `json:"available"`
	Short     float64 `json:"short"`
}

func trimQty(v float64) string {
	return strconv.FormatFloat(roundTo(v, areaScale), 'f', -1, 64)
}

// stockedItem is what the reservation needs to know about a catalogue item.
type stockedItem struct {
	id          int
	uuid        string
	sku         string
	name        string
	unitCode    string
	tracking    string
	trackStock  bool
	defaultWhID *int
}

// counted reports whether an order line for this item is checked and reserves. A
// slab-tracked item always is; anything else only if it is switched on.
func (s stockedItem) counted() bool {
	return s.tracking == TrackingSerialized || s.trackStock
}

// ReleaseOrder ends every open reservation the order holds, returning the stock
// to whoever else wants it. Idempotent.
func ReleaseOrder(ctx context.Context, tx pgx.Tx, orderID int) error {
	if _, err := tx.Exec(ctx, `
		UPDATE inventory_allocation SET allocation_status = 'released', allocation_updated_at = NOW()
		WHERE sales_order_id = $1 AND allocation_status IN `+openAllocation, orderID); err != nil {
		return fmt.Errorf("release order reservations: %w", err)
	}
	return nil
}

// ReserveOrder replaces the order's reservation with one matching its lines. It
// never refuses an order for want of stock: a line asking for more than is free
// still reserves its full quantity, so the hold doubles as the backorder, and
// OrderShortages reports what the stock cannot cover.
//
// It is called on create and again on every edit, and so REPLACES rather than
// adds: the order's old reservation is released first, which is what lets an
// order be edited without competing with its own earlier hold.
//
// Stone already cut for the order (fulfilled) is carried across the rewrite. It
// has already left on-hand, so the order is only asking for the rest.
func ReserveOrder(ctx context.Context, tx pgx.Tx, orderID int, lines []ReserveLine, actorEmployeeID int) error {
	carried, err := fulfilledByItem(ctx, tx, orderID)
	if err != nil {
		return err
	}
	if err := ReleaseOrder(ctx, tx, orderID); err != nil {
		return err
	}

	idSet := map[int]bool{}
	for _, l := range lines {
		idSet[l.ItemID] = true
	}
	if len(idSet) == 0 {
		return nil
	}
	ids := make([]int, 0, len(idSet))
	for id := range idSet {
		ids = append(ids, id)
	}
	sort.Ints(ids)

	items, err := lockItems(ctx, tx, ids)
	if err != nil {
		return err
	}

	var counted []int
	for _, id := range ids {
		if items[id].counted() {
			counted = append(counted, id)
		}
	}
	if len(counted) == 0 {
		return nil
	}

	fallbackWh := 0
	for _, l := range lines {
		it := items[l.ItemID]
		if !it.counted() {
			continue
		}
		fulfilled := 0.0
		if left := carried[l.ItemID]; left > 0 {
			fulfilled = roundTo(minFloat(l.Quantity, left), areaScale)
			carried[l.ItemID] = left - fulfilled
		}
		wh := 0
		switch {
		case l.WarehouseID != nil && *l.WarehouseID > 0:
			wh = *l.WarehouseID
		case it.defaultWhID != nil && *it.defaultWhID > 0:
			wh = *it.defaultWhID
		default:
			if fallbackWh == 0 {
				if fallbackWh, err = defaultWarehouseID(ctx, tx); err != nil {
					return err
				}
			}
			wh = fallbackWh
		}
		status := "reserved"
		switch {
		case fulfilled >= l.Quantity-stockEpsilon:
			status = "fulfilled"
		case fulfilled > 0:
			status = "partially_fulfilled"
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO inventory_allocation (
				inventory_item_id, warehouse_id, sales_order_id, sales_order_item_id,
				allocated_quantity, fulfilled_quantity, allocation_status, allocation_created_by
			) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
			l.ItemID, wh, orderID, l.LineID, l.Quantity, fulfilled, status, nullableInt(actorEmployeeID)); err != nil {
			if isFKViolation(err) {
				return ClientError{Msg: "A line names a location that does not exist."}
			}
			return fmt.Errorf("insert inventory allocation: %w", err)
		}
		if fulfilled > 0 {
			if _, err := tx.Exec(ctx,
				`UPDATE sales_order_item SET line_fulfilled_quantity = $2 WHERE sales_order_item_id = $1`,
				l.LineID, fulfilled); err != nil {
				return fmt.Errorf("carry fulfilled quantity to line: %w", err)
			}
		}
	}
	return nil
}

func minFloat(a, b float64) float64 {
	if a < b {
		return a
	}
	return b
}
