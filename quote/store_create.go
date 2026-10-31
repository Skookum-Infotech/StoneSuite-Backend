// quote/store_create.go
package quote

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/jackc/pgx/v5/pgxpool"
	"stonesuite-backend/idempotency"
	"stonesuite-backend/workflow"
)

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
	// A retry whose first attempt already committed gets that quote back
	// before any validation: the customer may have changed since, and a
	// replay must answer like the original, not re-judge it.
	if in.IdempotencyKey != "" {
		prior, err := idempotency.Lookup(ctx, pool, idempotencyScopeCreate, in.IdempotencyKey, in.RequestHash)
		if err != nil {
			return nil, err
		}
		if prior != "" {
			return Get(ctx, pool, prior)
		}
	}
	if err := validateWriteInput(len(in.Items), in.ShippingCharge, in.Adjustment, effectiveDate(in.QuoteDate), in.ValidUntil); err != nil {
		return nil, err
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin create quote: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	// Claim inside the create's transaction: a concurrent duplicate blocks
	// here until this one commits, then replays it instead of creating.
	if in.IdempotencyKey != "" {
		prior, err := idempotency.Claim(ctx, tx, idempotencyScopeCreate, in.IdempotencyKey, in.RequestHash)
		if err != nil {
			return nil, err
		}
		if prior != "" {
			_ = tx.Rollback(ctx)
			return Get(ctx, pool, prior)
		}
	}

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
		err := tx.QueryRow(ctx, `SELECT estimate_id FROM estimate WHERE estimate_uuid = $1 AND estimate_deleted_at IS NULL FOR SHARE`, *in.EstimateUUID).Scan(&eid)
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
	if header.GrandTotal < 0 {
		return nil, ClientError{Msg: "The quote total cannot be negative."}
	}

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

	if _, err := assignNumber(ctx, tx, int64(internalID)); err != nil {
		return nil, err
	}
	if in.IdempotencyKey != "" {
		if err := idempotency.Complete(ctx, tx, idempotencyScopeCreate, in.IdempotencyKey, newUUID); err != nil {
			return nil, err
		}
	}

	if err := insertLines(ctx, tx, internalID, lines, actorEmployeeID); err != nil {
		return nil, err
	}

	writeHistory(ctx, tx, internalID, "create", nil, &draftStatusID, actorEmployeeID)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit create quote: %w", err)
	}
	_ = custName
	notifyCreated(ctx, pool, newUUID, internalID, actorEmployeeID)
	return Get(ctx, pool, newUUID)
}
