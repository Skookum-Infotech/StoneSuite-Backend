package companylocation

import "testing"

func TestFromOnboardingMetadata(t *testing.T) {
	tests := []struct {
		name         string
		metadataJSON string
		wantInput    Input
		wantOK       bool
	}{
		{
			name: "full location",
			metadataJSON: `{
				"company_name": "Acme Stone Co.",
				"location_name": "Main Showroom",
				"location_phone": "+1 555 123 4567",
				"location_address_line1": "123 Main St",
				"location_address_line2": "Building B",
				"location_address_suite": "Suite 400",
				"location_address_city": "Springfield",
				"location_address_country": "United States of America",
				"location_address_state": "Illinois",
				"location_address_zip": "62704"
			}`,
			wantInput: Input{
				Name:  "Main Showroom",
				Phone: "+1 555 123 4567",
				Address: Address{
					Line1: "123 Main St", Line2: "Building B", Suite: "Suite 400", City: "Springfield",
					Country: "United States of America", State: "Illinois", Zip: "62704",
				},
			},
			wantOK: true,
		},
		{
			name:         "name only",
			metadataJSON: `{"location_name": "Warehouse"}`,
			wantInput:    Input{Name: "Warehouse"},
			wantOK:       true,
		},
		{
			name:         "values trimmed",
			metadataJSON: `{"location_name": "  Warehouse  ", "location_address_city": "  Springfield  "}`,
			wantInput:    Input{Name: "Warehouse", Address: Address{City: "Springfield"}},
			wantOK:       true,
		},
		{
			name:         "billing address keys are not a location",
			metadataJSON: `{"company_name": "Acme", "billing_address_line1": "123 Main St"}`,
			wantOK:       false,
		},
		{
			name:         "address without a name is not usable",
			metadataJSON: `{"location_address_line1": "123 Main St"}`,
			wantOK:       false,
		},
		{
			name:         "whitespace-only name",
			metadataJSON: `{"location_name": "   "}`,
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
			name:         "name is not a string",
			metadataJSON: `{"location_name": 123}`,
			wantOK:       false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := FromOnboardingMetadata(tt.metadataJSON)
			if ok != tt.wantOK {
				t.Fatalf("FromOnboardingMetadata(%q) ok = %v, want %v", tt.metadataJSON, ok, tt.wantOK)
			}
			if ok && got != tt.wantInput {
				t.Errorf("FromOnboardingMetadata(%q) = %+v, want %+v", tt.metadataJSON, got, tt.wantInput)
			}
		})
	}
}
