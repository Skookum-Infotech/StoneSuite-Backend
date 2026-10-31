package salesorder

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// quoteSnapshot is the subset of a source quote's header the convert path
// copies verbatim — including its already-frozen billing/shipping snapshot
// (spec AD-4: a snapshot is copied forward, never re-derived from the live
// customer, so later customer edits don't rewrite the chain).
type quoteSnapshot struct {
	internalID           int
	customerInternalID   int
	poNumber, refNumber  string
	memo, notes          string
	internalNotes, terms string
	paymentTermsID       *int
	priceLevelID         *int
	currencyID           *int
	salesRepEmployeeID   *int
	ownerEmployeeID      *int
	salesTaxPercent      float64
	shippingCharge       float64
	adjustment           float64
	shipSameAsBill       bool
	billing, shipping    AddressInput
	customFields         map[string]any
}

// loadQuoteSnapshot loads a live quote's header snapshot by external uuid
// inside tx. FOR SHARE serializes against quote.SoftDelete's FOR UPDATE.
func loadQuoteSnapshot(ctx context.Context, tx pgx.Tx, quoteUUID string) (*quoteSnapshot, error) {
	var s quoteSnapshot
	var customRaw []byte
	err := tx.QueryRow(ctx, `
		SELECT quote_id, quote_customer_id,
		       quote_po_number, quote_reference_number,
		       quote_memo, quote_notes, quote_internal_notes, quote_terms_conditions,
		       quote_payment_terms, quote_price_level, quote_currency,
		       quote_sales_rep_id, quote_owner_id, quote_sales_tax_percent,
		       quote_shipping_charge, quote_adjustment, quote_ship_same_as_bill,
		       quote_bill_customer_name, quote_bill_attention,
		       quote_bill_addr_line1, quote_bill_addr_line2, quote_bill_addr_suitenum,
		       quote_bill_addr_city, quote_bill_addr_state, quote_bill_addr_zip,
		       quote_bill_addr_country, quote_bill_phone, quote_bill_fax, quote_bill_email,
		       quote_ship_customer_name, quote_ship_attention,
		       quote_ship_addr_line1, quote_ship_addr_line2, quote_ship_addr_suitenum,
		       quote_ship_addr_city, quote_ship_addr_state, quote_ship_addr_zip,
		       quote_ship_addr_country, quote_ship_phone, quote_ship_fax, quote_ship_email,
		       quote_custom_fields
		FROM quote WHERE quote_uuid = $1 AND quote_deleted_at IS NULL
		FOR SHARE`, quoteUUID).Scan(
		&s.internalID, &s.customerInternalID,
		&s.poNumber, &s.refNumber,
		&s.memo, &s.notes, &s.internalNotes, &s.terms,
		&s.paymentTermsID, &s.priceLevelID, &s.currencyID,
		&s.salesRepEmployeeID, &s.ownerEmployeeID, &s.salesTaxPercent,
		&s.shippingCharge, &s.adjustment, &s.shipSameAsBill,
		&s.billing.CustomerName, &s.billing.Attention,
		&s.billing.AddrLine1, &s.billing.AddrLine2, &s.billing.SuiteUnit,
		&s.billing.City, &s.billing.StateID, &s.billing.Zip,
		&s.billing.CountryID, &s.billing.Phone, &s.billing.Fax, &s.billing.Email,
		&s.shipping.CustomerName, &s.shipping.Attention,
		&s.shipping.AddrLine1, &s.shipping.AddrLine2, &s.shipping.SuiteUnit,
		&s.shipping.City, &s.shipping.StateID, &s.shipping.Zip,
		&s.shipping.CountryID, &s.shipping.Phone, &s.shipping.Fax, &s.shipping.Email,
		&customRaw,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrQuoteNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load quote snapshot: %w", err)
	}
	s.customFields = map[string]any{}
	if len(customRaw) > 0 {
		_ = json.Unmarshal(customRaw, &s.customFields)
	}
	return &s, nil
}
