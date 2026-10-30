// estimate/store_create.go
package estimate

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/jackc/pgx/v5/pgxpool"
	"stonesuite-backend/idempotency"
	"stonesuite-backend/workflow"
)

// writeHistory records one estimate_history row inside the caller's transaction.
func writeHistory(ctx context.Context, tx pgx.Tx, estimateInternalID int, action string, fromStatusID, toStatusID *int, actorEmployeeID int) {
	_, _ = tx.Exec(ctx, `
		INSERT INTO estimate_history (estimate_id, from_status_id, to_status_id, action, actor_employee_id)
		VALUES ($1,$2,$3,$4,$5)`,
		estimateInternalID, fromStatusID, toStatusID, action, nullableInt(actorEmployeeID))
}

// Create inserts a new estimate (header + lines) inside one transaction:
// snapshots billing/shipping from the customer (unless overridden), resolves
// and prices every line, computes header totals, assigns the estimate
// number, and starts the estimate at DRFT (spec §5.1, AD-4, AD-7, AD-11).
func Create(ctx context.Context, pool *pgxpool.Pool, in CreateEstimateInput, actorEmployeeID int) (*Estimate, error) {
	if strings.TrimSpace(in.CustomerUUID) == "" {
		return nil, ClientError{Msg: "A customer is required."}
	}
	// A retry whose first attempt already committed gets that estimate back
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
	if err := validateWriteInput(len(in.Items), in.ShippingCharge, in.Adjustment, effectiveDate(in.EstimateDate), in.ValidUntil); err != nil {
		return nil, err
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin create estimate: %w", err)
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
		return nil, ClientError{Msg: "The estimate total cannot be negative."}
	}

	recordTypeID, err := recordTypeIDByCode(ctx, tx, estmRecordTypeCode)
	if err != nil {
		return nil, fmt.Errorf("resolve ESTM record type: %w", err)
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
		{"estimate_status", draftStatusID, ""},
		{"estimate_customer_id", custInternalID, ""},
		{"estimate_po_number", in.PONumber, ""},
		{"estimate_reference_number", in.ReferenceNumber, ""},
		{"estimate_date", orNow(in.EstimateDate), "::date"},
		{"estimate_valid_until", nullableDate(in.ValidUntil), "::date"},
		{"estimate_sales_tax_percent", in.SalesTaxPercent, ""},
		{"estimate_memo", in.Memo, ""},
		{"estimate_notes", in.Notes, ""},
		{"estimate_internal_notes", in.InternalNotes, ""},
		{"estimate_terms_conditions", in.TermsConditions, ""},
		{"estimate_sales_rep_id", in.SalesRepEmployeeID, ""},
		{"estimate_owner_id", nullableInt(ownerEmployeeID), ""},
		{"estimate_payment_terms", in.PaymentTermsID, ""},
		{"estimate_price_level", in.PriceLevelID, ""},
		{"estimate_currency", in.CurrencyID, ""},
		{"estimate_subtotal", header.Subtotal, ""},
		{"estimate_discount_total", header.DiscountTotal, ""},
		{"estimate_tax_total", header.TaxTotal, ""},
		{"estimate_shipping_charge", in.ShippingCharge, ""},
		{"estimate_adjustment", in.Adjustment, ""},
		{"estimate_grand_total", header.GrandTotal, ""},
		{"estimate_ship_same_as_bill", in.ShipSameAsBilling, ""},
		{"estimate_custom_fields", custom, ""},
		{"estimate_created_by", nullableInt(actorEmployeeID), ""},
	}
	cv = append(cv, addrColVals("estimate_bill", billing)...)
	cv = append(cv, addrColVals("estimate_ship", shipping)...)

	insertSQL, insertArgs := buildInsert("estimate", cv, "estimate_id, estimate_uuid")
	var internalID int
	var newUUID string
	err = tx.QueryRow(ctx, insertSQL, insertArgs...).Scan(&internalID, &newUUID)
	if err != nil {
		if isForeignKeyViolation(err) {
			return nil, ClientError{Msg: "One of the referenced ids (payment terms, price level, currency, state, or country) does not exist."}
		}
		return nil, fmt.Errorf("insert estimate: %w", err)
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
		return nil, fmt.Errorf("commit create estimate: %w", err)
	}
	_ = custName
	notifyCreated(ctx, pool, newUUID, internalID, actorEmployeeID)
	return Get(ctx, pool, newUUID)
}
