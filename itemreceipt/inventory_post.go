package itemreceipt

// inventory_post.go — the only place this module touches stock.
//
// Modelled on fabrication/allocation.go's ledgerAndStock, which maintains the
// same invariant for serialized slabs:
//
//	inventory_stock.quantity_on_hand = SUM(inventory_ledger.quantity_delta)
//	                                   per (inventory_item_id, warehouse_id)
//
// Both writes happen in the caller's transaction, so the ledger and the
// running total can never disagree.

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// itemReceiptRecordTypeID caches nothing — it is resolved per call by the
// caller and passed in, because record type ids are per-tenant-database.

// ledgerAndStock appends one inventory_ledger row and applies the same signed
// delta to inventory_stock, creating the stock row if this item has never been
// held at this warehouse.
//
// The ledger insert is deliberately first: inventory_ledger carries partial
// unique indexes on (source_line_id) per event, so a re-posted receipt trips a
// unique violation here *before* any stock is touched. Double-counting is
// prevented by the schema, not by a check that could be forgotten.
func ledgerAndStock(
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

	// Update first, insert only if there is no row yet.
	//
	// This cannot be collapsed into a single INSERT ... ON CONFLICT DO UPDATE:
	// PostgreSQL evaluates CHECK constraints on the proposed insert row before
	// it detects the conflict, so a reversal (negative delta) would trip
	// chk_inventory_stock_on_hand on the way in and never reach the UPDATE
	// branch. fabrication/allocation.go:229 splits it for the same reason.
	tag, err := tx.Exec(ctx, `
		UPDATE inventory_stock
		SET quantity_on_hand = quantity_on_hand + $3,
		    stock_updated_at = NOW(),
		    stock_record_version = stock_record_version + 1
		WHERE inventory_item_id = $1 AND warehouse_id = $2`, itemID, warehouseID, delta)
	if err != nil {
		if isCheckViolation(err) {
			// quantity_on_hand >= 0. Reaching here means the goods were already
			// consumed downstream, so there is nothing left to give back.
			return ClientError{Msg: "This would drive stock below zero — the received goods have already been used or shipped."}
		}
		return fmt.Errorf("apply inventory stock delta: %w", err)
	}
	if tag.RowsAffected() == 0 {
		// No stock row yet. Only a positive delta can create one; a reversal
		// against a nonexistent row means the ledger and stock have diverged.
		if delta < 0 {
			return ClientError{Msg: "No stock on hand for this item at the receiving warehouse."}
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
	}
	return nil
}
