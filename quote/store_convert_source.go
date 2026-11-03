package quote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// estimateSnapshot is the subset of a source estimate's header the convert
// path copies verbatim — including its already-frozen billing/shipping
// snapshot (spec AD-4: a snapshot is copied forward, never re-derived from
// the live customer, so later customer edits don't rewrite the chain).
type estimateSnapshot struct {
	internalID                                  int
	customerInternalID                          int
	poNumber, referenceNumber                   string
	memo, notes, internalNotes, termsConditions string
	paymentTermsID, priceLevelID, currencyID    *int
	salesRepEmployeeID, ownerEmployeeID         *int
	salesTaxPercent, shippingCharge, adjustment float64
	shipSameAsBill                              bool
	billing, shipping                           AddressInput
	customFields                                map[string]any
}

// loadEstimateSnapshot loads a live estimate's header snapshot by external
// uuid inside tx. FOR SHARE serializes against estimate.SoftDelete's FOR UPDATE,
// so a delete can't slip between its conversion check and commit.
func loadEstimateSnapshot(ctx context.Context, tx pgx.Tx, estimateUUID string) (*estimateSnapshot, error) {
	var s estimateSnapshot
	var customRaw []byte
	err := tx.QueryRow(ctx, `
		SELECT estimate_id, estimate_customer_id,
		       estimate_po_number, estimate_reference_number,
		       estimate_memo, estimate_notes, estimate_internal_notes, estimate_terms_conditions,
		       estimate_payment_terms, estimate_price_level, estimate_currency,
		       estimate_sales_rep_id, estimate_owner_id, estimate_sales_tax_percent,
		       estimate_shipping_charge, estimate_adjustment, estimate_ship_same_as_bill,
		       estimate_bill_customer_name, estimate_bill_attention,
		       estimate_bill_addr_line1, estimate_bill_addr_line2, estimate_bill_addr_suitenum,
		       estimate_bill_addr_city, estimate_bill_addr_state, estimate_bill_addr_zip,
		       estimate_bill_addr_country, estimate_bill_phone, estimate_bill_fax, estimate_bill_email,
		       estimate_ship_customer_name, estimate_ship_attention,
		       estimate_ship_addr_line1, estimate_ship_addr_line2, estimate_ship_addr_suitenum,
		       estimate_ship_addr_city, estimate_ship_addr_state, estimate_ship_addr_zip,
		       estimate_ship_addr_country, estimate_ship_phone, estimate_ship_fax, estimate_ship_email,
		       estimate_custom_fields
		FROM estimate WHERE estimate_uuid = $1 AND estimate_deleted_at IS NULL
		FOR SHARE`, estimateUUID).Scan(
		&s.internalID, &s.customerInternalID,
		&s.poNumber, &s.referenceNumber,
		&s.memo, &s.notes, &s.internalNotes, &s.termsConditions,
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
		return nil, ErrEstimateNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load estimate snapshot: %w", err)
	}
	s.customFields = map[string]any{}
	if len(customRaw) > 0 {
		_ = json.Unmarshal(customRaw, &s.customFields)
	}
	return &s, nil
}
