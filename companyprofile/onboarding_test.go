package companyprofile

import "testing"

func TestFromOnboardingMetadata(t *testing.T) {
	tests := []struct {
		name         string
		metadataJSON string
		wantProfile  Profile
		wantOK       bool
	}{
		{
			name: "full metadata",
			metadataJSON: `{
				"company_name": "Acme Stone Co.",
				"legal_name": "Acme Stone Company LLC",
				"industry": "Fabrication",
				"country": "United States of America",
				"currency": "USD",
				"billing_address_line1": "123 Main St",
				"billing_address_city": "Springfield",
				"billing_address_state": "IL",
				"billing_address_zip": "62704",
				"shipping_address_line1": "456 Warehouse Ave",
				"super_admin_email": "owner@acmestone.example"
			}`,
			wantProfile: Profile{
				CompanyName: "Acme Stone Co.",
				LegalName:   "Acme Stone Company LLC",
				Industry:    "Fabrication",
				Country:     "United States of America",
				Currency:    "USD",
				BillingAddress: Address{
					Line1: "123 Main St", City: "Springfield", State: "IL", Zip: "62704",
				},
				ShippingAddress: Address{Line1: "456 Warehouse Ave"},
			},
			wantOK: true,
		},
		{
			name:         "company name only",
			metadataJSON: `{"company_name": "Acme Stone Co."}`,
			wantProfile:  Profile{CompanyName: "Acme Stone Co."},
			wantOK:       true,
		},
		{
			name:         "company name trimmed",
			metadataJSON: `{"company_name": "  Acme Stone Co.  "}`,
			wantProfile:  Profile{CompanyName: "Acme Stone Co."},
			wantOK:       true,
		},
		{
			name:         "address field trimmed",
			metadataJSON: `{"company_name": "Acme", "billing_address_city": "  Springfield  "}`,
			wantProfile: Profile{
				CompanyName:    "Acme",
				BillingAddress: Address{City: "Springfield"},
			},
			wantOK: true,
		},
		{
			name:         "location keys are not part of the profile",
			metadataJSON: `{"company_name": "Acme", "location_name": "HQ", "location_address_city": "Springfield"}`,
			wantProfile:  Profile{CompanyName: "Acme"},
			wantOK:       true,
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
			got, ok := FromOnboardingMetadata(tt.metadataJSON)
			if ok != tt.wantOK {
				t.Fatalf("FromOnboardingMetadata(%q) ok = %v, want %v", tt.metadataJSON, ok, tt.wantOK)
			}
			if ok && got != tt.wantProfile {
				t.Errorf("FromOnboardingMetadata(%q) = %+v, want %+v", tt.metadataJSON, got, tt.wantProfile)
			}
		})
	}
}
