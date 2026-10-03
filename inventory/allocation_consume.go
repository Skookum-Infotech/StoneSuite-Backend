package inventory

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
)

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
