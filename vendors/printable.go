package vendors

import "stonesuite-backend/docpdf"

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

// ToPrintable maps a vendor to a contact-card PrintableDoc. There is no
// transactional document type for the vendor master itself (no lines, no
// totals) -- this is a one-page contact summary, reusing the shared docpdf
// renderer so "Send to Vendor" goes through the same PDF/email pipeline as
// every other purchase-side module.
func ToPrintable(v Vendor, seller docpdf.Seller) docpdf.PrintableDoc {
	email, _ := ContactEmail(&v)
	phone := ""
	if v.ContactPoint != nil {
		phone = v.ContactPoint.Telephone
	}
	return docpdf.PrintableDoc{
		Seller: seller, Kind: "VENDOR", Number: v.Number, Status: v.Status,
		BillTo: docpdf.Address{
			Name: v.DisplayName, Line1: v.PhysicalAddress, Phone: phone, Email: email,
		},
		Notes: "Vendor Type: " + v.VendorType,
	}
}

// Recipient resolves the default "Send to Vendor" email recipient directly
// from the vendor's own contact fields -- unlike the other four purchase-side
// modules, no secondary lookup is needed since this *is* the vendor record.
func Recipient(v Vendor) (email, name string) {
	return ContactEmail(&v)
}
