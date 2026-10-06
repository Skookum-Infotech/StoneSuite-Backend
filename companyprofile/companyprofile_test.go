package companyprofile

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

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

		{"valid payment details", func(p *Profile) {
			p.PaymentDetails = &PaymentDetails{BankName: "Chase Bank", AccountNumber: "000123456789", RoutingNumber: "021000021"}
		}, false},
		{"payment details omitted", func(p *Profile) { p.PaymentDetails = nil }, false},
		{"payment details at max length", func(p *Profile) {
			p.PaymentDetails = &PaymentDetails{BankName: atMax, AccountNumber: atMax, RoutingNumber: atMax}
		}, false},
		{"bank name too long", func(p *Profile) { p.PaymentDetails = &PaymentDetails{BankName: tooLong} }, true},
		{"account number too long", func(p *Profile) { p.PaymentDetails = &PaymentDetails{AccountNumber: tooLong} }, true},
		{"routing number too long", func(p *Profile) { p.PaymentDetails = &PaymentDetails{RoutingNumber: tooLong} }, true},
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

func TestPaymentArgs(t *testing.T) {
	str := func(s string) *string { return &s }
	tests := []struct {
		name                            string
		in                              *PaymentDetails
		wantBank, wantAcct, wantRouting *string
	}{
		{"omitted means keep what is stored (NULL)", nil, nil, nil, nil},
		{"values are passed through", &PaymentDetails{BankName: "Chase", AccountNumber: "123", RoutingNumber: "021"}, str("Chase"), str("123"), str("021")},
		{"surrounding whitespace is trimmed", &PaymentDetails{BankName: "  Chase ", AccountNumber: "\t123\n", RoutingNumber: " 021"}, str("Chase"), str("123"), str("021")},
		{"an empty object clears the stored values", &PaymentDetails{}, str(""), str(""), str("")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bank, acct, routing := paymentArgs(tt.in)
			for _, c := range []struct {
				name      string
				got, want *string
			}{{"bank", bank, tt.wantBank}, {"account", acct, tt.wantAcct}, {"routing", routing, tt.wantRouting}} {
				switch {
				case c.want == nil && c.got != nil:
					t.Errorf("%s = %q, want nil", c.name, *c.got)
				case c.want != nil && c.got == nil:
					t.Errorf("%s = nil, want %q", c.name, *c.want)
				case c.want != nil && *c.got != *c.want:
					t.Errorf("%s = %q, want %q", c.name, *c.got, *c.want)
				}
			}
		})
	}
}

func TestValidate_DocumentDefaults(t *testing.T) {
	long := strings.Repeat("x", MaxWordingLength+1)
	tests := []struct {
		name    string
		in      map[string]DocumentWording
		wantErr bool
	}{
		{"nil is fine", nil, false},
		{"known kind", map[string]DocumentWording{DocKindInvoice: {Terms: "Net 30"}}, false},
		{"unknown kind", map[string]DocumentWording{"purchase_order": {Terms: "x"}}, true},
		{"terms too long", map[string]DocumentWording{DocKindQuote: {Terms: long}}, true},
		{"notes too long", map[string]DocumentWording{DocKindQuote: {Notes: long}}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := Validate(Profile{CompanyName: "Acme", DocumentDefaults: tc.in})
			if tc.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestDefaultsArg(t *testing.T) {
	got, err := defaultsArg(nil)
	assert.NoError(t, err)
	assert.Nil(t, got, "nil keeps stored defaults")

	got, err = defaultsArg(map[string]DocumentWording{
		DocKindInvoice: {Terms: "  Net 30 ", Notes: " "},
		DocKindQuote:   {},
	})
	assert.NoError(t, err)
	assert.JSONEq(t, `{"invoice":{"terms":"Net 30","notes":""}}`, *got)
}
