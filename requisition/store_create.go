// requisition/store_create.go
package requisition

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/workflow"
)

// resolvedLine is a line after catalog/free-text resolution, ready to price
// and insert.
type resolvedLine struct {
	lineNumber      int
	inventoryItemID *int // internal FK, nil for free-text
	sku, name, desc string
	unitID          *int
	unitCode        string
	quantity        float64
	unitPrice       float64
	money           LineMoney
}

// resolveLines validates and resolves every input line against the catalog
// (or free text), computing each line's stored money (AD-3, AD-9).
func resolveLines(ctx context.Context, q workflow.Querier, items []LineInput) ([]resolvedLine, error) {
	if len(items) == 0 {
		return nil, ClientError{Msg: "At least one line item is required."}
	}
	out := make([]resolvedLine, 0, len(items))
	seenLine := map[int]bool{}
	for _, in := range items {
		if in.LineNumber <= 0 {
			return nil, ClientError{Msg: "Each line item needs a positive line number."}
		}
		if seenLine[in.LineNumber] {
			return nil, ClientError{Msg: fmt.Sprintf("Duplicate line number %d.", in.LineNumber)}
		}
		seenLine[in.LineNumber] = true
		if in.Quantity <= 0 {
			return nil, ClientError{Msg: fmt.Sprintf("Line %d: quantity must be greater than zero.", in.LineNumber)}
		}
		if in.EstimatedUnitPrice < 0 {
			return nil, ClientError{Msg: fmt.Sprintf("Line %d: estimated unit price cannot be negative.", in.LineNumber)}
		}

		rl := resolvedLine{
			lineNumber: in.LineNumber,
			quantity:   in.Quantity,
			unitPrice:  in.EstimatedUnitPrice,
		}

		if in.InventoryItemUUID != "" {
			item, err := resolveInventoryItem(ctx, q, in.InventoryItemUUID)
			if err != nil {
				return nil, err
			}
			id := item.internalID
			rl.inventoryItemID = &id
			rl.sku, rl.name, rl.desc = item.sku, item.name, item.desc
			rl.unitID, rl.unitCode = item.unitID, item.unitCode
			if rl.unitPrice == 0 {
				rl.unitPrice = item.unitPrice
			}
		} else if strings.TrimSpace(in.Description) == "" {
			return nil, ClientError{Msg: fmt.Sprintf("Line %d: either an inventory item or a description is required.", in.LineNumber)}
		} else {
			// Free-text lines (no catalog item) snapshot the caller's
			// description as both the item name and the description — item_name
			// must never be left empty, or a round-tripped Update would see
			// neither an inventoryItemUuid nor a description and be rejected
			// by this same check (mirrors purchaseorder/estimate/invoice).
			rl.name = in.Description
			rl.desc = in.Description
		}

		rl.money = ComputeLine(CalcLineInput{Quantity: rl.quantity, EstimatedUnitPrice: rl.unitPrice})
		out = append(out, rl)
	}
	return out, nil
}

// insertLines bulk-inserts resolved lines as requisition_item rows.
func insertLines(ctx context.Context, tx pgx.Tx, reqnInternalID int, lines []resolvedLine, actorEmployeeID int) error {
	for _, l := range lines {
		_, err := tx.Exec(ctx, `
			INSERT INTO requisition_item (
				requisition_id, line_number, inventory_item_id,
				item_name, sku, description, unit_id, unit_code,
				quantity, estimated_unit_price, estimated_amount,
				item_created_by
			) VALUES ($1,$2,$3, $4,$5,$6,$7,$8, $9,$10,$11, $12)`,
			reqnInternalID, l.lineNumber, l.inventoryItemID,
			l.name, l.sku, l.desc, l.unitID, l.unitCode,
			l.quantity, l.unitPrice, l.money.EstimatedAmount,
			nullableInt(actorEmployeeID),
		)
		if err != nil {
			if isForeignKeyViolation(err) {
				return ClientError{Msg: fmt.Sprintf("Line %d: an invalid unit was referenced.", l.lineNumber)}
			}
			return fmt.Errorf("insert requisition item: %w", err)
		}
	}
	return nil
}

// writeHistory records one requisition_history row inside the caller's transaction.
func writeHistory(ctx context.Context, tx pgx.Tx, reqnInternalID int, action string, fromStatusID, toStatusID *int, actorEmployeeID int) {
	_, _ = tx.Exec(ctx, `
		INSERT INTO requisition_history (requisition_id, from_status_id, to_status_id, action, actor_employee_id)
		VALUES ($1,$2,$3,$4,$5)`,
		reqnInternalID, fromStatusID, toStatusID, action, nullableInt(actorEmployeeID))
}

// Create inserts a new requisition (header + lines) inside one transaction:
// snapshots the suggested vendor's name when one is given (AD-2), resolves
// and prices every line (AD-3, AD-9), computes header totals, assigns the
// REQN number (AD-10), and starts the requisition at DRFT.
func Create(ctx context.Context, pool *pgxpool.Pool, in CreateRequisitionInput, actorEmployeeID int) (*Requisition, error) {
	if err := validateCustom(ctx, pool, in.CustomFields); err != nil {
		return nil, err
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin create requisition: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var vendorInternalID any
	var vendorName string
	if strings.TrimSpace(in.VendorUUID) != "" {
		id, name, err := vendorSnapshot(ctx, tx, in.VendorUUID)
		if err != nil {
			return nil, err
		}
		vendorInternalID, vendorName = id, name
	}

	lines, err := resolveLines(ctx, tx, in.Items)
	if err != nil {
		return nil, err
	}
	lineMoney := make([]LineMoney, len(lines))
	for i, l := range lines {
		lineMoney[i] = l.money
	}
	header := ComputeHeader(lineMoney, in.SalesTaxPercent)

	recordTypeID, err := recordTypeIDByCode(ctx, tx, reqnRecordTypeCode)
	if err != nil {
		return nil, fmt.Errorf("resolve REQN record type: %w", err)
	}
	draftStatusID, err := statusIDByCode(ctx, tx, recordTypeID, draftStatusCode)
	if err != nil {
		return nil, fmt.Errorf("resolve DRFT status: %w", err)
	}

	requestedByID := in.RequestedByEmployeeID
	if requestedByID <= 0 {
		requestedByID = actorEmployeeID
	}
	if requestedByID <= 0 {
		requestedByID = systemEmployeeID
	}
	custom := in.CustomFields
	if custom == nil {
		custom = map[string]any{}
	}
	priority, err := orNormal(in.Priority)
	if err != nil {
		return nil, err
	}

	cv := []colVal{
		{"record_type", recordTypeID, ""},
		{"requisition_status", draftStatusID, ""},
		{"requisition_requested_by_id", requestedByID, ""},
		{"requisition_department", in.Department, ""},
		{"requisition_needed_by_date", nullableDate(in.NeededByDate), "::date"},
		{"requisition_priority", priority, ""},
		{"requisition_memo", in.Memo, ""},
		{"requisition_vendor_id", vendorInternalID, ""},
		{"requisition_vendor_name", vendorName, ""},
		{"requisition_payment_terms", in.PaymentTermsID, ""},
		{"requisition_sales_tax_percent", in.SalesTaxPercent, ""},
		{"requisition_subtotal", header.Subtotal, ""},
		{"requisition_tax_total", header.TaxTotal, ""},
		{"requisition_estimated_total", header.EstimatedTotal, ""},
		{"requisition_custom_fields", custom, ""},
		{"requisition_created_by", nullableInt(actorEmployeeID), ""},
	}

	insertSQL, insertArgs := buildInsert("requisition", cv, "requisition_id, requisition_uuid")
	var internalID int
	var newUUID string
	err = tx.QueryRow(ctx, insertSQL, insertArgs...).Scan(&internalID, &newUUID)
	if err != nil {
		if isForeignKeyViolation(err) {
			return nil, ClientError{Msg: "One of the referenced ids (requester, payment terms, or vendor) does not exist."}
		}
		return nil, fmt.Errorf("insert requisition: %w", err)
	}

	number := FormatNumber(int64(internalID))
	if _, err := tx.Exec(ctx,
		`UPDATE requisition SET requisition_number = $1 WHERE requisition_id = $2`, number, internalID); err != nil {
		return nil, fmt.Errorf("set requisition number: %w", err)
	}

	if err := insertLines(ctx, tx, internalID, lines, actorEmployeeID); err != nil {
		return nil, err
	}

	writeHistory(ctx, tx, internalID, "create", nil, &draftStatusID, actorEmployeeID)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit create requisition: %w", err)
	}
	return Get(ctx, pool, newUUID)
}
