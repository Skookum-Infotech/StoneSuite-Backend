// Package vendors implements the relational Vendor module: a supplier/
// contractor directory modeled on schema.org Person ∩ Organization — a
// sibling of the CRM `customer` table and the `salesorder` package, not the
// generic v1 JSONB workflow engine. (The pre-existing `workflows` row keyed
// 'vendor' — schema.sql migration 010 — is an unrelated legacy JSONB
// placeholder; see salesorder.Create's identical note about the "sales_order"
// workflow key collision. It is intentionally left untouched.)
//
// The package is named "vendors" (plural), not "vendor", because a top-level
// directory literally named `vendor` has special meaning to the Go toolchain
// (dependency vendoring).
package vendors

import "time"

// ContactPoint is schema.org/ContactPoint (subset): a named point of contact.
type ContactPoint struct {
	ContactType string `json:"contactType"`
	Telephone   string `json:"telephone"`
	Email       string `json:"email"`
}

// CompliancePolicies are schema.org/Organization policy links (all URL range
// on schema.org): ethicsPolicy, diversityPolicy, correctionsPolicy,
// actionableFeedbackPolicy.
type CompliancePolicies struct {
	EthicsPolicyURL             string `json:"ethicsPolicyUrl"`
	DiversityPolicyURL          string `json:"diversityPolicyUrl"`
	CorrectionsPolicyURL        string `json:"correctionsPolicyUrl"`
	ActionableFeedbackPolicyURL string `json:"actionableFeedbackPolicyUrl"`
}

// vendorFields is the payload shared by create and update — everything
// except VendorType, which is fixed at creation and never changes (like
// Sales Order's customer).
type vendorFields struct {
	// Shared (schema.org properties common to Person and Organization)
	Email                string        `json:"email"`
	PhysicalAddress      string        `json:"physicalAddress"`
	FaxNumber            string        `json:"faxNumber"`
	GlobalLocationNumber string        `json:"globalLocationNumber"`
	ISICV4Code           string        `json:"isicV4Code"`
	AssociatedBrands     []string      `json:"associatedBrands"`
	AwardsWon            string        `json:"awardsWon"`
	ContactPoint         *ContactPoint `json:"contactPoint"`
	Funder               string        `json:"funder"`
	HasOfferCatalogURL   string        `json:"hasOfferCatalogUrl"`
	PointOfSaleLocations string        `json:"pointOfSaleLocations"`

	// schema.org/Person — authoritative when VendorType == "Person"
	HonorificPrefix      string `json:"honorificPrefix"`
	GivenName            string `json:"givenName"`
	AdditionalName       string `json:"additionalName"`
	FamilyName           string `json:"familyName"`
	HonorificSuffix      string `json:"honorificSuffix"`
	JobTitle             string `json:"jobTitle"`
	Gender               string `json:"gender"`
	NationalityCountryID *int   `json:"nationalityCountryId"`
	Height               string `json:"height"`
	NetWorth             string `json:"netWorth"`

	// schema.org/Organization — authoritative when VendorType == "Organization"
	LegalName              string               `json:"legalName"`
	RegistrationInfo       string               `json:"registrationInfo"`
	DUNSNumber             string               `json:"dunsNumber"`
	FoundingDate           string               `json:"foundingDate"` // "yyyy-mm-dd"
	FoundingLocation       string               `json:"foundingLocation"`
	DissolutionDate        string               `json:"dissolutionDate"` // "yyyy-mm-dd"
	Department             string               `json:"department"`
	AcceptedPaymentMethods []string             `json:"acceptedPaymentMethods"`
	CompliancePolicies     *CompliancePolicies  `json:"compliancePolicies"`
}

// CreateVendorInput is the create-request payload.
type CreateVendorInput struct {
	VendorType string `json:"vendorType"` // "Person" | "Organization"
	vendorFields
}

// UpdateVendorInput mirrors CreateVendorInput. VendorType is accepted but
// ignored by Update — a vendor's type is fixed after creation, like a sales
// order's customer.
type UpdateVendorInput struct {
	VendorType string `json:"vendorType"`
	vendorFields
}

// Vendor is the full API response for a vendor record.
type Vendor struct {
	ID              string `json:"id"`
	Number          string `json:"vendorNumber"`
	Status          string `json:"status"`     // human label, e.g. "Active"
	StatusCode      string `json:"statusCode"` // lkp_record_status code, e.g. "ACT_"
	VendorType      string `json:"vendorType"`
	DisplayName     string `json:"displayName"`
	OwnerUserID     string `json:"-"`
	OwnerEmployeeID *int   `json:"ownerEmployeeId"`

	vendorFields

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Page is one page of a keyset-paginated vendor search.
type Page struct {
	Records    []Vendor
	NextCursor string
	HasMore    bool
}

// displayName derives the human display name from type-specific fields —
// never stored, so it cannot drift from the authoritative name/legal-name
// columns.
func displayName(vendorType, honorificPrefix, givenName, familyName, legalName string) string {
	if vendorType != "Person" {
		return legalName
	}
	name := givenName
	if familyName != "" {
		if name != "" {
			name += " "
		}
		name += familyName
	}
	if honorificPrefix != "" && name != "" {
		name = honorificPrefix + " " + name
	}
	return name
}
