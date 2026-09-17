package vendors

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestContactEmail(t *testing.T) {
	cases := []struct {
		name      string
		vendor    Vendor
		wantEmail string
	}{
		{
			name:      "top-level email wins",
			vendor:    Vendor{DisplayName: "Acme Supply", vendorFields: vendorFields{Email: "sales@acme.example", ContactPoint: &ContactPoint{Email: "other@acme.example"}}},
			wantEmail: "sales@acme.example",
		},
		{
			name:      "falls back to contact point",
			vendor:    Vendor{DisplayName: "Acme Supply", vendorFields: vendorFields{ContactPoint: &ContactPoint{Email: "contact@acme.example"}}},
			wantEmail: "contact@acme.example",
		},
		{
			name:      "no email anywhere",
			vendor:    Vendor{DisplayName: "Acme Supply"},
			wantEmail: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			email, name := ContactEmail(&tc.vendor)
			assert.Equal(t, tc.wantEmail, email)
			assert.Equal(t, "Acme Supply", name)
		})
	}
}
