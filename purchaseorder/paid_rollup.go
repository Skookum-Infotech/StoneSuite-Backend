// purchaseorder/paid_rollup.go — the paid end of a purchase order's life.
//
// A PO's own statuses track goods (Sent to Vendor -> Partially Received ->
// Received); PAID tracks money, and is derived from the vendor bills raised
// against the order rather than requested by anyone: the order is Paid once
// every live bill against it is Paid, and falls back to its receiving status
// if one of those payments is later voided. Lives here, not in vendorbill or
// vendorpayment, because the package that owns the table performs the write —
// the same convention ConvertFromRequisition follows (raw SQL against the
// other module's table, no import of its package).
//
// Lock order: this extends the AP hierarchy to vendor_payment < vendor_bill <
// purchase_order. Callers reach it holding their payment and bill locks, and
// must pass bills in a stable order (ascending id) so two concurrent cascades
// over the same orders can never take them in opposite orders.
package purchaseorder

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// PaidStatusCode is the status a purchase order holds once every vendor bill
// raised against it has been paid.
const PaidStatusCode = "PAID"

// paidFromStatuses are the statuses a purchase order may be marked Paid from:
// the order has to be with the vendor for a bill against it to be payable at
// all. A short-closed order still qualifies — its bill can outlive it.
var paidFromStatuses = map[string]bool{
	SentStatusCode: true, "PART": true, "RCVD": true, closedStatusCode: true,
}

// ApplyBillPaymentRollup re-derives the paid status of the purchase order the
// given vendor bill belongs to, if any, inside tx. It is a no-op for a bill
// with no linked order, and for an order whose goods status rules it out.
//
// Marking Paid requires every live (non-void) bill against the order to be
// Paid, so a part-billed order waits for the rest. The reverse move — an
// order sitting at Paid when a bill is no longer paid, e.g. after a payment is
// voided — puts it back on the receiving status its lines imply.
func ApplyBillPaymentRollup(ctx context.Context, tx pgx.Tx, billInternalID, actorEmployeeID int) error {
	var poInternalID *int
	err := tx.QueryRow(ctx, `
		SELECT vendor_bill_purchase_order_id FROM vendor_bill WHERE vendor_bill_id = $1`,
		billInternalID).Scan(&poInternalID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("resolve vendor bill's purchase order: %w", err)
	}
	if poInternalID == nil {
		return nil
	}
	return applyPaidRollup(ctx, tx, *poInternalID, actorEmployeeID)
}

// applyPaidRollup locks one purchase order and moves it onto, or back off,
// PAID according to the bills raised against it.
func applyPaidRollup(ctx context.Context, tx pgx.Tx, poInternalID, actorEmployeeID int) error {
	var curStatusID int
	var curStatusCode string
	err := tx.QueryRow(ctx, `
		SELECT po.purchase_order_status, rs.record_status_code
		FROM purchase_order po JOIN lkp_record_status rs ON rs.record_status_id = po.purchase_order_status
		WHERE po.purchase_order_id = $1 AND po.purchase_order_deleted_at IS NULL
		FOR UPDATE OF po`, poInternalID).Scan(&curStatusID, &curStatusCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil // deleted order: nothing to roll up
	}
	if err != nil {
		return fmt.Errorf("lock purchase order for paid rollup: %w", err)
	}

	var liveBills, paidBills int
	if err := tx.QueryRow(ctx, `
		SELECT COUNT(*), COUNT(*) FILTER (WHERE rs.record_status_code = $2)
		FROM vendor_bill vb
		JOIN lkp_record_status rs ON rs.record_status_id = vb.vendor_bill_status
		WHERE vb.vendor_bill_purchase_order_id = $1 AND vb.vendor_bill_deleted_at IS NULL
		  AND rs.record_status_code <> 'VOID'`,
		poInternalID, PaidStatusCode).Scan(&liveBills, &paidBills); err != nil {
		return fmt.Errorf("count purchase order bills: %w", err)
	}
	allPaid := liveBills > 0 && paidBills == liveBills

	var toCode string
	switch {
	case allPaid && curStatusCode != PaidStatusCode && paidFromStatuses[curStatusCode]:
		toCode = PaidStatusCode
	case !allPaid && curStatusCode == PaidStatusCode:
		toCode, err = receivingStatusCode(ctx, tx, poInternalID)
		if err != nil {
			return err
		}
	default:
		return nil // already where it belongs
	}

	recordTypeID, err := recordTypeIDByCode(ctx, tx, pordRecordTypeCode)
	if err != nil {
		return fmt.Errorf("resolve PORD record type: %w", err)
	}
	toStatusID, err := statusIDByCode(ctx, tx, recordTypeID, toCode)
	if err != nil {
		return fmt.Errorf("resolve %s status: %w", toCode, err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE purchase_order SET purchase_order_status = $2,
			purchase_order_updated_at = NOW(), purchase_order_updated_by = $3,
			purchase_order_record_version = purchase_order_record_version + 1
		WHERE purchase_order_id = $1`, poInternalID, toStatusID, nullableInt(actorEmployeeID)); err != nil {
		return fmt.Errorf("update purchase order paid status: %w", err)
	}
	writeHistory(ctx, tx, poInternalID, "transition", &curStatusID, &toStatusID, actorEmployeeID)
	return nil
}

// receivingStatusCode is where an order belongs on its goods alone — what a
// no-longer-paid order falls back to. An order that was paid had to have been
// sent, so nothing received still means Sent to Vendor.
func receivingStatusCode(ctx context.Context, tx pgx.Tx, poInternalID int) (string, error) {
	rows, err := tx.Query(ctx, `
		SELECT quantity, qty_received FROM purchase_order_item
		WHERE purchase_order_id = $1 AND item_deleted_at IS NULL`, poInternalID)
	if err != nil {
		return "", fmt.Errorf("load purchase order lines for rollback: %w", err)
	}
	defer rows.Close()
	var lines []LineReceipt
	for rows.Next() {
		var l LineReceipt
		if err := rows.Scan(&l.Quantity, &l.QtyReceived); err != nil {
			return "", fmt.Errorf("scan purchase order line: %w", err)
		}
		lines = append(lines, l)
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("read purchase order lines: %w", err)
	}
	if code := RollupReceiptStatus(lines); code != "" {
		return code, nil
	}
	return SentStatusCode, nil
}
