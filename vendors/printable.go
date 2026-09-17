package vendors

// ContactEmail resolves the best available email/name pair for a vendor,
// preferring the top-level Email and falling back to ContactPoint.Email --
// used by the purchase-side document modules (purchaseorder, vendorbill,
// vendorcredit, vendorpayment) to resolve a "Send to Vendor" recipient, since
// none of them snapshot a vendor email on their own record.
func ContactEmail(v *Vendor) (email, name string) {
	email = v.Email
	if email == "" && v.ContactPoint != nil {
		email = v.ContactPoint.Email
	}
	return email, v.DisplayName
}
