package inventory

// ledger.go — the two stock-movement writers and the stock arithmetic they share.
//
// Before this file, near-identical copies of "write a ledger row and apply the
// delta to inventory_stock" lived privately in itemreceipt/inventory_post.go
// and fabrication/allocation.go. Both maintain the same invariant:
//
//	inventory_stock.quantity_on_hand = SUM(quantity_delta)
//	                                   per (inventory_item_id, warehouse_id)
//
// They are NOT interchangeable, and collapsing them into one function would be
// a bug. They differ in exactly one deliberate way — what a duplicate event
// means:
//
//   - Bulk (LedgerAndStock, inventory_ledger): a duplicate is the caller
//     re-posting a document. That is an ERROR the user must see, so it returns
//     ErrMovementAlreadyApplied and the receipt is refused.
//   - Serialized (SlabLedgerAndStock, inventory_slab_ledger): a duplicate is a
//     re-run of an idempotent state transition (consuming an
//     already-consumed slab). That is a NO-OP, because the partial unique
//     indexes per slab per event already make the stock effect once-only.
//
// What they genuinely share is the stock half, which is applyStockDelta.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Ledger events. Both ledgers constrain these with a CHECK; 'adjusted' is legal
// on each and is written by nothing yet — the future stock-adjustment document
// is its intended writer.
const (
	EventReceived  = "received"
	EventReturned  = "returned"
	EventConsumed  = "consumed"
	EventRecovered = "recovered"
	EventScrapped  = "scrapped"
	EventAdjusted  = "adjusted"
)

// Sentinels returned by applyStockDelta so each caller can attach its own
// user-facing wording. The bulk and serialized paths reach a negative balance
// for genuinely different reasons and their messages should say so.
var (
	// errStockWouldGoNegative signals chk_inventory_stock_on_hand rejected the
	// update. This CHECK is the real guard against negative stock, so it must
	// always surface as a 400 and never as a raw 500.
	errStockWouldGoNegative = errors.New("stock would go negative")
	// errNoStockRow signals a negative delta against an item/warehouse pair
	// that holds no stock row at all — the ledger and stock have diverged.
	errNoStockRow = errors.New("no stock row for this item and warehouse")
)

// applyStockDelta applies a signed delta to inventory_stock, creating the row
// if this item has never been held at this warehouse.
//
// The UPDATE-then-INSERT split is load-bearing and must not be collapsed into a
// single INSERT ... ON CONFLICT DO UPDATE: PostgreSQL evaluates CHECK
// constraints on the proposed insert row BEFORE it detects the conflict, so a
// reversal (negative delta) trips chk_inventory_stock_on_hand on the way in and
// never reaches the UPDATE branch. Both original copies split it for this
// reason, and both said so.
func applyStockDelta(ctx context.Context, tx pgx.Tx, itemID, warehouseID int, delta float64) error {
	tag, err := tx.Exec(ctx, `
		UPDATE inventory_stock
		SET quantity_on_hand = quantity_on_hand + $3,
		    stock_updated_at = NOW(),
		    stock_record_version = stock_record_version + 1
		WHERE inventory_item_id = $1 AND warehouse_id = $2`, itemID, warehouseID, delta)
	if err != nil {
		if isCheckViolation(err) {
			return errStockWouldGoNegative
		}
		return fmt.Errorf("apply inventory stock delta: %w", err)
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	// No stock row yet. Only a positive delta can create one; a reversal against
	// a nonexistent row means the ledger and stock have diverged.
	if delta < 0 {
		return errNoStockRow
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO inventory_stock (inventory_item_id, warehouse_id, quantity_on_hand)
		VALUES ($1,$2,$3)
		ON CONFLICT (inventory_item_id, warehouse_id) DO UPDATE
		SET quantity_on_hand = inventory_stock.quantity_on_hand + $3,
		    stock_updated_at = NOW()`,
		itemID, warehouseID, delta,
	); err != nil {
		return fmt.Errorf("seed inventory stock row: %w", err)
	}
	return nil
}

// LedgerAndStock appends one inventory_ledger row for a NON-serialized movement
// and applies the same signed delta to inventory_stock.
//
// The ledger insert is deliberately first: inventory_ledger carries partial
// unique indexes on (source_record_type, source_line_id) per event, so a
// re-posted document trips a unique violation here *before* any stock is
// touched. Double-counting is prevented by the schema, not by a check that
// could be forgotten.
//
// Returns ErrMovementAlreadyApplied when this exact source line has already
// posted this event.
func LedgerAndStock(
	ctx context.Context, tx pgx.Tx,
	itemID, warehouseID int, event string, delta float64,
	sourceRecordTypeID, sourceRecordID, sourceLineID, actorEmployeeID int,
) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO inventory_ledger (
			inventory_item_id, warehouse_id, event, quantity_delta,
			source_record_type, source_record_id, source_line_id, actor_employee_id
		) VALUES ($1,$2,$3,$4, $5,$6,$7,$8)`,
		itemID, warehouseID, event, delta,
		nullableInt(sourceRecordTypeID), nullableInt(sourceRecordID), nullableInt(sourceLineID),
		nullableInt(actorEmployeeID),
	); err != nil {
		if isUniqueViolation(err) {
			return ErrMovementAlreadyApplied
		}
		return fmt.Errorf("insert inventory ledger row: %w", err)
	}

	switch err := applyStockDelta(ctx, tx, itemID, warehouseID, delta); {
	case errors.Is(err, errStockWouldGoNegative):
		return ClientError{Msg: "This would drive stock below zero — the received goods have already been used or shipped."}
	case errors.Is(err, errNoStockRow):
		return ClientError{Msg: "No stock on hand for this item at the receiving warehouse."}
	case err != nil:
		return err
	}
	return nil
}

// SlabLedgerAndStock appends one inventory_slab_ledger row for a SERIALIZED
// movement and applies the same signed delta to inventory_stock.
//
// Unlike LedgerAndStock, a duplicate event is a NO-OP rather than an error: the
// partial unique indexes (one 'received', one 'consumed', one 'scrapped' per
// slab) already make each stock effect once-only, so re-running an idempotent
// transition must succeed quietly instead of failing the caller's job.
func SlabLedgerAndStock(
	ctx context.Context, tx pgx.Tx,
	slabID, itemID, warehouseID int, event string, delta float64,
	fabricationJobSlabID *int, actorEmployeeID int,
) error {
	if _, err := tx.Exec(ctx, `
		INSERT INTO inventory_slab_ledger (
			inventory_slab_id, inventory_item_id, warehouse_id, event,
			quantity_delta, fabrication_job_slab_id, actor_employee_id
		) VALUES ($1,$2,$3,$4,$5,$6,$7)`,
		slabID, itemID, warehouseID, event, delta,
		fabricationJobSlabID, nullableInt(actorEmployeeID),
	); err != nil {
		if isUniqueViolation(err) {
			// Already applied. Returning here rather than falling through is
			// what keeps the stock delta from being applied twice.
			return nil
		}
		return fmt.Errorf("insert slab ledger row: %w", err)
	}

	switch err := applyStockDelta(ctx, tx, itemID, warehouseID, delta); {
	case errors.Is(err, errStockWouldGoNegative):
		return ClientError{Msg: "This action would drive stock below zero; the reservation math is inconsistent."}
	case errors.Is(err, errNoStockRow):
		return ClientError{Msg: "No stock on hand for this item at its warehouse."}
	case err != nil:
		return err
	}
	return nil
}
