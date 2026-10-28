// itemreceipt/store_lines.go — receipt line resolution and insertion, split
// from store_create.go for the 300-line file cap.
package itemreceipt

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"stonesuite-backend/inventory"
	"stonesuite-backend/workflow"
)

// resolvedLine is a receipt line after its ordered line has been resolved,
// ready to insert. The snapshot fields are copied from the PO line rather than
// re-read from the catalog, so a later catalog edit never rewrites a receipt.
type resolvedLine struct {
	lineNumber      int
	poItemID        int
	inventoryItemID *int
	sku, name, desc string
	unitID          *int
	unitCode        string
	qtyReceived     float64
	qtyRejected     float64
	notes           string
	// Carried for the tolerance check at post time.
	ordered     float64
	alreadyRecv float64
	// slabs is set only on a serialized line, where it defines qtyReceived.
	slabs []resolvedSlab
}

// resolveSlabBins resolves each slab's bin uuid to its id, requiring the bin to
// sit in the receiving warehouse.
func resolveSlabBins(ctx context.Context, q workflow.Querier, lineNo int, slabs []resolvedSlab, in []SlabInput, warehouseID int) error {
	for i := range slabs {
		binID, err := inventory.ResolveBinForWarehouse(ctx, q, in[i].BinID, warehouseID)
		if err != nil {
			if inventory.IsClientError(err) {
				return ClientError{Msg: fmt.Sprintf("Line %d, slab %d: %s", lineNo, i+1, err.Error())}
			}
			return err
		}
		slabs[i].binID = binID
	}
	return nil
}

// requireExplicitWarehouse refuses a receipt that brings slabs in without the
// caller naming where they go. Bulk lines may fall back to the tenant's default
// warehouse; a slab's location is mandatory information, not a default.
func requireExplicitWarehouse(lines []resolvedLine, warehouseID *int) error {
	if warehouseID != nil && *warehouseID > 0 {
		return nil
	}
	for _, l := range lines {
		if len(l.slabs) > 0 {
			return ClientError{Msg: "A receiving location is required when receiving slabs."}
		}
	}
	return nil
}

// resolveLines validates every input line against the source order's live
// lines. A line may appear at most once per receipt: two arrivals of the same
// item are two receipts, or one line with the combined quantity — allowing a
// duplicate would silently double-post through the ledger's per-line uniqueness.
//
// warehouseID is the receiving warehouse, needed to validate each slab's bin. A
// serialized item's line is resolved from its slabs (see computeSlabs); any
// other line from the typed quantities.
func resolveLines(ctx context.Context, q workflow.Querier, poInternalID, warehouseID int, items []LineInput) ([]resolvedLine, error) {
	if len(items) == 0 {
		return nil, ClientError{Msg: "At least one line item is required."}
	}
	out := make([]resolvedLine, 0, len(items))
	seenLine := map[int]bool{}
	seenPOItem := map[string]bool{}
	for _, in := range items {
		if in.LineNumber <= 0 {
			return nil, ClientError{Msg: "Each line item needs a positive line number."}
		}
		if seenLine[in.LineNumber] {
			return nil, ClientError{Msg: fmt.Sprintf("Duplicate line number %d.", in.LineNumber)}
		}
		seenLine[in.LineNumber] = true

		if strings.TrimSpace(in.PurchaseOrderItemUUID) == "" {
			return nil, ClientError{Msg: fmt.Sprintf("Line %d: a purchase order line is required.", in.LineNumber)}
		}
		if seenPOItem[in.PurchaseOrderItemUUID] {
			return nil, ClientError{Msg: fmt.Sprintf(
				"Line %d: purchase order line %s appears twice on this receipt.", in.LineNumber, in.PurchaseOrderItemUUID)}
		}
		seenPOItem[in.PurchaseOrderItemUUID] = true

		po, err := resolvePOLine(ctx, q, poInternalID, in.PurchaseOrderItemUUID)
		if err != nil {
			return nil, err
		}

		qtyReceived, qtyRejected := in.QtyReceived, in.QtyRejected
		var slabs []resolvedSlab
		serialized := false
		if po.inventoryItemID != nil {
			info, err := inventory.ResolveItemUnitByID(ctx, q, *po.inventoryItemID)
			if err != nil {
				return nil, fromInventoryErr(err)
			}
			serialized = info.IsSerialized()
			if serialized {
				if in.QtyRejected != 0 {
					return nil, ClientError{Msg: fmt.Sprintf(
						"Line %d: a slab line has no rejected quantity — leave a refused slab out instead.", in.LineNumber)}
				}
				if slabs, qtyReceived, err = computeSlabs(in.LineNumber, info, in.Slabs); err != nil {
					return nil, err
				}
				if err := resolveSlabBins(ctx, q, in.LineNumber, slabs, in.Slabs, warehouseID); err != nil {
					return nil, err
				}
				qtyRejected = 0
			}
		}
		if !serialized && len(in.Slabs) > 0 {
			return nil, ClientError{Msg: fmt.Sprintf(
				"Line %d: slabs can only be entered for slab-tracked items.", in.LineNumber)}
		}

		if err := validateLineQuantities(in.LineNumber, qtyReceived, qtyRejected); err != nil {
			return nil, err
		}

		out = append(out, resolvedLine{
			lineNumber:      in.LineNumber,
			poItemID:        po.internalID,
			inventoryItemID: po.inventoryItemID,
			sku:             po.sku,
			name:            po.name,
			desc:            po.desc,
			unitID:          po.unitID,
			unitCode:        po.unitCode,
			qtyReceived:     qtyReceived,
			qtyRejected:     qtyRejected,
			notes:           in.LineNotes,
			ordered:         po.quantity,
			alreadyRecv:     po.qtyReceived,
			slabs:           slabs,
		})
	}
	return out, nil
}

// insertLines bulk-inserts resolved lines as item_receipt_line rows.
func insertLines(ctx context.Context, tx pgx.Tx, irInternalID int, lines []resolvedLine, actorEmployeeID int) error {
	for _, l := range lines {
		var lineID int
		err := tx.QueryRow(ctx, `
			INSERT INTO item_receipt_line (
				item_receipt_id, line_number, purchase_order_item_id, inventory_item_id,
				item_name, sku, description, unit_id, unit_code,
				qty_received, qty_rejected, line_notes,
				item_created_by
			) VALUES ($1,$2,$3,$4, $5,$6,$7,$8,$9, $10,$11,$12, $13)
			RETURNING item_receipt_line_id`,
			irInternalID, l.lineNumber, l.poItemID, l.inventoryItemID,
			l.name, l.sku, l.desc, l.unitID, l.unitCode,
			l.qtyReceived, l.qtyRejected, l.notes,
			nullableInt(actorEmployeeID),
		).Scan(&lineID)
		if err != nil {
			if isForeignKeyViolation(err) {
				return ClientError{Msg: fmt.Sprintf("Line %d: an invalid unit or item was referenced.", l.lineNumber)}
			}
			return fmt.Errorf("insert item receipt line: %w", err)
		}
		if err := insertLineSlabs(ctx, tx, lineID, l.lineNumber, l.slabs); err != nil {
			return err
		}
	}
	return nil
}
