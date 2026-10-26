package proposedentity

import (
	"context"
	"encoding/json"
	"fmt"

	"stonesuite-backend/authz"
	"stonesuite-backend/workflow"
)

// Entity kinds accepted by BuildPayload.
const (
	KindCustomer = "customer"
	KindVendor   = "vendor"
	KindItem     = "item"
)

// Document sides that decide which item price a line price fills.
const (
	SideSales    = "sales"
	SidePurchase = "purchase"
)

// ProposeInput is the extracted data a proposal is built from.
type ProposeInput struct {
	Kind           string
	SourceRef      string
	Name           string
	Address        string
	TaxID          string
	SKU            string
	Description    string
	DocUoM         string
	ContactEmail   string
	UnitPriceCents int64
	Side           string // SideSales | SidePurchase
}

// BuildPayload sanitizes in, resolves the unit for items, applies the price
// rules and validates the result with the matching executor. It returns the
// RBAC resource and JSON payload to hand to proposedaction.Store.Propose. The
// error is a *proposedaction.ValidationError that never contains payload values.
func BuildPayload(ctx context.Context, q workflow.Querier, in ProposeInput) (authz.Resource, json.RawMessage, error) {
	switch in.Kind {
	case KindCustomer:
		p := CustomerPayload{SourceRef: in.SourceRef, Name: in.Name, ContactEmail: in.ContactEmail, TaxID: in.TaxID, Address: in.Address}
		p.clean()
		return finish(ctx, authz.ResourceCustomer, p, customerExecutor{})
	case KindVendor:
		p := VendorPayload{SourceRef: in.SourceRef, Name: in.Name, Email: in.ContactEmail, Address: in.Address, TaxID: in.TaxID}
		p.clean()
		return finish(ctx, authz.ResourceVendor, p, vendorExecutor{})
	case KindItem:
		return buildItem(ctx, q, in)
	}
	return "", nil, invalid("unknown entity kind")
}

// buildItem fills the item payload, including the unit and price columns.
func buildItem(ctx context.Context, q workflow.Querier, in ProposeInput) (authz.Resource, json.RawMessage, error) {
	p := ItemPayload{SourceRef: in.SourceRef, SKU: in.SKU, Name: in.Name, Description: in.Description, DocUoM: in.DocUoM}
	switch in.Side {
	case SideSales:
		p.UnitPriceCents = in.UnitPriceCents
	case SidePurchase:
		p.PurchaseUnitPriceCents = in.UnitPriceCents
	default:
		return "", nil, invalid("unknown document side")
	}
	p.clean()
	id, code, err := ResolveUnit(ctx, q, p.DocUoM)
	if err != nil {
		return "", nil, fmt.Errorf("build item payload: %w", err)
	}
	p.UnitID, p.UnitCode = id, code
	return finish(ctx, authz.ResourceInventoryItem, p, itemExecutor{})
}

// validator is the subset of an executor BuildPayload needs.
type validator interface {
	Validate(ctx context.Context, payload json.RawMessage) error
}

// finish marshals p and validates it with the executor that will confirm it.
func finish[T CustomerPayload | VendorPayload | ItemPayload](ctx context.Context, res authz.Resource, p T, v validator) (authz.Resource, json.RawMessage, error) {
	raw, err := json.Marshal(p)
	if err != nil {
		return "", nil, fmt.Errorf("marshal proposed payload: %w", err)
	}
	if err := v.Validate(ctx, raw); err != nil {
		return "", nil, err
	}
	return res, raw, nil
}
