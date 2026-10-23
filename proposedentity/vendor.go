package proposedentity

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"stonesuite-backend/authz"
	"stonesuite-backend/proposedaction"
	"stonesuite-backend/vendors"
)

// vendorTypeOrganization is the vendor type proposals create.
const vendorTypeOrganization = "Organization"

// vendorCreateBody mirrors the vendors create JSON (vendorFields is unexported).
type vendorCreateBody struct {
	VendorType      string `json:"vendorType"`
	LegalName       string `json:"legalName"`
	Email           string `json:"email,omitempty"`
	PhysicalAddress string `json:"physicalAddress,omitempty"`
}

// vendorExecutor creates a vendor through vendors.CreateTx.
type vendorExecutor struct{}

// Resource returns the vendor RBAC resource.
func (vendorExecutor) Resource() authz.Resource { return authz.ResourceVendor }

// Validate strictly decodes and checks the payload.
func (vendorExecutor) Validate(_ context.Context, payload json.RawMessage) error {
	_, err := parseVendor(payload)
	return err
}

// Preview renders the human-readable summary.
func (vendorExecutor) Preview(_ context.Context, payload json.RawMessage) (proposedaction.Preview, error) {
	p, err := parseVendor(payload)
	if err != nil {
		return proposedaction.Preview{}, err
	}
	fields := []proposedaction.PreviewField{{Label: "Name", Value: p.Name}}
	if p.Email != "" {
		fields = append(fields, proposedaction.PreviewField{Label: "Email", Value: p.Email})
	}
	if p.Address != "" {
		fields = append(fields, proposedaction.PreviewField{Label: "Address", Value: p.Address})
	}
	if p.TaxID != "" {
		fields = append(fields, proposedaction.PreviewField{Label: "Tax ID", Value: p.TaxID})
	}
	return proposedaction.Preview{Title: "Create vendor '" + p.Name + "'", Summary: "New vendor from an extracted document", Fields: fields}, nil
}

// InScope always allows: a create has no row-level scope.
func (vendorExecutor) InScope(context.Context, proposedaction.Querier, authz.Scope, proposedaction.Caller, proposedaction.Kind, json.RawMessage) (bool, error) {
	return inScopeAlways()
}

// Execute creates the vendor and its audit row inside the confirm tx.
func (vendorExecutor) Execute(ctx context.Context, tx pgx.Tx, caller proposedaction.Caller, payload json.RawMessage) (proposedaction.ResultRef, error) {
	p, err := parseVendor(payload)
	if err != nil {
		return proposedaction.ResultRef{}, err
	}
	body, err := json.Marshal(vendorCreateBody{VendorType: vendorTypeOrganization, LegalName: p.Name, Email: p.Email, PhysicalAddress: p.Address})
	if err != nil {
		return proposedaction.ResultRef{}, fmt.Errorf("marshal vendor input: %w", err)
	}
	var in vendors.CreateVendorInput
	if err := json.Unmarshal(body, &in); err != nil {
		return proposedaction.ResultRef{}, fmt.Errorf("build vendor input: %w", err)
	}
	emp, err := actorEmployeeID(ctx, tx, caller.UserID)
	if err != nil {
		return proposedaction.ResultRef{}, err
	}
	id, err := vendors.CreateTx(ctx, tx, in, emp)
	if err != nil {
		return proposedaction.ResultRef{}, fmt.Errorf("create proposed vendor: %w", err)
	}
	snap := map[string]any{"id": id, "vendorType": vendorTypeOrganization, "displayName": p.Name}
	if err := auditCreate(ctx, tx, caller, authz.ResourceVendor, auditTableVendor, id, p.SourceRef, snap); err != nil {
		return proposedaction.ResultRef{}, err
	}
	return proposedaction.ResultRef{Resource: string(authz.ResourceVendor), RecordID: id, Label: p.Name}, nil
}
