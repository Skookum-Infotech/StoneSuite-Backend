package invoice

import "stonesuite-backend/docpdf"

// ToPrintable maps an invoice to the renderer's module-agnostic PrintableDoc.
// Invoices show payment status (Amount Paid / Balance Due).
func ToPrintable(inv Invoice, seller docpdf.Seller) docpdf.PrintableDoc {
	lines := make([]docpdf.PrintLine, 0, len(inv.Items))
	for _, it := range inv.Items {
		lines = append(lines, docpdf.PrintLine{
			SKU: it.SKU, Name: it.ItemName, Description: it.Description, UnitCode: it.UnitCode,
			Quantity: it.Quantity, UnitPrice: it.UnitPrice, DiscountPercent: it.DiscountPercent,
			TaxPercent: it.TaxPercent, LineTotal: it.LineTotal,
		})
	}
	return docpdf.PrintableDoc{
		Seller: seller, Kind: "INVOICE", Number: inv.Number, Status: inv.StatusName,
		IssueDate: inv.InvoiceDate, DueDate: inv.DueDate,
		BillTo: addrToPrintable(inv.Billing), ShipTo: addrToPrintable(inv.Shipping),
		Lines:    lines,
		Subtotal: inv.Subtotal, DiscountTotal: inv.DiscountTotal, TaxTotal: inv.TaxTotal,
		ShippingCharge: inv.ShippingCharge, Adjustment: inv.Adjustment, GrandTotal: inv.GrandTotal,
		AmountPaid: inv.AmountPaid, BalanceDue: inv.BalanceDue, ShowBalance: true,
		Terms: inv.TermsConditions, Notes: inv.Notes, Memo: inv.Memo,
	}
}

// Recipient resolves the default document-email recipient.
func Recipient(inv Invoice) (email, name string) {
	name = inv.Billing.CustomerName
	if name == "" {
		name = inv.Customer.Name
	}
	return inv.Billing.Email, name
}

func addrToPrintable(a Address) docpdf.Address {
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
