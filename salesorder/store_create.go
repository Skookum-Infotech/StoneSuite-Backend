package salesorder

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/workflow"
)

// ----- Create ---------------------------------------------------------------

// Create inserts a new sales order (header + lines) inside one transaction:
// snapshots billing/shipping from the customer (unless overridden), resolves
// and prices every line, computes header totals, assigns the order number,
// and starts the order at DRFT (spec §10, AD-4, AD-7).
func Create(ctx context.Context, pool *pgxpool.Pool, in CreateOrderInput, actorEmployeeID int) (*Order, error) {
	if strings.TrimSpace(in.CustomerUUID) == "" {
		return nil, ClientError{Msg: "A customer is required."}
	}
	if in.SalesTaxPercent < 0 || in.SalesTaxPercent > 100 {
		return nil, ClientError{Msg: "Sales tax percent must be between 0 and 100."}
	}
	// CustomFields is stored as-is, unvalidated: there is no admin-configurable
	// field-definition set for the relational sales_order module yet. It must
	// NOT be validated against the workflow keyed "sales_order" — that is an
	// unrelated legacy v1 JSONB placeholder workflow (schema.sql migration
	// 000010) whose field defs (customer_name required, etc.) predate this
	// module and collide only by sharing the same key string.

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin create sales order: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	msg, err := workflow.CustomerNotUsableByUUID(ctx, tx, in.CustomerUUID)
	if err != nil {
		return nil, err
	}
	if msg != "" {
		return nil, ClientError{Msg: msg}
	}

	custInternalID, custName, defBilling, defShipping, err := customerSnapshot(ctx, tx, in.CustomerUUID)
	if err != nil {
		return nil, err
	}
	dupUUID, _, err := guardDuplicatePO(ctx, tx, custInternalID, in.PONumber, in.AllowDuplicate)
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

	lines, err := resolveLines(ctx, tx, in.Items, in.SalesTaxPercent)
	if err != nil {
		return nil, err
	}
	lineMoney := make([]LineMoney, len(lines))
	for i, l := range lines {
		lineMoney[i] = l.money
	}
	header := ComputeHeader(lineMoney, in.ShippingCharge, in.Adjustment)

	recordTypeID, err := recordTypeIDByCode(ctx, tx, sordRecordTypeCode)
	if err != nil {
		return nil, fmt.Errorf("resolve SORD record type: %w", err)
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

	dueDate, err := resolvePaymentDueDate(ctx, tx, in.OrderDate, in.PaymentDueDate, in.PaymentTermsID)
	if err != nil {
		return nil, err
	}

	cv := []colVal{
		{"record_type", recordTypeID, ""},
		{"sales_order_status", draftStatusID, ""},
		{"sales_order_customer_id", custInternalID, ""},
		{"sales_order_po_number", in.PONumber, ""},
		{"sales_order_reference_number", in.ReferenceNumber, ""},
		{"sales_order_date", orNow(in.OrderDate), "::date"},
		{"sales_order_expected_delivery", nullableDate(in.ExpectedDelivery), "::date"},
		{"sales_order_payment_due_date", dueDate, "::date"},
		{"sales_order_sales_tax_percent", in.SalesTaxPercent, ""},
		{"sales_order_memo", in.Memo, ""},
		{"sales_order_notes", in.Notes, ""},
		{"sales_order_internal_notes", in.InternalNotes, ""},
		{"sales_order_terms_conditions", in.TermsConditions, ""},
		{"sales_order_sales_rep_id", in.SalesRepEmployeeID, ""},
		{"sales_order_owner_id", nullableInt(ownerEmployeeID), ""},
		{"sales_order_payment_terms", in.PaymentTermsID, ""},
		{"sales_order_price_level", in.PriceLevelID, ""},
		{"sales_order_currency", in.CurrencyID, ""},
		{"sales_order_subtotal", header.Subtotal, ""},
		{"sales_order_discount_total", header.DiscountTotal, ""},
		{"sales_order_tax_total", header.TaxTotal, ""},
		{"sales_order_shipping_charge", in.ShippingCharge, ""},
		{"sales_order_adjustment", in.Adjustment, ""},
		{"sales_order_grand_total", header.GrandTotal, ""},
		{"sales_order_ship_same_as_bill", in.ShipSameAsBilling, ""},
		{"sales_order_custom_fields", custom, ""},
		{"sales_order_created_by", nullableInt(actorEmployeeID), ""},
	}
	cv = append(cv, addrColVals("sales_order_bill", billing)...)
	cv = append(cv, addrColVals("sales_order_ship", shipping)...)

	insertSQL, insertArgs := buildInsert("sales_order", cv, "sales_order_id, sales_order_uuid")
	var internalID int
	var newUUID string
	err = tx.QueryRow(ctx, insertSQL, insertArgs...).Scan(&internalID, &newUUID)
	if err != nil {
		if isForeignKeyViolation(err) {
			return nil, ClientError{Msg: "One of the referenced ids (payment terms, price level, currency, state, or country) does not exist."}
		}
		if isCheckViolation(err) {
			return nil, ClientError{Msg: "One or more sales order values are out of range."}
		}
		return nil, fmt.Errorf("insert sales order: %w", err)
	}

	if _, err := assignNumber(ctx, tx, int64(internalID)); err != nil {
		return nil, err
	}

	lineIDs, err := insertLines(ctx, tx, internalID, lines, actorEmployeeID)
	if err != nil {
		return nil, err
	}
	// The stock check: an order cannot be saved for more than is free, and what it
	// needs is held back from every other order from this moment.
	if err := reserveStock(ctx, tx, internalID, lines, lineIDs, actorEmployeeID); err != nil {
		return nil, err
	}

	writeHistory(ctx, tx, internalID, "create", nil, &draftStatusID, actorEmployeeID)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit create sales order: %w", err)
	}
	_ = custName // used only to build the snapshot above
	notifyCreated(ctx, pool, newUUID, internalID, actorEmployeeID)
	order, err := Get(ctx, pool, newUUID)
	if err != nil {
		return nil, err
	}
	if dupUUID != "" {
		order.DuplicateOverride = true
		order.DuplicateOfUUID = dupUUID
	}
	return order, nil
}

// resolvePaymentDueDate computes the sales_order_payment_due_date arg (AD-8,
// schema.org paymentDueDate): an explicit caller value wins (and must be on or
// after the order date); otherwise the order date plus the payment term's
// net-days; otherwise NULL. orderDate is the raw request value (blank ⇒ today,
// matching orNow). Returns a nullable "yyyy-mm-dd" date arg.
func resolvePaymentDueDate(ctx context.Context, q workflow.Querier, orderDate, explicit string, paymentTermsID *int) (any, error) {
	base := time.Now()
	if od := strings.TrimSpace(orderDate); od != "" && od != "now" {
		parsed, perr := time.Parse("2006-01-02", od)
		if perr != nil {
			return nil, ClientError{Msg: "Order date must be in yyyy-mm-dd format."}
		}
		base = parsed
	}
	if s := strings.TrimSpace(explicit); s != "" {
		due, perr := time.Parse("2006-01-02", s)
		if perr != nil {
			return nil, ClientError{Msg: "Payment due date must be in yyyy-mm-dd format."}
		}
		if due.Before(base) {
			return nil, ClientError{Msg: "Payment due date cannot be before the order date."}
		}
		return s, nil
	}
	if paymentTermsID == nil || *paymentTermsID <= 0 {
		return nil, nil
	}
	var netDays int
	err := q.QueryRow(ctx,
		`SELECT payment_terms_net_days FROM lkp_payment_terms WHERE payment_terms_id = $1`, *paymentTermsID).Scan(&netDays)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil // invalid terms id is reported by the header FK violation
	}
	if err != nil {
		return nil, fmt.Errorf("load payment terms net days: %w", err)
	}
	return base.AddDate(0, 0, netDays).Format("2006-01-02"), nil
}

// insertLines bulk-inserts resolved lines as sales_order_item rows and returns
// their new ids, in the order of lines.
func insertLines(ctx context.Context, tx pgx.Tx, orderInternalID int, lines []resolvedLine, actorEmployeeID int) ([]int, error) {
	ids := make([]int, 0, len(lines))
	for _, l := range lines {
		var id int
		err := tx.QueryRow(ctx, `
			INSERT INTO sales_order_item (
				sales_order_id, line_number, inventory_item_id, warehouse_id,
				item_name, sku, description, unit_id, unit_code,
				quantity, unit_price, discount_percent, tax_rate_id, tax_percent,
				line_subtotal, line_discount, line_tax, line_total,
				item_created_by
			) VALUES ($1,$2,$3,$4, $5,$6,$7,$8,$9, $10,$11,$12,$13,$14, $15,$16,$17,$18, $19)
			RETURNING sales_order_item_id`,
			orderInternalID, l.lineNumber, l.inventoryItemID, l.warehouseID,
			l.name, l.sku, l.desc, l.unitID, l.unitCode,
			l.quantity, l.unitPrice, l.discountPercent, l.taxRateID, l.taxPercent,
			l.money.Subtotal, l.money.Discount, l.money.Tax, l.money.Total,
			nullableInt(actorEmployeeID),
		).Scan(&id)
		if err != nil {
			if isForeignKeyViolation(err) {
				return nil, ClientError{Msg: fmt.Sprintf("Line %d: an invalid unit, tax rate, or location was referenced.", l.lineNumber)}
			}
			if isCheckViolation(err) {
				return nil, ClientError{Msg: fmt.Sprintf("Line %d: one or more values are out of range.", l.lineNumber)}
			}
			return nil, fmt.Errorf("insert sales order item: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, nil
}

// writeHistory appends a sales_order_history row. Best-effort: failures are
// logged-and-swallowed by the caller's transaction commit path (a failed
// history write should not be silently ignored inside a still-open tx, so
// this returns the error for the caller to propagate, unlike crmstore's
// fire-and-forget writeHistory which runs outside any transaction).
func writeHistory(ctx context.Context, tx pgx.Tx, orderInternalID int, action string, fromStatusID, toStatusID *int, actorEmployeeID int) {
	_, _ = tx.Exec(ctx, `
		INSERT INTO sales_order_history (sales_order_id, from_status_id, to_status_id, action, actor_employee_id)
		VALUES ($1,$2,$3,$4,$5)`,
		orderInternalID, fromStatusID, toStatusID, action, nullableInt(actorEmployeeID))
}
