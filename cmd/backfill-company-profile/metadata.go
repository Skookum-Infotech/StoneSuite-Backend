package main

import (
	"encoding/json"
	"strings"

	"stonesuite-backend/companyprofile"
)

// parseMetadataProfile extracts a companyprofile.Profile from a tenant's
// onboarding metadata JSON (tenancy.Tenant.Metadata) using the same
// snake_case keys OnboardingForm.tsx submits -- flat top-level fields for
// company_name/legal_name/etc., and flat-prefixed keys for each address
// sub-field (e.g. billing_address_line1, billing_address_city). ok is false
// when the metadata is empty, malformed, or has no usable company_name --
// the caller then leaves that tenant alone rather than writing a
// company_profile row that would fail companyprofile.Validate.
func parseMetadataProfile(metadataJSON string) (profile companyprofile.Profile, ok bool) {
	if metadataJSON == "" {
		return companyprofile.Profile{}, false
	}
	var raw map[string]any
	if err := json.Unmarshal([]byte(metadataJSON), &raw); err != nil {
		return companyprofile.Profile{}, false
	}

	str := func(key string) string {
		v, _ := raw[key].(string)
		return strings.TrimSpace(v)
	}
	address := func(prefix string) companyprofile.Address {
		return companyprofile.Address{
			Line1:   str(prefix + "_line1"),
			Line2:   str(prefix + "_line2"),
			Suite:   str(prefix + "_suite"),
			City:    str(prefix + "_city"),
			Country: str(prefix + "_country"),
			State:   str(prefix + "_state"),
			Zip:     str(prefix + "_zip"),
		}
	}

	profile = companyprofile.Profile{
		CompanyName:     str("company_name"),
		LegalName:       str("legal_name"),
		Industry:        str("industry"),
		Website:         str("website"),
		Country:         str("country"),
		Currency:        str("currency"),
		Timezone:        str("timezone"),
		TaxID:           str("tax_id"),
		BillingAddress:  address("billing_address"),
		ShippingAddress: address("shipping_address"),
		ReturnAddress:   address("return_address"),
	}
	return profile, profile.CompanyName != ""
}
