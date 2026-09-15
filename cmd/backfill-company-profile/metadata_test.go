package main

import (
	"testing"

	"stonesuite-backend/companyprofile"
)

func TestParseMetadataProfile(t *testing.T) {
	tests := []struct {
		name         string
		metadataJSON string
		wantProfile  companyprofile.Profile
		wantOK       bool
	}{
		{
			name: "full metadata",
			metadataJSON: `{
				"company_name": "Acme Stone Co.",
				"legal_name": "Acme Stone Company LLC",
				"industry": "Fabrication",
				"billing_address_line1": "123 Main St",
				"billing_address_city": "Springfield",
				"billing_address_state": "IL",
				"billing_address_zip": "62704",
				"shipping_address_line1": "456 Warehouse Ave",
				"super_admin_email": "owner@acmestone.example"
			}`,
			wantProfile: companyprofile.Profile{
				CompanyName: "Acme Stone Co.",
				LegalName:   "Acme Stone Company LLC",
				Industry:    "Fabrication",
				BillingAddress: companyprofile.Address{
					Line1: "123 Main St", City: "Springfield", State: "IL", Zip: "62704",
				},
				ShippingAddress: companyprofile.Address{Line1: "456 Warehouse Ave"},
			},
			wantOK: true,
		},
		{
			name:         "company name only",
			metadataJSON: `{"company_name": "Acme Stone Co."}`,
			wantProfile:  companyprofile.Profile{CompanyName: "Acme Stone Co."},
			wantOK:       true,
		},
		{
			name:         "company name trimmed",
			metadataJSON: `{"company_name": "  Acme Stone Co.  "}`,
			wantProfile:  companyprofile.Profile{CompanyName: "Acme Stone Co."},
			wantOK:       true,
		},
		{
			name:         "address field trimmed",
			metadataJSON: `{"company_name": "Acme", "billing_address_city": "  Springfield  "}`,
			wantProfile: companyprofile.Profile{
				CompanyName:    "Acme",
				BillingAddress: companyprofile.Address{City: "Springfield"},
			},
			wantOK: true,
		},
		{
			name:         "missing company name",
			metadataJSON: `{"industry": "Fabrication"}`,
			wantOK:       false,
		},
		{
			name:         "empty company name",
			metadataJSON: `{"company_name": ""}`,
			wantOK:       false,
		},
		{
			name:         "whitespace-only company name",
			metadataJSON: `{"company_name": "   "}`,
			wantOK:       false,
		},
		{
			name:         "empty metadata string",
			metadataJSON: "",
			wantOK:       false,
		},
		{
			name:         "malformed JSON",
			metadataJSON: `not json`,
			wantOK:       false,
		},
		{
			name:         "company name is not a string",
			metadataJSON: `{"company_name": 123}`,
			wantOK:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseMetadataProfile(tt.metadataJSON)
			if ok != tt.wantOK {
				t.Fatalf("parseMetadataProfile(%q) ok = %v, want %v", tt.metadataJSON, ok, tt.wantOK)
			}
			if ok && got != tt.wantProfile {
				t.Errorf("parseMetadataProfile(%q) = %+v, want %+v", tt.metadataJSON, got, tt.wantProfile)
			}
		})
	}
}
