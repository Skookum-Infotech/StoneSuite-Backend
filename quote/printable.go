package quote

import "stonesuite-backend/docpdf"

// ToPrintable maps a quote to the renderer's PrintableDoc. Quotes do not show
// payment status.
func ToPrintable(q Quote, seller docpdf.Seller) docpdf.PrintableDoc {
	lines := make([]docpdf.PrintLine, 0, len(q.Items))
	for _, it := range q.Items {
		lines = append(lines, docpdf.PrintLine{
			SKU: it.SKU, Name: it.ItemName, Description: it.Description, UnitCode: it.UnitCode,
			Quantity: it.Quantity, UnitPrice: it.UnitPrice, DiscountPercent: it.DiscountPercent,
			TaxPercent: it.TaxPercent, LineTotal: it.LineTotal,
		})
	}
	return docpdf.PrintableDoc{
		Seller: seller, Kind: "QUOTE", Number: q.Number, Status: q.Status,
		IssueDate: q.QuoteDate, DueDate: q.ValidUntil,
		BillTo: addrToPrintable(q.Billing), ShipTo: addrToPrintable(q.Shipping),
		Lines:    lines,
		Subtotal: q.Subtotal, DiscountTotal: q.DiscountTotal, TaxTotal: q.TaxTotal,
		ShippingCharge: q.ShippingCharge, Adjustment: q.Adjustment, GrandTotal: q.GrandTotal,
		Terms: q.TermsConditions, Notes: q.Notes, Memo: q.Memo,
	}
}

// Recipient resolves the default document-email recipient.
func Recipient(q Quote) (email, name string) {
	name = q.Billing.CustomerName
	if name == "" {
		name = q.Customer.Name
	}
	return q.Billing.Email, name
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
