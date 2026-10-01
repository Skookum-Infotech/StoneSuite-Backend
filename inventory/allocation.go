package inventory

// allocation.go — reserving stock for sales orders.
//
// A sales order promises stock to a customer. Without holding that stock back,
// two orders can promise the same stone and the shortfall only surfaces on the
// shop floor. So saving an order checks what is free and reserves what it needs
// (inventory_allocation, one open row per order line), and the reservation ends
// one of three ways: the order is cancelled or deleted (released), the stone is
// cut for it (fulfilled, by ApplyConsumption), or the order is filled
// (FulfillOrder).
//
//	free stock  = on hand, across warehouses
//	              - what every OTHER order still holds  (allocated - fulfilled)
//
// A line's reservation counts only what has not yet been fulfilled: stone that
// has already been cut for the order has left on-hand, so counting it again in
// the hold would hide stock that is really there.
//
// This lives in the inventory package rather than salesorder because the
// reservation is shared inventory state — the fabrication module also writes to
// it as it consumes slabs — and inventory imports neither of them.

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
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

// Shortage is one item an order asks for more of than is free.
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

// StockShortageError is returned when an order asks for more than is free. It is
// not a ClientError: the controller answers it with a structured 409 the screen
// turns into a "not enough stock" dialog, not a bare message.
type StockShortageError struct {
	Shortages []Shortage
}

func (e *StockShortageError) Error() string {
	parts := make([]string, 0, len(e.Shortages))
	for _, s := range e.Shortages {
		parts = append(parts, fmt.Sprintf("%s (need %s, %s available)", s.SKU, trimQty(s.Requested), trimQty(s.Available)))
	}
	return "Not enough stock for: " + strings.Join(parts, "; ") + "."
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
		SELECT inventory_item_id, COALESCE(SUM(quantity_on_hand), 0)
		FROM inventory_stock WHERE inventory_item_id = ANY($1) GROUP BY inventory_item_id`, ids)
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

// ReserveOrder checks that the order's lines can be covered by free stock and,
// if so, replaces the order's reservation with one matching those lines. If any
// line cannot be covered it returns a *StockShortageError naming every short
// item and leaves the transaction to be rolled back by the caller.
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

	requested := map[int]float64{}
	var counted []int
	for _, id := range ids {
		if items[id].counted() {
			counted = append(counted, id)
		}
	}
	for _, l := range lines {
		if items[l.ItemID].counted() {
			requested[l.ItemID] += l.Quantity
		}
	}
	if len(counted) == 0 {
		return nil
	}

	onHand, held, err := stockPosition(ctx, tx, counted)
	if err != nil {
		return err
	}
	var short []Shortage
	for _, id := range counted {
		need := requested[id] - carried[id]
		if need < 0 {
			need = 0
		}
		free := onHand[id] - held[id]
		if free < 0 {
			free = 0
		}
		if need > free+stockEpsilon {
			it := items[id]
			short = append(short, Shortage{
				ItemID: it.uuid, SKU: it.sku, Name: it.name, UnitCode: it.unitCode,
				Requested: roundTo(need, areaScale), Available: roundTo(free, areaScale),
				Short: roundTo(need-free, areaScale),
			})
		}
	}
	if len(short) > 0 {
		return &StockShortageError{Shortages: short}
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

// ApplyConsumption records that area of an item has been cut for the order: it
// shrinks the order's hold by that much, so the stone — which has now left
// on-hand — is not counted twice against everyone else's free stock. It is
// spread over the order's lines for the item in line order and never past what a
// line asked for, so cutting more than was ordered does not overshoot.
func ApplyConsumption(ctx context.Context, tx pgx.Tx, orderID, itemID int, area float64) error {
	if area <= stockEpsilon {
		return nil
	}
	rows, err := tx.Query(ctx, `
		SELECT inventory_allocation_id, sales_order_item_id, allocated_quantity, fulfilled_quantity
		FROM inventory_allocation
		WHERE sales_order_id = $1 AND inventory_item_id = $2 AND allocation_status IN `+openAllocation+`
		ORDER BY sales_order_item_id
		FOR UPDATE`, orderID, itemID)
	if err != nil {
		return fmt.Errorf("load reservations to consume: %w", err)
	}
	type held struct {
		id, lineID           int
		allocated, fulfilled float64
	}
	var open []held
	for rows.Next() {
		var h held
		if err := rows.Scan(&h.id, &h.lineID, &h.allocated, &h.fulfilled); err != nil {
			rows.Close()
			return fmt.Errorf("scan reservation to consume: %w", err)
		}
		open = append(open, h)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	left := area
	for _, h := range open {
		room := h.allocated - h.fulfilled
		take := minFloat(room, left)
		if take <= stockEpsilon {
			continue
		}
		newFulfilled := h.fulfilled + take
		status := "partially_fulfilled"
		if newFulfilled >= h.allocated-stockEpsilon {
			newFulfilled, status = h.allocated, "fulfilled"
		}
		newFulfilled = roundTo(newFulfilled, areaScale)
		if _, err := tx.Exec(ctx, `
			UPDATE inventory_allocation
			SET fulfilled_quantity = $2, allocation_status = $3, allocation_updated_at = NOW()
			WHERE inventory_allocation_id = $1`, h.id, newFulfilled, status); err != nil {
			return fmt.Errorf("apply consumption to reservation: %w", err)
		}
		if _, err := tx.Exec(ctx,
			`UPDATE sales_order_item SET line_fulfilled_quantity = $2 WHERE sales_order_item_id = $1`,
			h.lineID, newFulfilled); err != nil {
			return fmt.Errorf("apply consumption to line: %w", err)
		}
		left -= take
	}
	return nil
}

// FulfillOrder closes the order's reservations as it is filled. A
// quantity-tracked line is taken out of stock now — nothing else deducts them,
// so without this an item could be sold again and again against the same on-hand.
// A slab-tracked line is not deducted here: its stone leaves stock when a
// fabrication job cuts it (ApplyConsumption), so filling only ends the hold.
//
// It works from the order's own reservations, so an order saved before
// reservations existed has none and is left alone, rather than failing to fill
// for want of stock it never claimed.
func FulfillOrder(ctx context.Context, tx pgx.Tx, orderID, actorEmployeeID int) error {
	rows, err := tx.Query(ctx, `
		SELECT a.inventory_allocation_id, a.inventory_item_id, a.sales_order_item_id,
		       a.allocated_quantity, a.fulfilled_quantity,
		       ii.inventory_item_tracking, ii.inventory_item_sku
		FROM inventory_allocation a
		JOIN inventory_item ii ON ii.inventory_item_id = a.inventory_item_id
		WHERE a.sales_order_id = $1 AND a.allocation_status IN `+openAllocation+`
		ORDER BY a.sales_order_item_id
		FOR UPDATE OF a`, orderID)
	if err != nil {
		return fmt.Errorf("load reservations to fill: %w", err)
	}
	type open struct {
		id, itemID, lineID   int
		allocated, fulfilled float64
		tracking, sku        string
	}
	var held []open
	for rows.Next() {
		var o open
		if err := rows.Scan(&o.id, &o.itemID, &o.lineID, &o.allocated, &o.fulfilled, &o.tracking, &o.sku); err != nil {
			rows.Close()
			return fmt.Errorf("scan reservation to fill: %w", err)
		}
		held = append(held, o)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(held) == 0 {
		return nil
	}

	var recordTypeID int
	if err := tx.QueryRow(ctx,
		`SELECT record_type_id FROM lkp_record_type WHERE record_type_code = $1`, sordRecordTypeCode).Scan(&recordTypeID); err != nil {
		return fmt.Errorf("resolve SORD record type: %w", err)
	}

	for _, o := range held {
		if o.tracking == TrackingQuantity {
			if remaining := o.allocated - o.fulfilled; remaining > stockEpsilon {
				if err := deductBulk(ctx, tx, o.itemID, o.sku, remaining, recordTypeID, orderID, o.lineID, actorEmployeeID); err != nil {
					return err
				}
			}
			if _, err := tx.Exec(ctx,
				`UPDATE sales_order_item SET line_fulfilled_quantity = $2 WHERE sales_order_item_id = $1`,
				o.lineID, o.allocated); err != nil {
				return fmt.Errorf("mark line fulfilled: %w", err)
			}
			o.fulfilled = o.allocated
		}
		if _, err := tx.Exec(ctx, `
			UPDATE inventory_allocation
			SET allocation_status = 'fulfilled', fulfilled_quantity = $2, allocation_updated_at = NOW()
			WHERE inventory_allocation_id = $1`, o.id, o.fulfilled); err != nil {
			return fmt.Errorf("close reservation: %w", err)
		}
	}
	return nil
}

// deductBulk takes qty of a quantity-tracked item out of stock for a filled
// order line, drawing from the warehouses that hold it, fullest first. It is
// refused, naming the item, if the stock is not there: something (an adjustment,
// a count) has removed what the order had reserved.
func deductBulk(ctx context.Context, tx pgx.Tx, itemID int, sku string, qty float64,
	recordTypeID, orderID, lineID, actorEmployeeID int) error {
	rows, err := tx.Query(ctx, `
		SELECT warehouse_id, quantity_on_hand FROM inventory_stock
		WHERE inventory_item_id = $1 AND quantity_on_hand > 0
		ORDER BY quantity_on_hand DESC, warehouse_id
		FOR UPDATE`, itemID)
	if err != nil {
		return fmt.Errorf("load stock to deduct: %w", err)
	}
	type bin struct {
		warehouseID int
		onHand      float64
	}
	var bins []bin
	for rows.Next() {
		var b bin
		if err := rows.Scan(&b.warehouseID, &b.onHand); err != nil {
			rows.Close()
			return fmt.Errorf("scan stock to deduct: %w", err)
		}
		bins = append(bins, b)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}

	left := qty
	for _, b := range bins {
		if left <= stockEpsilon {
			break
		}
		take := roundTo(minFloat(left, b.onHand), areaScale)
		if err := LedgerAndStock(ctx, tx, itemID, b.warehouseID, EventConsumed, -take,
			recordTypeID, orderID, lineID, actorEmployeeID); err != nil {
			return err
		}
		left -= take
	}
	if left > stockEpsilon {
		return ClientError{Msg: fmt.Sprintf(
			"Cannot fill this order: %s needs %s more than is on hand. Adjust or receive stock first.", sku, trimQty(left))}
	}
	return nil
}
