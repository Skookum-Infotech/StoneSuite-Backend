// quote/store_create.go
package quote

import (
	"context"
	"errors"
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
// (or free text), computing each line's stored money (spec §8). headerTax is
// the header's default tax percent, used when a line has no tax rate.
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

// insertLines bulk-inserts resolved lines as quote_item rows.
func insertLines(ctx context.Context, tx pgx.Tx, quoteInternalID int, lines []resolvedLine, actorEmployeeID int) error {
	for _, l := range lines {
		_, err := tx.Exec(ctx, `
			INSERT INTO quote_item (
				quote_id, line_number, inventory_item_id,
				item_name, sku, description, unit_id, unit_code,
				quantity, unit_price, discount_percent, tax_rate_id, tax_percent,
				line_subtotal, line_discount, line_tax, line_total,
				item_created_by
			) VALUES ($1,$2,$3, $4,$5,$6,$7,$8, $9,$10,$11,$12,$13, $14,$15,$16,$17, $18)`,
			quoteInternalID, l.lineNumber, l.inventoryItemID,
			l.name, l.sku, l.desc, l.unitID, l.unitCode,
			l.quantity, l.unitPrice, l.discountPercent, l.taxRateID, l.taxPercent,
			l.money.Subtotal, l.money.Discount, l.money.Tax, l.money.Total,
			nullableInt(actorEmployeeID),
		)
		if err != nil {
			if isForeignKeyViolation(err) {
				return ClientError{Msg: fmt.Sprintf("Line %d: an invalid unit or tax rate was referenced.", l.lineNumber)}
			}
			return fmt.Errorf("insert quote item: %w", err)
		}
	}
	return nil
}

// writeHistory records one quote_history row inside the caller's transaction.
func writeHistory(ctx context.Context, tx pgx.Tx, quoteInternalID int, action string, fromStatusID, toStatusID *int, actorEmployeeID int) {
	_, _ = tx.Exec(ctx, `
		INSERT INTO quote_history (quote_id, from_status_id, to_status_id, action, actor_employee_id)
		VALUES ($1,$2,$3,$4,$5)`,
		quoteInternalID, fromStatusID, toStatusID, action, nullableInt(actorEmployeeID))
}

// Create inserts a new quote (header + lines) inside one transaction:
// snapshots billing/shipping from the customer (unless overridden), resolves
// and prices every line, computes header totals, assigns the quote
// number, and starts the quote at DRFT (spec §5.1, AD-4, AD-7, AD-11).
func Create(ctx context.Context, pool *pgxpool.Pool, in CreateQuoteInput, actorEmployeeID int) (*Quote, error) {
	if strings.TrimSpace(in.CustomerUUID) == "" {
		return nil, ClientError{Msg: "A customer is required."}
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin create quote: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	custInternalID, custName, defBilling, defShipping, err := customerSnapshot(ctx, tx, in.CustomerUUID)
	if err != nil {
		return nil, err
	}
	billing := overrideAddress(defBilling, in.Billing)
	var shipping AddressInput
	if in.ShipSameAsBilling {
		shipping = billing
	} else {
		shipping = overrideAddress(defShipping, in.Shipping)
	}

	var estimateInternalID *int
	if in.EstimateUUID != nil && *in.EstimateUUID != "" {
		var eid int
		err := tx.QueryRow(ctx, `SELECT estimate_id FROM estimate WHERE estimate_uuid = $1 AND estimate_deleted_at IS NULL`, *in.EstimateUUID).Scan(&eid)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, ClientError{Msg: "Referenced estimate not found."}
			}
			return nil, fmt.Errorf("resolve estimate: %w", err)
		}
		estimateInternalID = &eid
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

	recordTypeID, err := recordTypeIDByCode(ctx, tx, quotRecordTypeCode)
	if err != nil {
		return nil, fmt.Errorf("resolve QUOT record type: %w", err)
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
		{"quote_status", draftStatusID, ""},
		{"quote_customer_id", custInternalID, ""},
		{"quote_estimate_id", estimateInternalID, ""},
		{"quote_po_number", in.PONumber, ""},
		{"quote_reference_number", in.ReferenceNumber, ""},
		{"quote_date", orNow(in.QuoteDate), "::date"},
		{"quote_valid_until", nullableDate(in.ValidUntil), "::date"},
		{"quote_sales_tax_percent", in.SalesTaxPercent, ""},
		{"quote_memo", in.Memo, ""},
		{"quote_notes", in.Notes, ""},
		{"quote_internal_notes", in.InternalNotes, ""},
		{"quote_terms_conditions", in.TermsConditions, ""},
		{"quote_sales_rep_id", in.SalesRepEmployeeID, ""},
		{"quote_owner_id", nullableInt(ownerEmployeeID), ""},
		{"quote_payment_terms", in.PaymentTermsID, ""},
		{"quote_price_level", in.PriceLevelID, ""},
		{"quote_currency", in.CurrencyID, ""},
		{"quote_subtotal", header.Subtotal, ""},
		{"quote_discount_total", header.DiscountTotal, ""},
		{"quote_tax_total", header.TaxTotal, ""},
		{"quote_shipping_charge", in.ShippingCharge, ""},
		{"quote_adjustment", in.Adjustment, ""},
		{"quote_grand_total", header.GrandTotal, ""},
		{"quote_ship_same_as_bill", in.ShipSameAsBilling, ""},
		{"quote_custom_fields", custom, ""},
		{"quote_created_by", nullableInt(actorEmployeeID), ""},
	}
	cv = append(cv, addrColVals("quote_bill", billing)...)
	cv = append(cv, addrColVals("quote_ship", shipping)...)

	insertSQL, insertArgs := buildInsert("quote", cv, "quote_id, quote_uuid")
	var internalID int
	var newUUID string
	err = tx.QueryRow(ctx, insertSQL, insertArgs...).Scan(&internalID, &newUUID)
	if err != nil {
		if isForeignKeyViolation(err) {
			return nil, ClientError{Msg: "One of the referenced ids (payment terms, price level, currency, state, or country) does not exist."}
		}
		return nil, fmt.Errorf("insert quote: %w", err)
	}

	number := FormatNumber(int64(internalID))
	if _, err := tx.Exec(ctx,
		`UPDATE quote SET quote_number = $1 WHERE quote_id = $2`, number, internalID); err != nil {
		return nil, fmt.Errorf("set quote number: %w", err)
	}

	if err := insertLines(ctx, tx, internalID, lines, actorEmployeeID); err != nil {
		return nil, err
	}

	writeHistory(ctx, tx, internalID, "create", nil, &draftStatusID, actorEmployeeID)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit create quote: %w", err)
	}
	_ = custName
	return Get(ctx, pool, newUUID)
}
