package controllers

import (
	"stonesuite-backend/companyprofile"
	"stonesuite-backend/docpdf"
)

// paymentFromProfile maps the company profile's payment details onto the
// renderer's type; a profile with none stored yields the zero value.
func paymentFromProfile(p *companyprofile.Profile) docpdf.PaymentDetails {
	if p == nil || p.PaymentDetails == nil {
		return docpdf.PaymentDetails{}
	}
	pd := p.PaymentDetails
	return docpdf.PaymentDetails{BankName: pd.BankName, AccountNumber: pd.AccountNumber, RoutingNumber: pd.RoutingNumber}
}

// docKindByProfileKey maps a company-profile document-default key to the
// docpdf document Kind it applies to.
var docKindByProfileKey = map[string]string{
	companyprofile.DocKindEstimate:   "ESTIMATE",
	companyprofile.DocKindQuote:      "QUOTE",
	companyprofile.DocKindSalesOrder: "SALES ORDER",
	companyprofile.DocKindInvoice:    "INVOICE",
}

// defaultsFromProfile maps the profile's per-kind default Terms/Notes onto the
// renderer's Kind-keyed form; a profile with none stored yields nil.
func defaultsFromProfile(p *companyprofile.Profile) map[string]docpdf.Wording {
	if p == nil || len(p.DocumentDefaults) == 0 {
		return nil
	}
	out := make(map[string]docpdf.Wording, len(p.DocumentDefaults))
	for key, w := range p.DocumentDefaults {
		if kind, ok := docKindByProfileKey[key]; ok {
			out[kind] = docpdf.Wording{Terms: w.Terms, Notes: w.Notes}
		}
	}
	return out
}

// profileResponse is the profile as the API returns it: with PaymentDetails
// always present, so a client never has to special-case "none saved yet". The
// caller's profile is not modified.
func profileResponse(p *companyprofile.Profile) *companyprofile.Profile {
	if p.PaymentDetails != nil {
		return p
	}
	out := *p
	out.PaymentDetails = &companyprofile.PaymentDetails{}
	return &out
}
