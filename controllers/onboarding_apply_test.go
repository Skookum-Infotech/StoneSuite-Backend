package controllers

import (
	"reflect"
	"testing"
)

func TestMissingApplyFields(t *testing.T) {
	complete := map[string]any{
		"company_name":             "Acme Stone Co.",
		"country":                  "United States of America",
		"currency":                 "USD",
		"location_name":            "Main Showroom",
		"location_address_line1":   "123 Main St",
		"location_address_city":    "Springfield",
		"location_address_country": "United States of America",
		"location_address_state":   "Illinois",
		"location_address_zip":     "62704",
		"super_admin_email":        "owner@acmestone.example",
	}
	without := func(keys ...string) map[string]any {
		out := make(map[string]any, len(complete))
		for k, v := range complete {
			out[k] = v
		}
		for _, k := range keys {
			delete(out, k)
		}
		return out
	}
	with := func(key string, value any) map[string]any {
		out := without()
		out[key] = value
		return out
	}

	tests := []struct {
		name string
		form map[string]any
		want []string
	}{
		{"complete application", complete, nil},
		{"location state missing", without("location_address_state"), []string{"Location state"}},
		{"whitespace-only value counts as blank", with("location_name", "   "), []string{"Location name"}},
		{"non-string value counts as blank", with("company_name", 42), []string{"Company name"}},
		{
			"several missing are reported in form order",
			without("currency", "location_address_zip", "company_name"),
			[]string{"Company name", "Company currency", "Location zip / postal code"},
		},
		{
			"empty submission lists everything",
			map[string]any{},
			[]string{
				"Company name", "Company country", "Company currency", "Location name",
				"Location address line 1", "Location city", "Location country", "Location state",
				"Location zip / postal code", "Super admin email",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := missingApplyFields(tt.form); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("missingApplyFields() = %v, want %v", got, tt.want)
			}
		})
	}
}
