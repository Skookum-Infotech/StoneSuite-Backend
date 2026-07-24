// itemreceipt/store_post.go — the act that makes a receipt real.
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

// postLine is one line's contribution to a posting, after the ordered line has
// been re-read under a row lock.
type postLine struct {
	lineID          int
	lineNumber      int
	poItemID        int
	inventoryItemID *int
	ordered         float64
	alreadyRecv     float64
	// accepted is what actually counts: goods received minus goods rejected.
	// Rejected goods are refused at the dock and go back to the vendor, so they
	// neither enter stock nor satisfy the order — the line stays outstanding
	// for them, which is what lets a replacement shipment be received later.
	accepted float64
}

// loadPostLines re-reads this receipt's lines together with their ordered
// lines, locking the purchase_order_item rows so two receipts against the same
// order cannot both pass the tolerance check on stale quantities.
func loadPostLines(ctx context.Context, tx pgx.Tx, irInternalID int) ([]postLine, error) {
	rows, err := tx.Query(ctx, `
		SELECT irl.item_receipt_line_id, irl.line_number, irl.purchase_order_item_id,
		       irl.inventory_item_id,
		       poi.quantity, poi.qty_received,
		       irl.qty_received - irl.qty_rejected
		FROM item_receipt_line irl
		JOIN purchase_order_item poi ON poi.purchase_order_item_id = irl.purchase_order_item_id
		WHERE irl.item_receipt_id = $1 AND irl.item_deleted_at IS NULL
		ORDER BY irl.line_number
		FOR UPDATE OF poi`, irInternalID)
	if err != nil {
		return nil, fmt.Errorf("load receipt lines for posting: %w", err)
	}
	defer rows.Close()
	var out []postLine
	for rows.Next() {
		var l postLine
		if err := rows.Scan(&l.lineID, &l.lineNumber, &l.poItemID, &l.inventoryItemID,
			&l.ordered, &l.alreadyRecv, &l.accepted); err != nil {
			return nil, fmt.Errorf("scan receipt line for posting: %w", err)
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load receipt lines for posting: %w", err)
	}
	if len(out) == 0 {
		return nil, ClientError{Msg: "This receipt has no lines to post."}
	}
	return out, nil
}

// checkTolerance enforces AD-3 across every line. canApproveOverReceipt is the
// caller's item_receipt:approve grant, resolved by the controller — the store
// decides whether the override is *needed*, the controller decides whether the
// caller *has* it.
func checkTolerance(lines []postLine, canApproveOverReceipt bool, reason string) error {
	var over []string
	for _, l := range lines {
		if WithinTolerance(l.ordered, l.alreadyRecv, l.accepted) {
			continue
		}
		over = append(over, fmt.Sprintf(
			"line %d (ordered %g, already received %g, receiving %g)",
			l.lineNumber, l.ordered, l.alreadyRecv, l.accepted))
	}
	if len(over) == 0 {
		return nil
	}
	if !canApproveOverReceipt {
		return fmt.Errorf("%w: %s", ErrOverReceipt, strings.Join(over, "; "))
	}
	if strings.TrimSpace(reason) == "" {
		return ClientError{Msg: "An over-receipt reason is required when accepting more than the ordered quantity."}
	}
	return nil
}

// receiptStatusFor derives the posted receipt's own status from the lines it
// settled: RCVD when every ordered line it touched is now fully satisfied,
// PART otherwise. It reuses the purchase order module's rollup helper rather
// than restating the rule.
func receiptStatusFor(lines []postLine) string {
	lr := make([]purchaseorder.LineReceipt, 0, len(lines))
	for _, l := range lines {
		lr = append(lr, purchaseorder.LineReceipt{
			Quantity:    l.ordered,
			QtyReceived: l.alreadyRecv + l.accepted,
		})
	}
	if code := purchaseorder.RollupReceiptStatus(lr); code == receivedStatusCode {
		return receivedStatusCode
	}
	// "" (nothing accepted — every unit was rejected) is still a posted
	// receipt that settled nothing, which is exactly what PART means here.
	return partialStatusCode
}

// Post applies a pending receipt: it advances purchase_order_item.qty_received,
// moves stock through the inventory ledger, sets the receipt's own status, and
// rolls the purchase order header forward — all in one transaction.
//
// canApproveOverReceipt carries the caller's item_receipt:approve grant (AD-3).
func Post(
	ctx context.Context, pool *pgxpool.Pool,
	uuid string, in PostInput, canApproveOverReceipt bool, actorEmployeeID int,
) (*ItemReceipt, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin post item receipt: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Lock the receipt, then its order, always in that order — every writer in
	// this module takes the same sequence, so concurrent posts queue instead of
	// deadlocking.
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
		return nil, fmt.Errorf("load item receipt for posting: %w", err)
	}
	if IsPosted(curStatusCode) {
		return nil, ErrAlreadyPosted
	}
	if curStatusCode != pendingStatusCode {
		return nil, ErrInvalidTransition
	}

	var poStatusCode string
	if err := tx.QueryRow(ctx, `
		SELECT rs.record_status_code
		FROM purchase_order po
		JOIN lkp_record_status rs ON rs.record_status_id = po.purchase_order_status
		WHERE po.purchase_order_id = $1 AND po.purchase_order_deleted_at IS NULL
		FOR UPDATE OF po`, poInternalID).Scan(&poStatusCode); err != nil {
		return nil, fmt.Errorf("lock purchase order for posting: %w", err)
	}
	if !receivableStatusCodes[poStatusCode] {
		return nil, ErrPONotReceivable
	}

	lines, err := loadPostLines(ctx, tx, irInternalID)
	if err != nil {
		return nil, err
	}
	if err := checkTolerance(lines, canApproveOverReceipt, in.OverReceiptReason); err != nil {
		return nil, err
	}

	recordTypeID, err := recordTypeIDByCode(ctx, tx, irctRecordTypeCode)
	if err != nil {
		return nil, fmt.Errorf("resolve IRCT record type: %w", err)
	}

	for _, l := range lines {
		if l.accepted <= 0 {
			continue // everything on this line was rejected; nothing to post
		}
		if _, err := tx.Exec(ctx, `
			UPDATE purchase_order_item
			SET qty_received = qty_received + $2,
			    item_updated_at = NOW(),
			    item_record_version = item_record_version + 1
			WHERE purchase_order_item_id = $1`, l.poItemID, l.accepted); err != nil {
			return nil, fmt.Errorf("advance ordered line received quantity: %w", err)
		}
		// Free-text purchase order lines carry no catalog item, so there is no
		// stock to move — the receipt still records their arrival.
		if l.inventoryItemID == nil {
			continue
		}
		if err := ledgerAndStock(ctx, tx, *l.inventoryItemID, warehouseID,
			ledgerEventReceived, l.accepted,
			recordTypeID, irInternalID, l.lineID, actorEmployeeID); err != nil {
			return nil, err
		}
	}

	newCode := receiptStatusFor(lines)
	if err := ValidateTransition(curStatusCode, newCode); err != nil {
		return nil, err
	}
	newStatusID, err := statusIDByCode(ctx, tx, recordTypeID, newCode)
	if err != nil {
		return nil, fmt.Errorf("resolve %q status: %w", newCode, err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE item_receipt SET
			item_receipt_status = $2,
			item_receipt_posted_at = NOW(),
			item_receipt_posted_by = $3,
			item_receipt_over_receipt_reason = $4,
			item_receipt_updated_at = NOW(),
			item_receipt_updated_by = $3,
			item_receipt_record_version = item_receipt_record_version + 1
		WHERE item_receipt_id = $1`,
		irInternalID, newStatusID, nullableInt(actorEmployeeID), in.OverReceiptReason); err != nil {
		return nil, fmt.Errorf("post item receipt: %w", err)
	}
	writeHistory(ctx, tx, irInternalID, "post", &curStatusID, &newStatusID, actorEmployeeID)

	if _, err := purchaseorder.ApplyReceiptRollup(ctx, tx, poInternalID, actorEmployeeID); err != nil {
		return nil, fmt.Errorf("roll up purchase order status: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit post item receipt: %w", err)
	}
	return Get(ctx, pool, uuid)
}
