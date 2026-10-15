package salesorder

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"stonesuite-backend/workflow"
)

// CreateOptions tunes CreateInTx for a caller that is not the staff form.
type CreateOptions struct {
	// DeferStockReservation skips the stock check and the reservation. Used for an
	// order drafted from a customer's form, whose lines are placeholders: a draft
	// the customer cannot submit because some item is short helps nobody, and the
	// first staff save reserves for the finished lines as usual.
	DeferStockReservation bool
	// ShippingAsGiven uses the input's Shipping block as it is instead of layering
	// it over the customer's own shipping address. For a caller whose ship-to is
	// somewhere else entirely (a job site), where an inherited suite number or
	// state would be wrong rather than helpful.
	ShippingAsGiven bool
}

// Created identifies the order CreateInTx inserted.
type Created struct {
	InternalID int
	UUID       string
	// DuplicateOfUUID is the live same-customer, same-PO order an AllowDuplicate
	// create went past; "" when there was none.
	DuplicateOfUUID string
}

// CreateInTx inserts a new sales order (header + lines + stock reservation +
// schedule + history) inside the caller's transaction and leaves committing,
// notifying and reading it back to the caller. Any error means the caller must
// roll the transaction back; nothing here commits.
func CreateInTx(ctx context.Context, tx pgx.Tx, in CreateOrderInput, actorEmployeeID int, opts CreateOptions) (*Created, error) {
	if strings.TrimSpace(in.CustomerUUID) == "" {
		return nil, ClientError{Msg: "A customer is required."}
	}
	if in.SalesTaxPercent < 0 || in.SalesTaxPercent > 100 {
		return nil, ClientError{Msg: "Sales tax percent must be between 0 and 100."}
	}
	// The installation date is checked up front (no DB needed). Blank is allowed
	// here — the HTTP layer is what makes it mandatory — and then no schedule is
	// planned until a later update supplies one.
	today := scheduleClock()
	installDate := strings.TrimSpace(in.InstallDate)
	var plannedInstall time.Time
	if installDate != "" {
		d, err := validateInstallDate(installDate, today)
		if err != nil {
			return nil, err
		}
		plannedInstall = d
	}
	// CustomFields is stored as-is, unvalidated: there is no admin-configurable
	// field-definition set for the relational sales_order module yet. It must
	// NOT be validated against the workflow keyed "sales_order" — that is an
	// unrelated legacy v1 JSONB placeholder workflow (schema.sql migration
	// 000010) whose field defs (customer_name required, etc.) predate this
	// module and collide only by sharing the same key string.

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
	switch {
	case in.ShipSameAsBilling:
		shipping = billing
	case opts.ShippingAsGiven:
		shipping = in.Shipping
	default:
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
		{"sales_order_scheduled_install_date", nullableDate(installDate), "::date"},
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
	// needs is held back from every other order from this moment. A caller that
	// builds the order from something other than a stocked-item pick (the portal's
	// order form) defers it: its lines are placeholders, and the first staff save
	// reserves what the finished lines really need.
	if !opts.DeferStockReservation {
		if err := reserveStock(ctx, tx, internalID, lines, lineIDs, actorEmployeeID); err != nil {
			return nil, err
		}
	}
	if installDate != "" {
		if err := syncSchedule(ctx, tx, internalID, planOrderDate(in.OrderDate, today), plannedInstall, today, true, actorEmployeeID); err != nil {
			return nil, err
		}
	}

	writeHistory(ctx, tx, internalID, "create", nil, &draftStatusID, actorEmployeeID)
	_ = custName // used only to build the snapshot above
	return &Created{InternalID: internalID, UUID: newUUID, DuplicateOfUUID: dupUUID}, nil
}
