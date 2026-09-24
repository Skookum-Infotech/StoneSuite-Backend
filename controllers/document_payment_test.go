package controllers

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"stonesuite-backend/companyprofile"
	"stonesuite-backend/docpdf"
)

func TestPaymentFromProfile(t *testing.T) {
	tests := []struct {
		name string
		in   *companyprofile.Profile
		want docpdf.PaymentDetails
	}{
		{"no profile", nil, docpdf.PaymentDetails{}},
		{"profile without payment details", &companyprofile.Profile{CompanyName: "Acme"}, docpdf.PaymentDetails{}},
		{
			"payment details are carried over",
			&companyprofile.Profile{PaymentDetails: &companyprofile.PaymentDetails{BankName: "Chase Bank", AccountNumber: "000123456789", RoutingNumber: "021000021"}},
			docpdf.PaymentDetails{BankName: "Chase Bank", AccountNumber: "000123456789", RoutingNumber: "021000021"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, paymentFromProfile(tc.in))
		})
	}
}

func TestProfileResponse(t *testing.T) {
	stored := &companyprofile.PaymentDetails{BankName: "Chase Bank"}

	got := profileResponse(&companyprofile.Profile{CompanyName: "Acme", PaymentDetails: stored})
	assert.Same(t, stored, got.PaymentDetails, "stored details are returned as they are")

	in := &companyprofile.Profile{CompanyName: "Acme"}
	got = profileResponse(in)
	if assert.NotNil(t, got.PaymentDetails, "paymentDetails is always present in a response") {
		assert.Equal(t, companyprofile.PaymentDetails{}, *got.PaymentDetails)
	}
	assert.Equal(t, "Acme", got.CompanyName)
	assert.Nil(t, in.PaymentDetails, "the caller's profile is not modified")
}
