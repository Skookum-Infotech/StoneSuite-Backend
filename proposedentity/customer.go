package proposedentity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"stonesuite-backend/authz"
	"stonesuite-backend/crmstore"
	"stonesuite-backend/proposedaction"
	"stonesuite-backend/tenancy"
)

// Customer core-field keys understood by crmstore.
const (
	coreCustomerName  = "customer_name"
	coreCustomerEmail = "customer_contact_email"
	coreCustomerTaxID = "customer_tax_id"
)

// addressNotSaved labels the customer address in previews.
const addressNotSaved = "(from document, not saved)"

// customerExecutor creates a customer through crmstore.CreateCustomerTx.
// designVersion is a test seam; nil reads the tenant from the context.
type customerExecutor struct {
	designVersion func(ctx context.Context) (string, error)
}

func (e customerExecutor) design(ctx context.Context) (string, error) {
	if e.designVersion != nil {
		return e.designVersion(ctx)
	}
	t, err := tenancy.TenantFromContext(ctx)
	if err != nil {
		return "", fmt.Errorf("resolve tenant: %w", err)
	}
	return t.DesignVersion, nil
}

// Resource returns the customer RBAC resource.
func (customerExecutor) Resource() authz.Resource { return authz.ResourceCustomer }

// Validate strictly decodes and checks the payload.
func (customerExecutor) Validate(_ context.Context, payload json.RawMessage) error {
	_, err := parseCustomer(payload)
	return err
}

// Preview renders the human-readable summary.
func (customerExecutor) Preview(_ context.Context, payload json.RawMessage) (proposedaction.Preview, error) {
	p, err := parseCustomer(payload)
	if err != nil {
		return proposedaction.Preview{}, err
	}
	fields := []proposedaction.PreviewField{{Label: "Name", Value: p.Name}, {Label: "Contact email", Value: p.ContactEmail}}
	if p.TaxID != "" {
		fields = append(fields, proposedaction.PreviewField{Label: "Tax ID", Value: p.TaxID})
	}
	if p.Address != "" {
		fields = append(fields, proposedaction.PreviewField{Label: "Address " + addressNotSaved, Value: p.Address})
	}
	return proposedaction.Preview{Title: "Create customer '" + p.Name + "'", Summary: "New customer from an extracted document", Fields: fields}, nil
}

// InScope always allows: a create has no row-level scope.
func (customerExecutor) InScope(context.Context, proposedaction.Querier, authz.Scope, proposedaction.Caller, proposedaction.Kind, json.RawMessage) (bool, error) {
	return inScopeAlways()
}

// Execute creates the customer and its audit row inside the confirm tx.
func (e customerExecutor) Execute(ctx context.Context, tx pgx.Tx, caller proposedaction.Caller, payload json.RawMessage) (proposedaction.ResultRef, error) {
	p, err := parseCustomer(payload)
	if err != nil {
		return proposedaction.ResultRef{}, err
	}
	dv, err := e.design(ctx)
	if err != nil {
		return proposedaction.ResultRef{}, err
	}
	if dv != tenancy.DesignV2 {
		return proposedaction.ResultRef{}, errors.New("customer creation from proposals is unavailable for this workspace design")
	}
	core := map[string]any{coreCustomerName: p.Name, coreCustomerEmail: p.ContactEmail}
	if p.TaxID != "" {
		core[coreCustomerTaxID] = p.TaxID
	}
	rec, err := crmstore.CreateCustomerTx(ctx, tx, crmstore.CreateInput{ActorIdentityID: caller.IdentityID, CoreFields: core})
	if err != nil {
		return proposedaction.ResultRef{}, fmt.Errorf("create proposed customer: %w", err)
	}
	snap := map[string]any{"id": rec.ID, "recordNumber": rec.RecordNumber, "name": p.Name}
	if err := auditCreate(ctx, tx, caller, authz.ResourceCustomer, auditTableCustomer, rec.ID, p.SourceRef, snap); err != nil {
		return proposedaction.ResultRef{}, err
	}
	return proposedaction.ResultRef{Resource: string(authz.ResourceCustomer), RecordID: rec.ID, Label: p.Name}, nil
}
