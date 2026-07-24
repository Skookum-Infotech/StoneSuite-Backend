package purchaseorder

// receipt_apply.go — the transactional half of AD-4. receipt_rollup.go decides
// *what* status the receiving progress implies; this file applies it to the
// header, inside the caller's transaction.
//
// Why this lives here and not in itemreceipt/:
//
//   - Transition() begins its own transaction and row-locks the order, so an
//     Item Receipt posting cannot call it from inside its own transaction
//     without deadlocking against itself. The rollup has to be tx-scoped.
//   - Purchase order status semantics belong to the purchase order module. The
//     Item Receipt module supplies the trigger; it does not get to invent what
//     a PO status means.

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// rollupTransitions is the status map for *receipt-driven* header moves, and
// it is deliberately NOT allowedTransitions.
//
// The user-facing map is one-way on purpose: a person may not walk an order
// backwards. But voiding a receipt gives quantities back, and the header must
// be free to follow them down — an order that is no longer fully received must
// stop claiming RCVD. So this map carries both directions between the three
// receiving states, and nothing else: DRFT/PAPV/APPV are unreachable (an order
// the vendor never received cannot be receiving goods) and CLSD/CANC are
// terminal (a late or reversed receipt must never resurrect a closed order).
var rollupTransitions = map[string]map[string]bool{
	"SENT": {"PART": true, "RCVD": true},
	"PART": {"SENT": true, "RCVD": true},
	"RCVD": {"SENT": true, "PART": true},
}

// canRollup reports whether a receipt posting/void may move the header from
// fromCode to toCode.
func canRollup(fromCode, toCode string) bool {
	return rollupTransitions[fromCode][toCode]
}

// ApplyReceiptRollup recomputes a purchase order's header status from its
// lines' ordered-vs-received quantities and writes the result, inside the
// caller's transaction.
//
// It must be called after the caller has updated purchase_order_item
// .qty_received, and the caller must already hold the order's row lock
// (SELECT ... FOR UPDATE) so two concurrent receipts cannot interleave.
//
// Returns the status code the order now carries. When the rollup implies no
// change — or implies a move this map forbids, such as reopening a closed
// order — the current code is returned and nothing is written.
//
// The zero-received case is handled explicitly: RollupReceiptStatus returns ""
// for "nothing received", which is indistinguishable from "no opinion" for a
// forward posting but means "fall back to SENT" after a void.
func ApplyReceiptRollup(ctx context.Context, tx pgx.Tx, poInternalID, actorEmployeeID int) (string, error) {
	var curStatusID int
	var curCode string
	err := tx.QueryRow(ctx, `
		SELECT po.purchase_order_status, rs.record_status_code
		FROM purchase_order po
		JOIN lkp_record_status rs ON rs.record_status_id = po.purchase_order_status
		WHERE po.purchase_order_id = $1 AND po.purchase_order_deleted_at IS NULL`, poInternalID,
	).Scan(&curStatusID, &curCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", fmt.Errorf("load purchase order for rollup: %w", err)
	}

	rows, err := tx.Query(ctx, `
		SELECT quantity, qty_received
		FROM purchase_order_item
		WHERE purchase_order_id = $1 AND item_deleted_at IS NULL`, poInternalID)
	if err != nil {
		return "", fmt.Errorf("load purchase order lines for rollup: %w", err)
	}
	defer rows.Close()
	var lines []LineReceipt
	for rows.Next() {
		var l LineReceipt
		if err := rows.Scan(&l.Quantity, &l.QtyReceived); err != nil {
			return "", fmt.Errorf("scan purchase order line for rollup: %w", err)
		}
		lines = append(lines, l)
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("load purchase order lines for rollup: %w", err)
	}

	implied := RollupReceiptStatus(lines)
	if implied == "" {
		// Nothing is received any more. From a receiving state that means the
		// order reverts to "issued, awaiting goods"; from anywhere else it
		// means this order simply has no receiving progress to express.
		if _, receiving := rollupTransitions[curCode]; !receiving {
			return curCode, nil
		}
		implied = "SENT"
	}
	if implied == curCode || !canRollup(curCode, implied) {
		return curCode, nil
	}

	recordTypeID, err := recordTypeIDByCode(ctx, tx, pordRecordTypeCode)
	if err != nil {
		return "", fmt.Errorf("resolve PORD record type: %w", err)
	}
	toStatusID, err := statusIDByCode(ctx, tx, recordTypeID, implied)
	if err != nil {
		return "", fmt.Errorf("resolve %q status: %w", implied, err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE purchase_order SET
			purchase_order_status = $2,
			purchase_order_updated_at = NOW(),
			purchase_order_updated_by = $3,
			purchase_order_record_version = purchase_order_record_version + 1
		WHERE purchase_order_id = $1`, poInternalID, toStatusID, nullableInt(actorEmployeeID)); err != nil {
		return "", fmt.Errorf("apply receipt rollup: %w", err)
	}
	writeHistory(ctx, tx, poInternalID, "transition", &curStatusID, &toStatusID, actorEmployeeID)
	return implied, nil
}
