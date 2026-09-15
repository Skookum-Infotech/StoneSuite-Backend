package companyprofile

import "testing"

func TestValidate(t *testing.T) {
	base := func() Profile {
		return Profile{CompanyName: "Acme Stone Co."}
	}
	tooLong := func() string {
		b := make([]byte, MaxFieldLength+1)
		for i := range b {
			b[i] = 'a'
		}
		return string(b)
	}()
	atMax := tooLong[:MaxFieldLength]

	tests := []struct {
		name    string
		mutate  func(p *Profile)
		wantErr bool
	}{
		{"valid minimal", func(p *Profile) {}, false},
		{"company name empty", func(p *Profile) { p.CompanyName = "" }, true},
		{"company name whitespace only", func(p *Profile) { p.CompanyName = "   " }, true},
		{"company name at max length", func(p *Profile) { p.CompanyName = atMax }, false},
		{"company name too long", func(p *Profile) { p.CompanyName = tooLong }, true},
		{"legal name too long", func(p *Profile) { p.LegalName = tooLong }, true},
		{"industry too long", func(p *Profile) { p.Industry = tooLong }, true},
		{"website too long", func(p *Profile) { p.Website = tooLong }, true},
		{"country too long", func(p *Profile) { p.Country = tooLong }, true},
		{"currency too long", func(p *Profile) { p.Currency = tooLong }, true},
		{"timezone too long", func(p *Profile) { p.Timezone = tooLong }, true},
		{"tax id too long", func(p *Profile) { p.TaxID = tooLong }, true},

		{"valid full address", func(p *Profile) {
			p.BillingAddress = Address{
				Line1: "123 Main St", Line2: "Suite 400", Suite: "400",
				City: "Springfield", Country: "United States", State: "IL", Zip: "62704",
			}
		}, false},
		{"billing address line1 too long", func(p *Profile) { p.BillingAddress.Line1 = tooLong }, true},
		{"billing address line2 too long", func(p *Profile) { p.BillingAddress.Line2 = tooLong }, true},
		{"billing address suite too long", func(p *Profile) { p.BillingAddress.Suite = tooLong }, true},
		{"billing address city too long", func(p *Profile) { p.BillingAddress.City = tooLong }, true},
		{"billing address country too long", func(p *Profile) { p.BillingAddress.Country = tooLong }, true},
		{"billing address state too long", func(p *Profile) { p.BillingAddress.State = tooLong }, true},
		{"billing address zip too long", func(p *Profile) { p.BillingAddress.Zip = tooLong }, true},
		{"shipping address line1 too long", func(p *Profile) { p.ShippingAddress.Line1 = tooLong }, true},
		{"return address line1 too long", func(p *Profile) { p.ReturnAddress.Line1 = tooLong }, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := base()
			tt.mutate(&p)
			err := Validate(p)
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate(%+v) error = %v, wantErr %v", p, err, tt.wantErr)
			}
		})
	}
}
