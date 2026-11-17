package inventory

// unit_so_hold.go — holding slabs for one sales order from its Material drawing.
//
// The third kind of hold beside a customer's (unit_reservation.go) and a
// fabrication job's. The slab goes to status 'reserved' and remembers the order
// (inventory_slab.reserved_sales_order_id). Stock is not touched — a held slab is
// still on hand — and nothing else that allocates stone will take it while held.
//
// The hold is handed over, not released, when the order's fabrication job
// allocates the slab: the job's own fabrication_job_slab row owns it from then on
// and the column is cleared (see fabrication/allocation.go). It is released when
// the drawing drops the slab, the order is cancelled or deleted, or an inventory
// user releases it from the slab page.

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// OrderSlabGroup is one slab-tracked item on a sales order with the units of that
// item the order's drawing can use: slabs free to take, and the ones the order
// already holds or has had a job allocate.
type OrderSlabGroup struct {
	ItemID   string `json:"itemId"`
	ItemName string `json:"itemName"`
	ItemSKU  string `json:"itemSku"`
	Units    []Unit `json:"units"`
}

// salesOrderRow is what a hold needs to know about the order.
type salesOrderRow struct {
	id     int
	number string
	status string
}

// salesOrderClosedForHolds are the order statuses that take no new holds.
var salesOrderClosedForHolds = map[string]string{
	"CANC": "This sales order is cancelled, so no slab can be held for it.",
	"FILL": "This sales order is filled, so no more slabs can be held for it.",
}

func loadSalesOrderForHold(ctx context.Context, q pgxQuerier, soUUID string) (salesOrderRow, error) {
	var o salesOrderRow
	err := q.QueryRow(ctx, `
		SELECT so.sales_order_id, COALESCE(so.sales_order_number,''), rs.record_status_code
		FROM sales_order so JOIN lkp_record_status rs ON rs.record_status_id = so.sales_order_status
		WHERE so.sales_order_uuid = $1 AND so.sales_order_deleted_at IS NULL`, soUUID).Scan(&o.id, &o.number, &o.status)
	if errors.Is(err, pgx.ErrNoRows) || isInvalidTextRepresentation(err) {
		return salesOrderRow{}, ErrNotFound
	}
	if err != nil {
		return salesOrderRow{}, fmt.Errorf("load sales order for slab hold: %w", err)
	}
	return o, nil
}

// ListUnitsForSalesOrder returns, for each slab-tracked catalogue item on the
// order's live lines, the units its drawing can see: available slabs, plus every
// unit already held for the order or allocated to one of its fabrication jobs.
//
// A slab still awaiting inspection is listed (so stock that is plainly in the yard
// does not seem to be missing) but cannot be held: HoldUnitForSalesOrder refuses it,
// and the screen shows it as waiting. A rejected slab is not offered at all.
func ListUnitsForSalesOrder(ctx context.Context, pool *pgxpool.Pool, soUUID string) ([]OrderSlabGroup, error) {
	order, err := loadSalesOrderForHold(ctx, pool, soUUID)
	if err != nil {
		return nil, err
	}
	rows, err := pool.Query(ctx, `
		SELECT DISTINCT ii.inventory_item_id, ii.inventory_item_uuid, ii.inventory_item_name, ii.inventory_item_sku
		FROM sales_order_item soi
		JOIN inventory_item ii ON ii.inventory_item_id = soi.inventory_item_id AND ii.inventory_item_deleted_at IS NULL
		WHERE soi.sales_order_id = $1 AND soi.item_deleted_at IS NULL AND ii.inventory_item_tracking = $2
		ORDER BY ii.inventory_item_name, ii.inventory_item_id`, order.id, TrackingSerialized)
	if err != nil {
		return nil, fmt.Errorf("list slab items on sales order: %w", err)
	}
	var (
		groups  []OrderSlabGroup
		itemIDs []int
		byItem  = map[string]int{}
	)
	for rows.Next() {
		var id int
		var g OrderSlabGroup
		if err := rows.Scan(&id, &g.ItemID, &g.ItemName, &g.ItemSKU); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan slab item on sales order: %w", err)
		}
		g.Units = []Unit{}
		byItem[g.ItemID] = len(groups)
		groups = append(groups, g)
		itemIDs = append(itemIDs, id)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list slab items on sales order: %w", err)
	}
	if len(itemIDs) == 0 {
		return []OrderSlabGroup{}, nil
	}

	urows, err := pool.Query(ctx, unitSelect+`
		WHERE s.inventory_item_id = ANY($1) AND s.slab_deleted_at IS NULL
		  AND ( (s.slab_status = 'available' AND s.inspection_status IN ('accepted','legacy','pending'))
		     OR (s.slab_status = 'reserved' AND s.reserved_sales_order_id = $2)
		     OR (s.slab_status IN ('reserved','consumed') AND fj.sales_order_id = $2) )
		ORDER BY s.slab_serial, s.inventory_slab_id`, itemIDs, order.id)
	if err != nil {
		return nil, fmt.Errorf("list slabs for sales order: %w", err)
	}
	defer urows.Close()
	for urows.Next() {
		u, err := scanUnit(urows)
		if err != nil {
			return nil, fmt.Errorf("scan slab for sales order: %w", err)
		}
		if i, ok := byItem[u.InventoryItemID]; ok {
			groups[i].Units = append(groups[i].Units, *u)
		}
	}
	if err := urows.Err(); err != nil {
		return nil, fmt.Errorf("list slabs for sales order: %w", err)
	}
	return groups, nil
}

// HoldUnitForSalesOrder holds one available slab for a sales order. Holding a slab
// the order already holds is a no-op, so a double-clicked Add is harmless.
func HoldUnitForSalesOrder(ctx context.Context, pool *pgxpool.Pool, soUUID, unitUUID string, actorEmployeeID int) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin hold unit: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	order, err := loadSalesOrderForHold(ctx, tx, soUUID)
	if err != nil {
		return err
	}
	if msg, closed := salesOrderClosedForHolds[order.status]; closed {
		return ClientError{Msg: msg}
	}
	u, err := unitByUUID(ctx, tx, unitUUID, true)
	if err != nil {
		return err
	}

	var heldFor *int
	if err := tx.QueryRow(ctx, `SELECT reserved_sales_order_id FROM inventory_slab WHERE inventory_slab_id = $1`, u.id).Scan(&heldFor); err != nil {
		return fmt.Errorf("read slab hold: %w", err)
	}
	if u.status == StatusReserved && heldFor != nil && *heldFor == order.id {
		return nil
	}
	switch u.status {
	case StatusAvailable:
	case StatusReserved:
		return ClientError{Msg: "This slab is already reserved for something else."}
	case StatusReceived:
		return ClientError{Msg: "This slab has not been put in a bin yet, so it cannot be held."}
	case StatusOnOrder:
		return ClientError{Msg: "This slab is still on order and has not arrived."}
	case StatusInTransit:
		return ClientError{Msg: "This slab is in transit. Receive its transfer first."}
	default:
		return ClientError{Msg: "A consumed or scrapped slab cannot be held."}
	}
	if err := RequireInspectedTx(ctx, tx, unitUUID); err != nil {
		return err
	}

	// A slab of another material is no use to this order's drawing.
	var onOrder bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM sales_order_item soi
			WHERE soi.sales_order_id = $1 AND soi.item_deleted_at IS NULL AND soi.inventory_item_id = $2)`,
		order.id, u.itemID).Scan(&onOrder); err != nil {
		return fmt.Errorf("check slab material against order: %w", err)
	}
	if !onOrder {
		return ClientError{Msg: "That slab is a different material from the ones on this sales order."}
	}

	if _, err := tx.Exec(ctx, `
		UPDATE inventory_slab
		SET slab_status = $2, reserved_sales_order_id = $3, reserved_at = NOW(), reserved_by = $4,
		    slab_updated_at = NOW(), slab_updated_by = $4
		WHERE inventory_slab_id = $1`,
		u.id, StatusReserved, order.id, nullableInt(actorEmployeeID)); err != nil {
		return mapUnitWriteErr(err, "hold")
	}
	if err := writeUnitHistory(ctx, tx, u.id, "status_change", "status", StatusAvailable, StatusReserved,
		u.binID, u.binID, nil, "Held for "+orderLabel(order)+" from its drawing.", actorEmployeeID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit hold unit: %w", err)
	}
	return nil
}

// ReleaseUnitFromSalesOrder frees a slab the order holds. A slab the order's job
// has taken over is the job's to release, and a slab already free is left alone.
func ReleaseUnitFromSalesOrder(ctx context.Context, pool *pgxpool.Pool, soUUID, unitUUID string, actorEmployeeID int) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin release hold: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	order, err := loadSalesOrderForHold(ctx, tx, soUUID)
	if err != nil {
		return err
	}
	u, err := unitByUUID(ctx, tx, unitUUID, true)
	if err != nil {
		return err
	}
	var heldFor *int
	if err := tx.QueryRow(ctx, `SELECT reserved_sales_order_id FROM inventory_slab WHERE inventory_slab_id = $1`, u.id).Scan(&heldFor); err != nil {
		return fmt.Errorf("read slab hold: %w", err)
	}
	if u.status != StatusReserved || heldFor == nil || *heldFor != order.id {
		if u.status == StatusAvailable {
			return nil
		}
		return ClientError{Msg: "This slab is not held by this sales order. If a fabrication job has it, release it from the job."}
	}
	if err := freeSalesOrderHoldTx(ctx, tx, u.id, u.binID, "Released from "+orderLabel(order)+".", actorEmployeeID); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit release hold: %w", err)
	}
	return nil
}

// ReleaseSalesOrderHoldsTx frees every slab still held for an order — called when
// the order is cancelled or deleted. Slabs a job has taken over are not touched.
func ReleaseSalesOrderHoldsTx(ctx context.Context, tx pgx.Tx, orderID int, actorEmployeeID int) error {
	var number string
	if err := tx.QueryRow(ctx, `SELECT COALESCE(sales_order_number,'') FROM sales_order WHERE sales_order_id = $1`, orderID).Scan(&number); err != nil {
		return fmt.Errorf("read sales order for hold release: %w", err)
	}
	rows, err := tx.Query(ctx, `
		SELECT inventory_slab_id, inventory_bin_id FROM inventory_slab
		WHERE reserved_sales_order_id = $1 AND slab_status = 'reserved' AND slab_deleted_at IS NULL
		FOR UPDATE`, orderID)
	if err != nil {
		return fmt.Errorf("list slabs held for sales order: %w", err)
	}
	type held struct {
		id  int
		bin *int
	}
	var all []held
	for rows.Next() {
		var h held
		if err := rows.Scan(&h.id, &h.bin); err != nil {
			rows.Close()
			return fmt.Errorf("scan slab held for sales order: %w", err)
		}
		all = append(all, h)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("list slabs held for sales order: %w", err)
	}
	label := orderLabel(salesOrderRow{id: orderID, number: number})
	for _, h := range all {
		if err := freeSalesOrderHoldTx(ctx, tx, h.id, h.bin, "Released: "+label+" was cancelled or deleted.", actorEmployeeID); err != nil {
			return err
		}
	}
	return nil
}

// freeSalesOrderHoldTx puts a held slab back to available and writes its history.
func freeSalesOrderHoldTx(ctx context.Context, tx pgx.Tx, unitID int, binID *int, note string, actorEmployeeID int) error {
	if _, err := tx.Exec(ctx, `
		UPDATE inventory_slab
		SET slab_status = $2, reserved_sales_order_id = NULL, reserved_at = NULL, reserved_by = NULL,
		    slab_updated_at = NOW(), slab_updated_by = $3
		WHERE inventory_slab_id = $1`, unitID, StatusAvailable, nullableInt(actorEmployeeID)); err != nil {
		return mapUnitWriteErr(err, "release")
	}
	return writeUnitHistory(ctx, tx, unitID, "status_change", "status", StatusReserved, StatusAvailable,
		binID, binID, nil, note, actorEmployeeID)
}

func orderLabel(o salesOrderRow) string {
	if n := strings.TrimSpace(o.number); n != "" {
		return "sales order " + n
	}
	return "a sales order"
}
