package crmstore

import "stonesuite-backend/tenancy"

// For returns the CRM Store implementation for a tenant's design version.
// Unknown/empty versions fall back to DesignV1 (the original design), so
// existing tenants behave exactly as before.
func For(designVersion string) Store {
	switch designVersion {
	case tenancy.DesignV2:
		return &relationalStore{}
	default:
		return &workflowStore{}
	}
}
