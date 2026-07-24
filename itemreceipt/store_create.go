// itemreceipt/store_create.go
package itemreceipt

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

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
}

// resolveLines validates every input line against the source order's live
// lines. A line may appear at most once per receipt: two arrivals of the same
// item are two receipts, or one line with the combined quantity — allowing a
// duplicate would silently double-post through the ledger's per-line uniqueness.
func resolveLines(ctx context.Context, q workflow.Querier, poInternalID int, items []LineInput) ([]resolvedLine, error) {
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

		if in.QtyReceived <= 0 {
			return nil, ClientError{Msg: fmt.Sprintf("Line %d: received quantity must be greater than zero.", in.LineNumber)}
		}
		if in.QtyRejected < 0 {
			return nil, ClientError{Msg: fmt.Sprintf("Line %d: rejected quantity cannot be negative.", in.LineNumber)}
		}
		if in.QtyRejected > in.QtyReceived {
			return nil, ClientError{Msg: fmt.Sprintf(
				"Line %d: rejected quantity cannot exceed the received quantity.", in.LineNumber)}
		}

		po, err := resolvePOLine(ctx, q, poInternalID, in.PurchaseOrderItemUUID)
		if err != nil {
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
			qtyReceived:     in.QtyReceived,
			qtyRejected:     in.QtyRejected,
			notes:           in.LineNotes,
			ordered:         po.quantity,
			alreadyRecv:     po.qtyReceived,
		})
	}
	return out, nil
}

// insertLines bulk-inserts resolved lines as item_receipt_line rows.
func insertLines(ctx context.Context, tx pgx.Tx, irInternalID int, lines []resolvedLine, actorEmployeeID int) error {
	for _, l := range lines {
		_, err := tx.Exec(ctx, `
			INSERT INTO item_receipt_line (
				item_receipt_id, line_number, purchase_order_item_id, inventory_item_id,
				item_name, sku, description, unit_id, unit_code,
				qty_received, qty_rejected, line_notes,
				item_created_by
			) VALUES ($1,$2,$3,$4, $5,$6,$7,$8,$9, $10,$11,$12, $13)`,
			irInternalID, l.lineNumber, l.poItemID, l.inventoryItemID,
			l.name, l.sku, l.desc, l.unitID, l.unitCode,
			l.qtyReceived, l.qtyRejected, l.notes,
			nullableInt(actorEmployeeID),
		)
		if err != nil {
			if isForeignKeyViolation(err) {
				return ClientError{Msg: fmt.Sprintf("Line %d: an invalid unit or item was referenced.", l.lineNumber)}
			}
			return fmt.Errorf("insert item receipt line: %w", err)
		}
	}
	return nil
}

// Create inserts a new item receipt (header + lines) inside one transaction:
// resolves the source purchase order and inherits its vendor snapshot, resolves
// every line against that order's ordered lines, defaults the warehouse, assigns
// the IRCT number, and starts the receipt at PEND.
//
// Nothing is posted here — no stock moves and no qty_received changes until
// Post is called. Creating a receipt is recording paperwork; posting it is the
// act that touches inventory.
func Create(ctx context.Context, pool *pgxpool.Pool, in CreateItemReceiptInput, actorEmployeeID int) (*ItemReceipt, error) {
	if strings.TrimSpace(in.PurchaseOrderUUID) == "" {
		return nil, ClientError{Msg: "A purchase order is required."}
	}
	if err := validateCustom(ctx, pool, in.CustomFields); err != nil {
		return nil, err
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin create item receipt: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	src, err := resolveSourceOrder(ctx, tx, in.PurchaseOrderUUID)
	if err != nil {
		return nil, err
	}

	lines, err := resolveLines(ctx, tx, src.internalID, in.Items)
	if err != nil {
		return nil, err
	}

	warehouseID := 0
	if in.WarehouseID != nil && *in.WarehouseID > 0 {
		warehouseID = *in.WarehouseID
	} else {
		warehouseID, err = defaultWarehouseID(ctx, tx)
		if err != nil {
			return nil, err
		}
	}

	recordTypeID, err := recordTypeIDByCode(ctx, tx, irctRecordTypeCode)
	if err != nil {
		return nil, fmt.Errorf("resolve IRCT record type: %w", err)
	}
	pendingStatusID, err := statusIDByCode(ctx, tx, recordTypeID, pendingStatusCode)
	if err != nil {
		return nil, fmt.Errorf("resolve PEND status: %w", err)
	}

	ownerEmployeeID := actorEmployeeID
	if in.OwnerEmployeeID != nil && *in.OwnerEmployeeID > 0 {
		ownerEmployeeID = *in.OwnerEmployeeID
	}
	custom := in.CustomFields
	if custom == nil {
		custom = map[string]any{}
	}

	var internalID int
	var newUUID string
	err = tx.QueryRow(ctx, `
		INSERT INTO item_receipt (
			record_type, item_receipt_status, purchase_order_id,
			item_receipt_vendor_id, item_receipt_vendor_name, warehouse_id,
			item_receipt_date, item_receipt_packing_slip, item_receipt_carrier,
			item_receipt_tracking_number, item_receipt_bill_of_lading,
			item_receipt_notes, item_receipt_internal_notes,
			item_receipt_owner_id, item_receipt_custom_fields, item_receipt_created_by
		) VALUES ($1,$2,$3, $4,$5,$6, $7::date,$8,$9, $10,$11, $12,$13, $14,$15,$16)
		RETURNING item_receipt_id, item_receipt_uuid`,
		recordTypeID, pendingStatusID, src.internalID,
		src.vendorID, src.vendorName, warehouseID,
		orNow(in.ReceiptDate), in.PackingSlip, in.Carrier,
		in.TrackingNumber, in.BillOfLading,
		in.Notes, in.InternalNotes,
		nullableInt(ownerEmployeeID), custom, nullableInt(actorEmployeeID),
	).Scan(&internalID, &newUUID)
	if err != nil {
		if isForeignKeyViolation(err) {
			return nil, ClientError{Msg: "One of the referenced ids (warehouse or owner) does not exist."}
		}
		return nil, fmt.Errorf("insert item receipt: %w", err)
	}

	number := FormatNumber(int64(internalID))
	if _, err := tx.Exec(ctx,
		`UPDATE item_receipt SET item_receipt_number = $1 WHERE item_receipt_id = $2`, number, internalID); err != nil {
		return nil, fmt.Errorf("set item receipt number: %w", err)
	}

	if err := insertLines(ctx, tx, internalID, lines, actorEmployeeID); err != nil {
		return nil, err
	}

	writeHistory(ctx, tx, internalID, "create", nil, &pendingStatusID, actorEmployeeID)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit create item receipt: %w", err)
	}
	return Get(ctx, pool, newUUID)
}
