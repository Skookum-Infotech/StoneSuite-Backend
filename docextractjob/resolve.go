package docextractjob

import (
	"context"
	"fmt"

	"stonesuite-backend/docextract"
	"stonesuite-backend/duplicate"
	"stonesuite-backend/workflow"
)

// Resolver maps extracted document text onto this tenant's master data. It
// runs on a tenant pool, so every lookup is tenant-scoped by construction.
type Resolver struct {
	db   workflow.Querier
	opts ResolveOptions
}

// ResolveOptions tunes resolution.
type ResolveOptions struct {
	// OwnCompanyNames are the tenant's own names; a customer PO names the
	// tenant as the vendor, so these are never matched as the customer.
	OwnCompanyNames []string
}

// NewResolver builds a Resolver over a tenant pool or transaction.
func NewResolver(db workflow.Querier, opts ResolveOptions) *Resolver {
	return &Resolver{db: db, opts: opts}
}

// Candidate is one possible match with its similarity score.
type Candidate struct {
	UUID   string  `json:"uuid"`
	Name   string  `json:"name"`
	Active bool    `json:"active"`
	Score  float64 `json:"score"`
}

// CustomerMatch is the resolved customer. UUID is empty when nothing could be
// matched (or when fuzzy candidates were too close to pick one).
type CustomerMatch struct {
	UUID       string                `json:"uuid,omitempty"`
	Name       string                `json:"name,omitempty"`
	Active     bool                  `json:"active"`
	StatusName string                `json:"statusName,omitempty"`
	Confidence docextract.Confidence `json:"confidence"`
	Source     docextract.Source     `json:"source"`
	Candidates []Candidate           `json:"candidates,omitempty"`
}

// ItemInfo is the catalog data of a matched item, so the form renders without
// per-line fetches.
type ItemInfo struct {
	UUID              string `json:"uuid"`
	Name              string `json:"name"`
	SKU               string `json:"sku"`
	UnitCode          string `json:"unitCode"`
	UnitCategory      string `json:"unitCategory"`
	CatalogPriceCents int64  `json:"catalogPriceCents"`
	Active            bool   `json:"active"`
}

// Line match methods.
const (
	MatchAlias = "alias"
	MatchSKU   = "sku"
	MatchName  = "name"
)

// Line flags raised by item resolution.
const (
	LineFlagPriceDiffers  = "price_differs_from_catalog"
	LineFlagItemInactive  = "item_inactive"
	LineFlagUoMUnknown    = "uom_unknown"
	LineFlagUoMMismatch   = "uom_mismatch"
	LineFlagItemAmbiguous = "item_ambiguous"
	LineFlagItemUnmatched = "item_unmatched"
)

// UnitConversion records a document-to-catalog unit conversion. The line
// amount is unchanged: qty and unit price are scaled inversely.
type UnitConversion struct {
	FromUoM        string `json:"fromUom"`
	ToUoM          string `json:"toUom"`
	FromQtyMilli   int64  `json:"fromQtyMilli"`
	QtyMilli       int64  `json:"qtyMilli"`
	UnitPriceCents int64  `json:"unitPriceCents"`
}

// LineMatch is the resolution of one document line (Index into Result.Lines).
type LineMatch struct {
	Index     int               `json:"index"`
	Item      *ItemInfo         `json:"item,omitempty"`
	MatchedBy string            `json:"matchedBy,omitempty"`
	Source    docextract.Source `json:"source,omitempty"`
	Flags     []string          `json:"flags,omitempty"`
	Converted *UnitConversion   `json:"converted,omitempty"`
}

// TermsMatch is a payment-terms lookup by name.
type TermsMatch struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Code string `json:"code"`
}

// Resolution is everything resolved server-side for one extraction.
type Resolution struct {
	Customer     CustomerMatch `json:"customer"`
	PaymentTerms *TermsMatch   `json:"paymentTerms,omitempty"`
	Lines        []LineMatch   `json:"lines"`
}

// Duplicate is one duplicate / revision / reference finding.
type Duplicate struct {
	Kind       string `json:"kind"`
	RecordUUID string `json:"recordUuid,omitempty"`
	Number     string `json:"number,omitempty"`
	Status     string `json:"status,omitempty"`
	Reason     string `json:"reason"`
	// OwnerUserID is the record's tenant users.id. It is persisted in the
	// stored result (server-side) so the controller can apply the caller's read
	// scope at read time before exposing RecordUUID; the controller blanks it
	// from every response, so it is never sent to a client.
	OwnerUserID string `json:"ownerUserId,omitempty"`
}

// ResultDoc is the JSON stored in document_extractions.result.
type ResultDoc struct {
	Extracted  docextract.Result `json:"extracted"`
	Resolution Resolution        `json:"resolution"`
	Duplicates []Duplicate       `json:"duplicates"`
}

// Resolve resolves the customer, payment terms and line items of res. The
// customer and each line item are looked up in batched queries (one per kind).
func (r *Resolver) Resolve(ctx context.Context, res docextract.Result) (Resolution, error) {
	cust, err := r.ResolveCustomer(ctx, res.Header.CustomerName.Value)
	if err != nil {
		return Resolution{}, fmt.Errorf("resolve customer: %w", err)
	}
	out := Resolution{Customer: cust}
	if out.PaymentTerms, err = r.ResolvePaymentTerms(ctx, res.Header.PaymentTerms.Value); err != nil {
		return Resolution{}, fmt.Errorf("resolve payment terms: %w", err)
	}
	if out.Lines, err = r.ResolveItems(ctx, cust.UUID, res.Lines); err != nil {
		return Resolution{}, fmt.Errorf("resolve items: %w", err)
	}
	return out, nil
}

// isOwnCompany reports whether name is one of the tenant's own names.
func (r *Resolver) isOwnCompany(name string) bool {
	key := duplicate.Key(name)
	for _, own := range r.opts.OwnCompanyNames {
		if k := duplicate.Key(own); k != "" && k == key {
			return true
		}
	}
	return false
}
