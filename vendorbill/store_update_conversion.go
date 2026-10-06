// vendorbill/store_update_conversion.go — keeping a converted bill's purchase
// order lineage in step when its draft lines are edited. Split from
// store_update.go; the quantity rules are pure and live in billable.go.
package vendorbill

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// loadConversionID returns the vendor_bill_conversion row of a bill and its
// purchase order, or zeros when the bill was not converted from a purchase order.
func loadConversionID(ctx context.Context, tx pgx.Tx, vbInternalID int) (conversionID, poID int, err error) {
	err = tx.QueryRow(ctx, `
		SELECT vendor_bill_conversion_id, purchase_order_id FROM vendor_bill_conversion
		WHERE vendor_bill_id = $1`, vbInternalID).Scan(&conversionID, &poID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, 0, nil
	}
	if err != nil {
		return 0, 0, fmt.Errorf("load vendor bill conversion: %w", err)
	}
	return conversionID, poID, nil
}

// loadConversionQuantities returns what this bill's conversion currently
// claims per purchase order line (purchase_order_item_id -> quantity).
func loadConversionQuantities(ctx context.Context, tx pgx.Tx, conversionID int) (map[int]float64, error) {
	rows, err := tx.Query(ctx, `
		SELECT purchase_order_item_id, quantity FROM vendor_bill_conversion_line
		WHERE vendor_bill_conversion_id = $1`, conversionID)
	if err != nil {
		return nil, fmt.Errorf("load vendor bill conversion lines: %w", err)
	}
	defer rows.Close()
	out := map[int]float64{}
	for rows.Next() {
		var id int
		var qty float64
		if err := rows.Scan(&id, &qty); err != nil {
			return nil, fmt.Errorf("scan vendor bill conversion line: %w", err)
		}
		out[id] = qty
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read vendor bill conversion lines: %w", err)
	}
	return out, nil
}

// anyLinked reports whether any resolved line carries a purchase order link.
func anyLinked(lines []resolvedLine) bool {
	for _, l := range lines {
		if l.purchaseOrderItemUUID != "" {
			return true
		}
	}
	return false
}

// syncConversionOnEdit resolves each edited line's purchase order link to the
// PO line's internal id (stamped onto the line, so insertLines preserves it),
// rejects links that would over-bill, and rewrites the bill's
// vendor_bill_conversion_line rows from the edited quantities. It runs inside
// Update's transaction and locks the purchase order lines.
//
// When no edited line carries a link the conversion is left untouched: a
// client that predates the link field cannot express "same lines", and
// treating its silence as "unlink everything" would release billed goods.
func syncConversionOnEdit(ctx context.Context, tx pgx.Tx, vbInternalID int, lines []resolvedLine) error {
	conversionID, poID, err := loadConversionID(ctx, tx, vbInternalID)
	if err != nil {
		return err
	}
	if conversionID == 0 {
		if anyLinked(lines) {
			return ClientError{Msg: "This vendor bill was not converted from a purchase order, so its lines cannot link to purchase order lines."}
		}
		return nil
	}
	if !anyLinked(lines) {
		return nil
	}

	source, err := loadPurchaseOrderSourceLines(ctx, tx, poID)
	if err != nil {
		return err
	}
	current, err := loadConversionQuantities(ctx, tx, conversionID)
	if err != nil {
		return err
	}

	byUUID := make(map[string]poSourceLine, len(source))
	for _, s := range source {
		byUUID[s.uuid] = s
	}
	linked := make([]linkedQty, 0, len(lines))
	for i := range lines {
		u := lines[i].purchaseOrderItemUUID
		if u == "" {
			continue
		}
		s, ok := byUUID[u]
		if !ok {
			return ClientError{Msg: fmt.Sprintf("Line %d: purchaseOrderItemId does not belong to this bill's purchase order.", lines[i].lineNumber)}
		}
		id := s.internalID
		lines[i].purchaseOrderItemID = &id
		linked = append(linked, linkedQty{lineNumber: lines[i].lineNumber, poItemID: id, quantity: lines[i].quantity})
	}

	next, err := planLinkedQuantities(source, current, linked)
	if err != nil {
		return err
	}

	if _, err := tx.Exec(ctx,
		`DELETE FROM vendor_bill_conversion_line WHERE vendor_bill_conversion_id = $1`, conversionID); err != nil {
		return fmt.Errorf("clear vendor bill conversion lines: %w", err)
	}
	for _, s := range source {
		qty, ok := next[s.internalID]
		if !ok {
			continue
		}
		if _, err := tx.Exec(ctx, `
			INSERT INTO vendor_bill_conversion_line (vendor_bill_conversion_id, purchase_order_item_id, quantity)
			VALUES ($1, $2, $3)`, conversionID, s.internalID, qty); err != nil {
			return fmt.Errorf("rewrite vendor bill conversion line: %w", err)
		}
	}
	return nil
}
