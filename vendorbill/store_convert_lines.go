// vendorbill/store_convert_lines.go — reading a purchase order's lines for
// conversion: what each line ordered, received and has already had billed.
// Split from store_convert.go for the 300-line file cap; the decision about how
// much of each line to bill lives in billable.go.
package vendorbill

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// poSourceLine is one live purchase_order_item row's frozen values, copied
// verbatim (not re-priced) into the new bill's lines, plus the quantities that
// decide how much of the line this conversion bills.
type poSourceLine struct {
	internalID          int
	uuid                string
	lineNumber          int
	inventoryItemID     *int
	itemName, sku, desc string
	unitID              *int
	unitCode            string
	quantity            float64 // ordered
	qtyReceived         float64 // accepted so far -- rejected goods excluded
	qtyBilled           float64 // already covered by live, non-void bills
	billQty             float64 // what THIS conversion bills; set by planConversion
	unitPrice           float64
	discountPercent     float64
	taxRateID           *int
	taxPercent          float64
}

// loadPurchaseOrderSourceLines loads a live purchase order's lines by its
// internal id, with how much of each has been received and already billed.
//
// It locks the lines in one statement and reads the quantities in a second.
// Under READ COMMITTED that split is the point: a conversion queued behind
// another on the lock takes its snapshot only after the first has committed,
// so it sees the first's bill_conversion_line rows. Reading the billed total in
// the same statement as the lock would use a snapshot from before the wait and
// bill the same goods twice.
//
// The billed subquery must stay in step with purchaseorder's line query
// (store_get.go), which reports the same figure to the UI: sum the quantities
// recorded at conversion over bills that are live and not VOID.
func loadPurchaseOrderSourceLines(ctx context.Context, tx pgx.Tx, poInternalID int) ([]poSourceLine, error) {
	if _, err := tx.Exec(ctx, `
		SELECT 1 FROM purchase_order_item
		WHERE purchase_order_id = $1 AND item_deleted_at IS NULL
		FOR UPDATE`, poInternalID); err != nil {
		return nil, fmt.Errorf("lock purchase order lines: %w", err)
	}
	rows, err := tx.Query(ctx, `
		SELECT poi.purchase_order_item_id, poi.purchase_order_item_uuid, poi.line_number, poi.inventory_item_id,
		       poi.item_name, poi.sku, poi.description, poi.unit_id, COALESCE(poi.unit_code,''),
		       poi.quantity, poi.qty_received,
		       COALESCE((
		           SELECT SUM(cl.quantity)
		           FROM vendor_bill_conversion_line cl
		           JOIN vendor_bill_conversion c ON c.vendor_bill_conversion_id = cl.vendor_bill_conversion_id
		           JOIN vendor_bill vb ON vb.vendor_bill_id = c.vendor_bill_id AND vb.vendor_bill_deleted_at IS NULL
		           JOIN lkp_record_status rs ON rs.record_status_id = vb.vendor_bill_status
		           WHERE cl.purchase_order_item_id = poi.purchase_order_item_id
		             AND rs.record_status_code <> 'VOID'
		       ), 0),
		       poi.unit_price, poi.discount_percent, poi.tax_rate_id, poi.tax_percent
		FROM purchase_order_item poi
		WHERE poi.purchase_order_id = $1 AND poi.item_deleted_at IS NULL
		ORDER BY poi.line_number`, poInternalID)
	if err != nil {
		return nil, fmt.Errorf("load purchase order lines: %w", err)
	}
	defer rows.Close()
	out := []poSourceLine{}
	for rows.Next() {
		var l poSourceLine
		if err := rows.Scan(
			&l.internalID, &l.uuid, &l.lineNumber, &l.inventoryItemID,
			&l.itemName, &l.sku, &l.desc, &l.unitID, &l.unitCode,
			&l.quantity, &l.qtyReceived, &l.qtyBilled,
			&l.unitPrice, &l.discountPercent, &l.taxRateID, &l.taxPercent,
		); err != nil {
			return nil, fmt.Errorf("scan purchase order line: %w", err)
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}
