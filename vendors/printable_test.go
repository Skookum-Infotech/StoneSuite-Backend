package vendors

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"stonesuite-backend/docpdf"
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

func TestToPrintable_Vendor(t *testing.T) {
	v := Vendor{
		Number: "VNDR-1001", Status: "Active", DisplayName: "Acme Supply Co", VendorType: "Organization",
		vendorFields: vendorFields{
			Email: "sales@acme.example", PhysicalAddress: "1 Dock Rd, Dallas, TX 75201",
			ContactPoint: &ContactPoint{Telephone: "555-0100"},
		},
	}
	d := ToPrintable(v, docpdf.Seller{Name: "Acme Stone Co"})
	assert.Equal(t, "VENDOR", d.Kind)
	assert.Equal(t, "VNDR-1001", d.Number)
	assert.Equal(t, "Acme Supply Co", d.BillTo.Name)
	assert.Equal(t, "sales@acme.example", d.BillTo.Email)
	assert.Equal(t, "555-0100", d.BillTo.Phone)
	assert.Empty(t, d.Lines, "vendor is a contact card, not a transactional document")

	email, name := Recipient(v)
	assert.Equal(t, "sales@acme.example", email)
	assert.Equal(t, "Acme Supply Co", name)
}
