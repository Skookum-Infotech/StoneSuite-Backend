package proposedentity

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"stonesuite-backend/authz"
	"stonesuite-backend/docextract"
	"stonesuite-backend/inventory"
	"stonesuite-backend/proposedaction"
)

// itemExecutor creates an inventory item through inventory.CreateTx.
type itemExecutor struct{}

// Resource returns the inventory item RBAC resource.
func (itemExecutor) Resource() authz.Resource { return authz.ResourceInventoryItem }

// Validate strictly decodes and checks the payload.
func (itemExecutor) Validate(_ context.Context, payload json.RawMessage) error {
	_, err := parseItem(payload)
	return err
}

// unitLabel renders the unit, noting when the document spelled it differently.
func unitLabel(p ItemPayload) string {
	code := p.UnitCode
	if code == "" {
		code = fmt.Sprintf("unit #%d", p.UnitID)
	}
	if p.DocUoM == "" {
		return code
	}
	if canon, _, ok := docextract.NormalizeUnit(p.DocUoM); ok && strings.EqualFold(p.UnitCode, canon) {
		return code
	}
	return fmt.Sprintf("%s (document said '%s')", code, p.DocUoM)
}

// Preview renders the human-readable summary.
func (itemExecutor) Preview(_ context.Context, payload json.RawMessage) (proposedaction.Preview, error) {
	p, err := parseItem(payload)
	if err != nil {
		return proposedaction.Preview{}, err
	}
	fields := []proposedaction.PreviewField{
		{Label: "SKU", Value: p.SKU}, {Label: "Name", Value: p.Name},
	}
	if p.Description != "" {
		fields = append(fields, proposedaction.PreviewField{Label: "Description", Value: p.Description})
	}
	fields = append(fields,
		proposedaction.PreviewField{Label: "Unit", Value: unitLabel(p)},
		proposedaction.PreviewField{Label: "Selling price", Value: formatDollars(p.UnitPriceCents)},
		proposedaction.PreviewField{Label: "Purchase price", Value: formatDollars(p.PurchaseUnitPriceCents)})
	return proposedaction.Preview{Title: "Create item '" + p.Name + "'", Summary: "New inventory item from an extracted document", Fields: fields}, nil
}

// InScope always allows: a create has no row-level scope.
func (itemExecutor) InScope(context.Context, proposedaction.Querier, authz.Scope, proposedaction.Caller, proposedaction.Kind, json.RawMessage) (bool, error) {
	return inScopeAlways()
}

// Execute creates the item and its audit row inside the confirm tx.
func (itemExecutor) Execute(ctx context.Context, tx pgx.Tx, caller proposedaction.Caller, payload json.RawMessage) (proposedaction.ResultRef, error) {
	p, err := parseItem(payload)
	if err != nil {
		return proposedaction.ResultRef{}, err
	}
	emp, err := actorEmployeeID(ctx, tx, caller.UserID)
	if err != nil {
		return proposedaction.ResultRef{}, err
	}
	sell, buy := centsToDollars(p.UnitPriceCents), centsToDollars(p.PurchaseUnitPriceCents)
	id, err := inventory.CreateTx(ctx, tx, inventory.CreateItemInput{
		SKU: p.SKU, Name: p.Name, Description: p.Description, UnitID: p.UnitID,
		UnitPrice: &sell, PurchaseUnitPrice: &buy,
	}, emp)
	if err != nil {
		return proposedaction.ResultRef{}, fmt.Errorf("create proposed item: %w", err)
	}
	snap := map[string]any{"id": id, "sku": p.SKU, "name": p.Name}
	if err := auditCreate(ctx, tx, caller, authz.ResourceInventoryItem, auditTableItem, id, p.SourceRef, snap); err != nil {
		return proposedaction.ResultRef{}, err
	}
	return proposedaction.ResultRef{Resource: string(authz.ResourceInventoryItem), RecordID: id, Label: p.Name}, nil
}
