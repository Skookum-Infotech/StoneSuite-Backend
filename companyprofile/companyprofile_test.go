package companyprofile

import "testing"

func TestValidate(t *testing.T) {
	base := func() Profile {
		return Profile{CompanyName: "Acme Stone Co."}
	}
	longShort := make([]byte, MaxShortFieldLength+1)
	for i := range longShort {
		longShort[i] = 'a'
	}
	longAddress := make([]byte, MaxAddressLength+1)
	for i := range longAddress {
		longAddress[i] = 'a'
	}

	tests := []struct {
		name    string
		mutate  func(p *Profile)
		wantErr bool
	}{
		{"valid minimal", func(p *Profile) {}, false},
		{"company name empty", func(p *Profile) { p.CompanyName = "" }, true},
		{"company name whitespace only", func(p *Profile) { p.CompanyName = "   " }, true},
		{"company name at max length", func(p *Profile) { p.CompanyName = string(longShort[:MaxShortFieldLength]) }, false},
		{"company name too long", func(p *Profile) { p.CompanyName = string(longShort) }, true},
		{"legal name too long", func(p *Profile) { p.LegalName = string(longShort) }, true},
		{"industry too long", func(p *Profile) { p.Industry = string(longShort) }, true},
		{"website too long", func(p *Profile) { p.Website = string(longShort) }, true},
		{"country too long", func(p *Profile) { p.Country = string(longShort) }, true},
		{"currency too long", func(p *Profile) { p.Currency = string(longShort) }, true},
		{"timezone too long", func(p *Profile) { p.Timezone = string(longShort) }, true},
		{"tax id too long", func(p *Profile) { p.TaxID = string(longShort) }, true},
		{"billing address at max length", func(p *Profile) { p.BillingAddress = string(longAddress[:MaxAddressLength]) }, false},
		{"billing address too long", func(p *Profile) { p.BillingAddress = string(longAddress) }, true},
		{"shipping address too long", func(p *Profile) { p.ShippingAddress = string(longAddress) }, true},
		{"return address too long", func(p *Profile) { p.ReturnAddress = string(longAddress) }, true},
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
