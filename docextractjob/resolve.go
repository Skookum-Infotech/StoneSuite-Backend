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

// PartyMatch is a resolved customer or vendor; the shapes are identical.
type PartyMatch = CustomerMatch

// LinkedPOLine is one line of the purchase order a vendor bill references,
// for tying the bill's lines to it (purchaseOrderItemId on save).
type LinkedPOLine struct {
	ID                string `json:"id"` // purchase_order_item uuid
	LineNumber        int    `json:"lineNumber"`
	InventoryItemUUID string `json:"inventoryItemUuid,omitempty"`
	Description       string `json:"description"`
	QtyMilli          int64  `json:"qtyMilli"`
	// BillableQtyMilli is received and not yet billed: what a bill may still
	// claim against this line without over-billing.
	BillableQtyMilli int64 `json:"billableQtyMilli"`
	UnitPriceCents   int64 `json:"unitPriceCents"`
}

// LinkedPO is the purchase order a vendor bill's PO reference resolved to.
// Billable is false when the order has received nothing (or everything
// received is already billed): the bill may still reference it, but its
// lines cannot link. Reason explains a refused link for the review screen.
type LinkedPO struct {
	UUID     string         `json:"uuid,omitempty"`
	Number   string         `json:"number,omitempty"`
	Status   string         `json:"status,omitempty"`
	Billable bool           `json:"billable"`
	Reason   string         `json:"reason,omitempty"`
	Lines    []LinkedPOLine `json:"lines"`
}

// Resolution is everything resolved server-side for one extraction. A sales
// order resolves Customer; a purchase order or vendor bill resolves Vendor
// (and a bill its LinkedPO). The party not used by the doc type is zero.
type Resolution struct {
	Customer     CustomerMatch `json:"customer"`
	Vendor       PartyMatch    `json:"vendor"`
	LinkedPO     *LinkedPO     `json:"linkedPurchaseOrder,omitempty"`
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
	// ProposedEntities are customers/vendors/items the document names that
	// master data lacks (see DeriveProposedEntities).
	ProposedEntities []ProposedEntity `json:"proposedEntities"`
}

// Resolve resolves the party, payment terms and line items of res. A sales
// order resolves its customer; a purchase order or vendor bill resolves its
// vendor (and a bill the purchase order it references). Each lookup kind is
// batched into one query.
func (r *Resolver) Resolve(ctx context.Context, res docextract.Result) (Resolution, error) {
	switch res.DocType {
	case docextract.DocTypePurchaseOrder, docextract.DocTypeVendorBill:
		return r.resolveVendorDoc(ctx, res)
	default:
		return r.resolveSalesOrder(ctx, res)
	}
}

// resolveSalesOrder resolves a sales order's customer, terms and items.
func (r *Resolver) resolveSalesOrder(ctx context.Context, res docextract.Result) (Resolution, error) {
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

// resolveVendorDoc resolves a purchase order or vendor bill: the vendor, the
// payment terms, the lines (item aliases are keyed by the vendor uuid) and, for
// a bill, the purchase order its PO reference points at.
func (r *Resolver) resolveVendorDoc(ctx context.Context, res docextract.Result) (Resolution, error) {
	vendor, err := r.ResolveVendor(ctx, res.Header.VendorName.Value)
	if err != nil {
		return Resolution{}, fmt.Errorf("resolve vendor: %w", err)
	}
	out := Resolution{Vendor: vendor}
	if out.PaymentTerms, err = r.ResolvePaymentTerms(ctx, res.Header.PaymentTerms.Value); err != nil {
		return Resolution{}, fmt.Errorf("resolve payment terms: %w", err)
	}
	if out.Lines, err = r.ResolveItems(ctx, vendor.UUID, res.Lines); err != nil {
		return Resolution{}, fmt.Errorf("resolve items: %w", err)
	}
	if res.DocType == docextract.DocTypeVendorBill {
		if out.LinkedPO, err = r.ResolveLinkedPO(ctx, vendor.UUID, res.Header.PONumber.Value); err != nil {
			return Resolution{}, fmt.Errorf("resolve linked purchase order: %w", err)
		}
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
