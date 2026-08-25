package salesorder

import "stonesuite-backend/docpdf"

// ToPrintable maps a sales order to the renderer's PrintableDoc. Sales orders
// do not show payment status; DueDate carries the order's PaymentDueDate
// (there is no expiry-date concept for a sales order).
func ToPrintable(o Order, seller docpdf.Seller) docpdf.PrintableDoc {
	lines := make([]docpdf.PrintLine, 0, len(o.Items))
	for _, it := range o.Items {
		lines = append(lines, docpdf.PrintLine{
			SKU: it.SKU, Name: it.ItemName, Description: it.Description, UnitCode: it.UnitCode,
			Quantity: it.Quantity, UnitPrice: it.UnitPrice, DiscountPercent: it.DiscountPercent,
			TaxPercent: it.TaxPercent, LineTotal: it.LineTotal,
		})
	}
	return docpdf.PrintableDoc{
		Seller: seller, Kind: "SALES ORDER", Number: o.Number, Status: o.Status,
		IssueDate: o.OrderDate, DueDate: o.PaymentDueDate,
		BillTo: addrToPrintable(o.Billing), ShipTo: addrToPrintable(o.Shipping),
		Lines:    lines,
		Subtotal: o.Subtotal, DiscountTotal: o.DiscountTotal, TaxTotal: o.TaxTotal,
		ShippingCharge: o.ShippingCharge, Adjustment: o.Adjustment, GrandTotal: o.GrandTotal,
		Terms: o.TermsConditions, Notes: o.Notes, Memo: o.Memo,
	}
}

// Recipient resolves the default document-email recipient.
func Recipient(o Order) (email, name string) {
	name = o.Billing.CustomerName
	if name == "" {
		name = o.Customer.Name
	}
	return o.Billing.Email, name
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
