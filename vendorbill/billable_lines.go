// vendorbill/billable_lines.go — read-only view of what a purchase order still
// lets a bill claim, for callers outside this package (the document-extraction
// PO link). It shares poSourceLinesSQL with the convert path so the billed rule
// has exactly one copy.
package vendorbill

import (
	"context"

	"stonesuite-backend/workflow"
)

// BillableLine is one live purchase order line with its received and billed
// quantities; what a new bill may still claim is QtyReceived - QtyBilled.
type BillableLine struct {
	UUID              string
	LineNumber        int
	InventoryItemUUID string
	Description       string
	Qty               float64
	QtyReceived       float64
	QtyBilled         float64
	UnitPrice         float64
}

// BillableLines loads a purchase order's live lines, by its internal id, with
// received and billed quantities (billed = recorded at conversion over live,
// non-VOID bills). It takes no locks.
func BillableLines(ctx context.Context, q workflow.Querier, poInternalID int) ([]BillableLine, error) {
	src, err := queryPurchaseOrderSourceLines(ctx, q, poInternalID)
	if err != nil {
		return nil, err
	}
	out := make([]BillableLine, len(src))
	for i, l := range src {
		out[i] = BillableLine{
			UUID: l.uuid, LineNumber: l.lineNumber, InventoryItemUUID: l.inventoryItemUUID,
			Description: l.desc, Qty: l.quantity, QtyReceived: l.qtyReceived,
			QtyBilled: l.qtyBilled, UnitPrice: l.unitPrice,
		}
	}
	return out, nil
}
