// purchaseorder/store_create.go
package purchaseorder

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
	discountPercent float64
	taxRateID       *int
	taxPercent      float64
	money           LineMoney
}

// resolveLines validates and resolves every input line against the catalog
// (or free text), computing each line's stored money (AD-3, AD-7). headerTax
// is the header's default tax percent, used when a line has no tax rate.
func resolveLines(ctx context.Context, q workflow.Querier, items []LineInput, headerTax float64) ([]resolvedLine, error) {
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
		if in.UnitPrice < 0 {
			return nil, ClientError{Msg: fmt.Sprintf("Line %d: unit price cannot be negative.", in.LineNumber)}
		}

		rl := resolvedLine{
			lineNumber:      in.LineNumber,
			quantity:        in.Quantity,
			unitPrice:       in.UnitPrice,
			discountPercent: in.DiscountPercent,
			taxRateID:       in.TaxRateID,
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
			if rl.taxRateID == nil {
				rl.taxRateID = item.taxRateID
			}
		} else if strings.TrimSpace(in.Description) == "" {
			return nil, ClientError{Msg: fmt.Sprintf("Line %d: either an inventory item or a description is required.", in.LineNumber)}
		} else {
			// Free-text lines (no catalog item) snapshot the caller's
			// description as both the item name and the description — item_name
			// must never be left empty, or a round-tripped Update would see
			// neither an inventoryItemUuid nor a description and be rejected
			// by this same check (mirrors estimate/invoice).
			rl.name = in.Description
			rl.desc = in.Description
		}

		if rl.taxRateID != nil {
			pct, err := taxPercentForRate(ctx, q, *rl.taxRateID)
			if err != nil {
				return nil, err
			}
			rl.taxPercent = pct
		} else {
			rl.taxPercent = headerTax
		}

		rl.money = ComputeLine(CalcLineInput{
			Quantity: rl.quantity, UnitPrice: rl.unitPrice,
			DiscountPercent: rl.discountPercent, TaxPercent: rl.taxPercent,
		})
		out = append(out, rl)
	}
	return out, nil
}

// insertLines bulk-inserts resolved lines as purchase_order_item rows.
func insertLines(ctx context.Context, tx pgx.Tx, poInternalID int, lines []resolvedLine, actorEmployeeID int) error {
	for _, l := range lines {
		_, err := tx.Exec(ctx, `
			INSERT INTO purchase_order_item (
				purchase_order_id, line_number, inventory_item_id,
				item_name, sku, description, unit_id, unit_code,
				quantity, unit_price, discount_percent, tax_rate_id, tax_percent,
				line_subtotal, line_discount, line_tax, line_total,
				item_created_by
			) VALUES ($1,$2,$3, $4,$5,$6,$7,$8, $9,$10,$11,$12,$13, $14,$15,$16,$17, $18)`,
			poInternalID, l.lineNumber, l.inventoryItemID,
			l.name, l.sku, l.desc, l.unitID, l.unitCode,
			l.quantity, l.unitPrice, l.discountPercent, l.taxRateID, l.taxPercent,
			l.money.Subtotal, l.money.Discount, l.money.Tax, l.money.Total,
			nullableInt(actorEmployeeID),
		)
		if err != nil {
			if isForeignKeyViolation(err) {
				return ClientError{Msg: fmt.Sprintf("Line %d: an invalid unit or tax rate was referenced.", l.lineNumber)}
			}
			return fmt.Errorf("insert purchase order item: %w", err)
		}
	}
	return nil
}

// writeHistory records one purchase_order_history row inside the caller's transaction.
func writeHistory(ctx context.Context, tx pgx.Tx, poInternalID int, action string, fromStatusID, toStatusID *int, actorEmployeeID int) {
	_, _ = tx.Exec(ctx, `
		INSERT INTO purchase_order_history (purchase_order_id, from_status_id, to_status_id, action, actor_employee_id)
		VALUES ($1,$2,$3,$4,$5)`,
		poInternalID, fromStatusID, toStatusID, action, nullableInt(actorEmployeeID))
}

// Create inserts a new purchase order (header + lines) inside one
// transaction: snapshots the vendor name (AD-2), resolves and prices every
// line (AD-3, AD-7), computes header totals, assigns the PORD number (AD-8),
// and starts the order at DRFT (AD-5).
func Create(ctx context.Context, pool *pgxpool.Pool, in CreatePurchaseOrderInput, actorEmployeeID int) (*PurchaseOrder, error) {
	if strings.TrimSpace(in.VendorUUID) == "" {
		return nil, ClientError{Msg: "A vendor is required."}
	}
	if err := validateCustom(ctx, pool, in.CustomFields); err != nil {
		return nil, err
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin create purchase order: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	vendorInternalID, vendorName, err := vendorSnapshot(ctx, tx, in.VendorUUID)
	if err != nil {
		return nil, err
	}

	lines, err := resolveLines(ctx, tx, in.Items, in.SalesTaxPercent)
	if err != nil {
		return nil, err
	}
	lineMoney := make([]LineMoney, len(lines))
	for i, l := range lines {
		lineMoney[i] = l.money
	}
	header := ComputeHeader(lineMoney, in.ShippingCharge, in.Adjustment)

	recordTypeID, err := recordTypeIDByCode(ctx, tx, pordRecordTypeCode)
	if err != nil {
		return nil, fmt.Errorf("resolve PORD record type: %w", err)
	}
	draftStatusID, err := statusIDByCode(ctx, tx, recordTypeID, draftStatusCode)
	if err != nil {
		return nil, fmt.Errorf("resolve DRFT status: %w", err)
	}

	ownerEmployeeID := actorEmployeeID
	if in.OwnerEmployeeID != nil && *in.OwnerEmployeeID > 0 {
		ownerEmployeeID = *in.OwnerEmployeeID
	}
	custom := in.CustomFields
	if custom == nil {
		custom = map[string]any{}
	}

	cv := []colVal{
		{"record_type", recordTypeID, ""},
		{"purchase_order_status", draftStatusID, ""},
		{"purchase_order_vendor_id", vendorInternalID, ""},
		{"purchase_order_vendor_name", vendorName, ""},
		{"purchase_order_reference_number", in.ReferenceNumber, ""},
		{"purchase_order_date", orNow(in.OrderDate), "::date"},
		{"purchase_order_expected_date", nullableDate(in.ExpectedDate), "::date"},
		{"purchase_order_sales_tax_percent", in.SalesTaxPercent, ""},
		{"purchase_order_memo", in.Memo, ""},
		{"purchase_order_notes", in.Notes, ""},
		{"purchase_order_internal_notes", in.InternalNotes, ""},
		{"purchase_order_terms_conditions", in.TermsConditions, ""},
		{"purchase_order_owner_id", nullableInt(ownerEmployeeID), ""},
		{"purchase_order_payment_terms", in.PaymentTermsID, ""},
		{"purchase_order_currency", in.CurrencyID, ""},
		{"purchase_order_subtotal", header.Subtotal, ""},
		{"purchase_order_discount_total", header.DiscountTotal, ""},
		{"purchase_order_tax_total", header.TaxTotal, ""},
		{"purchase_order_shipping_charge", in.ShippingCharge, ""},
		{"purchase_order_adjustment", in.Adjustment, ""},
		{"purchase_order_grand_total", header.GrandTotal, ""},
		{"purchase_order_custom_fields", custom, ""},
		{"purchase_order_created_by", nullableInt(actorEmployeeID), ""},
	}
	cv = append(cv, addrColVals(in.ShipTo)...)

	insertSQL, insertArgs := buildInsert("purchase_order", cv, "purchase_order_id, purchase_order_uuid")
	var internalID int
	var newUUID string
	err = tx.QueryRow(ctx, insertSQL, insertArgs...).Scan(&internalID, &newUUID)
	if err != nil {
		if isForeignKeyViolation(err) {
			return nil, ClientError{Msg: "One of the referenced ids (payment terms, currency, state, or country) does not exist."}
		}
		return nil, fmt.Errorf("insert purchase order: %w", err)
	}

	number := FormatNumber(int64(internalID))
	if _, err := tx.Exec(ctx,
		`UPDATE purchase_order SET purchase_order_number = $1 WHERE purchase_order_id = $2`, number, internalID); err != nil {
		return nil, fmt.Errorf("set purchase order number: %w", err)
	}

	if err := insertLines(ctx, tx, internalID, lines, actorEmployeeID); err != nil {
		return nil, err
	}

	writeHistory(ctx, tx, internalID, "create", nil, &draftStatusID, actorEmployeeID)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit create purchase order: %w", err)
	}
	return Get(ctx, pool, newUUID)
}
