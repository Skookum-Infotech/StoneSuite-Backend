package estimate

import "stonesuite-backend/docpdf"

// ToPrintable maps an estimate to the renderer's PrintableDoc. Estimates do
// not show payment status.
func ToPrintable(e Estimate, seller docpdf.Seller) docpdf.PrintableDoc {
	lines := make([]docpdf.PrintLine, 0, len(e.Items))
	for _, it := range e.Items {
		lines = append(lines, docpdf.PrintLine{
			SKU: it.SKU, Name: it.ItemName, Description: it.Description, UnitCode: it.UnitCode,
			Quantity: it.Quantity, UnitPrice: it.UnitPrice, DiscountPercent: it.DiscountPercent,
			TaxPercent: it.TaxPercent, LineTotal: it.LineTotal,
		})
	}
	return docpdf.PrintableDoc{
		Seller: seller, Kind: "ESTIMATE", Number: e.Number, Status: e.Status,
		IssueDate: e.EstimateDate, DueDate: e.ValidUntil,
		BillTo: addrToPrintable(e.Billing), ShipTo: addrToPrintable(e.Shipping),
		Lines:    lines,
		Subtotal: e.Subtotal, DiscountTotal: e.DiscountTotal, TaxTotal: e.TaxTotal,
		ShippingCharge: e.ShippingCharge, Adjustment: e.Adjustment, GrandTotal: e.GrandTotal,
		Terms: e.TermsConditions, Notes: e.Notes, Memo: e.Memo,
	}
}

// Recipient resolves the default document-email recipient.
func Recipient(e Estimate) (email, name string) {
	name = e.Billing.CustomerName
	if name == "" {
		name = e.Customer.Name
	}
	return e.Billing.Email, name
}

func addrToPrintable(a AddressInput) docpdf.Address {
	cityStateZip := a.City
	if a.Zip != "" {
		if cityStateZip != "" {
			cityStateZip += " "
		}
		cityStateZip += a.Zip
	}
	return docpdf.Address{
		Name: a.CustomerName, Attention: a.Attention, Line1: a.AddrLine1, Line2: a.AddrLine2,
		CityStateZip: cityStateZip, Phone: a.Phone, Email: a.Email,
	}
}
