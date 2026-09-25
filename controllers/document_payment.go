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
