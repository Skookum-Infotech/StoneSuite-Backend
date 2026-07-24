// itemreceipt/store_void.go — the reversal of Post.
package itemreceipt

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/purchaseorder"
)

// Void reverses a receipt: it gives back the quantity it took from the ordered
// lines, writes compensating inventory ledger rows, and re-runs the purchase
// order rollup so a header that claimed RCVD can fall back to PART or SENT.
//
// A receipt that was never posted (PEND) can also be voided — there is nothing
// to reverse, so this is just a status change recording that the paperwork was
// abandoned.
//
// Voiding is how a posted receipt gets corrected (AD-5): the document is
// immutable once its quantities have moved, so the fix is to void and reissue
// rather than edit in place, which keeps the ledger a true history of what was
// believed when.
func Void(ctx context.Context, pool *pgxpool.Pool, uuid string, in VoidInput, actorEmployeeID int) (*ItemReceipt, error) {
	if strings.TrimSpace(in.VoidReason) == "" {
		return nil, ClientError{Msg: "A void reason is required."}
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin void item receipt: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Same lock order as Post: receipt, then order.
	var irInternalID, curStatusID, poInternalID, warehouseID int
	var curStatusCode string
	err = tx.QueryRow(ctx, `
		SELECT ir.item_receipt_id, ir.item_receipt_status, rs.record_status_code,
		       ir.purchase_order_id, ir.warehouse_id
		FROM item_receipt ir
		JOIN lkp_record_status rs ON rs.record_status_id = ir.item_receipt_status
		WHERE ir.item_receipt_uuid = $1 AND ir.item_receipt_deleted_at IS NULL
		FOR UPDATE OF ir`, uuid,
	).Scan(&irInternalID, &curStatusID, &curStatusCode, &poInternalID, &warehouseID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load item receipt for void: %w", err)
	}
	if err := ValidateTransition(curStatusCode, voidStatusCode); err != nil {
		return nil, err
	}

	wasPosted := IsPosted(curStatusCode)
	if wasPosted {
		// QueryRow, not Exec: Exec reports no error when the SELECT matches
		// nothing, so a deleted order would leave us reversing quantities
		// against a row we never locked. Scanning forces the miss to surface.
		var locked int
		err := tx.QueryRow(ctx, `
			SELECT 1 FROM purchase_order
			WHERE purchase_order_id = $1 AND purchase_order_deleted_at IS NULL
			FOR UPDATE`, poInternalID).Scan(&locked)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ClientError{Msg: "The purchase order this receipt belongs to no longer exists."}
		}
		if err != nil {
			return nil, fmt.Errorf("lock purchase order for void: %w", err)
		}
		if err := reverseLines(ctx, tx, irInternalID, warehouseID, actorEmployeeID); err != nil {
			return nil, err
		}
	}

	recordTypeID, err := recordTypeIDByCode(ctx, tx, irctRecordTypeCode)
	if err != nil {
		return nil, fmt.Errorf("resolve IRCT record type: %w", err)
	}
	voidStatusID, err := statusIDByCode(ctx, tx, recordTypeID, voidStatusCode)
	if err != nil {
		return nil, fmt.Errorf("resolve VOID status: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE item_receipt SET
			item_receipt_status = $2,
			item_receipt_voided_at = NOW(),
			item_receipt_voided_by = $3,
			item_receipt_void_reason = $4,
			item_receipt_updated_at = NOW(),
			item_receipt_updated_by = $3,
			item_receipt_record_version = item_receipt_record_version + 1
		WHERE item_receipt_id = $1`,
		irInternalID, voidStatusID, nullableInt(actorEmployeeID), in.VoidReason); err != nil {
		return nil, fmt.Errorf("void item receipt: %w", err)
	}
	writeHistory(ctx, tx, irInternalID, "void", &curStatusID, &voidStatusID, actorEmployeeID)

	if wasPosted {
		if _, err := purchaseorder.ApplyReceiptRollup(ctx, tx, poInternalID, actorEmployeeID); err != nil {
			return nil, fmt.Errorf("roll back purchase order status: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit void item receipt: %w", err)
	}
	return Get(ctx, pool, uuid)
}

// reverseLines undoes a posting's quantity and stock effects. It mirrors the
// forward loop in Post exactly — same lines, same accepted quantities, opposite
// sign — so the two can only ever disagree if this loop is edited alone.
func reverseLines(ctx context.Context, tx pgx.Tx, irInternalID, warehouseID, actorEmployeeID int) error {
	rows, err := tx.Query(ctx, `
		SELECT irl.item_receipt_line_id, irl.purchase_order_item_id, irl.inventory_item_id,
		       irl.qty_received - irl.qty_rejected
		FROM item_receipt_line irl
		JOIN purchase_order_item poi ON poi.purchase_order_item_id = irl.purchase_order_item_id
		WHERE irl.item_receipt_id = $1 AND irl.item_deleted_at IS NULL
		ORDER BY irl.line_number
		FOR UPDATE OF poi`, irInternalID)
	if err != nil {
		return fmt.Errorf("load receipt lines for void: %w", err)
	}
	type rev struct {
		lineID   int
		poItemID int
		itemID   *int
		accepted float64
	}
	var lines []rev
	for rows.Next() {
		var r rev
		if err := rows.Scan(&r.lineID, &r.poItemID, &r.itemID, &r.accepted); err != nil {
			rows.Close()
			return fmt.Errorf("scan receipt line for void: %w", err)
		}
		lines = append(lines, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("load receipt lines for void: %w", err)
	}

	recordTypeID, err := recordTypeIDByCode(ctx, tx, irctRecordTypeCode)
	if err != nil {
		return fmt.Errorf("resolve IRCT record type: %w", err)
	}

	for _, l := range lines {
		if l.accepted <= 0 {
			continue
		}
		// GREATEST guards the ordered line's own non-negativity CHECK: if the
		// order was amended down after this receipt posted, giving back the
		// full quantity could otherwise drive qty_received below zero.
		if _, err := tx.Exec(ctx, `
			UPDATE purchase_order_item
			SET qty_received = GREATEST(qty_received - $2, 0),
			    item_updated_at = NOW(),
			    item_record_version = item_record_version + 1
			WHERE purchase_order_item_id = $1`, l.poItemID, l.accepted); err != nil {
			return fmt.Errorf("reverse ordered line received quantity: %w", err)
		}
		if l.itemID == nil {
			continue
		}
		if err := ledgerAndStock(ctx, tx, *l.itemID, warehouseID,
			ledgerEventReturned, -l.accepted,
			recordTypeID, irInternalID, l.lineID, actorEmployeeID); err != nil {
			return err
		}
	}
	return nil
}
