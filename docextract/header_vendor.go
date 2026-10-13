package docextract

import (
	"sort"
	"strings"
)

// blankHeader is a header with every field explicitly not found.
func blankHeader() Header {
	nf := notFound()
	return Header{
		PONumber: nf, OrderDate: nf, DeliveryDate: nf, CustomerName: nf, BillTo: nf, ShipTo: nf,
		PaymentTerms: nf, Subtotal: nf, Tax: nf, Shipping: nf, Discount: nf, Total: nf, Currency: nf, Notes: nf,
		VendorName: nf, InvoiceNumber: nf, InvoiceDate: nf, DueDate: nf,
	}
}

// parseVendorHeader extracts the header of a purchase order or vendor bill
// from rows outside the line table. It never sets CustomerName: the party of
// interest here is the vendor, the company that is not us.
func parseVendorHeader(pages []PageRows, spans map[int][2]int, opts HeaderOptions) headerOut {
	rows, text := headerRows(pages, spans)
	specs := vendorSpecs(opts.DocType, opts.Class == ClassQuote)
	hits := findHitsWith(rows, specs)
	out := headerOut{Header: blankHeader()}
	h := &out.Header
	seen := map[string]bool{}
	for _, hit := range hits {
		seen[hit.spec.key] = true
	}
	for k := range seen {
		out.Keys = append(out.Keys, k)
	}
	sort.Strings(out.Keys)

	var warn []string
	pos, multi := extractPO(rows, hits)
	h.PONumber = pos
	if multi {
		out.Warnings = append(out.Warnings, WarnMultipleDocuments)
	}
	if opts.DocType == DocTypeVendorBill {
		h.InvoiceNumber = extractInvoiceNumber(rows, hits)
		h.InvoiceDate, warn = extractDate(rows, hits, keyInvDate)
		out.Warnings = append(out.Warnings, warn...)
		h.DueDate, warn = extractDate(rows, hits, keyDue)
		out.Warnings = append(out.Warnings, warn...)
	} else {
		h.OrderDate, warn = extractDate(rows, hits, keyOrderDate)
		out.Warnings = append(out.Warnings, warn...)
		h.DeliveryDate, warn = extractDate(rows, hits, keyDelivery)
		out.Warnings = append(out.Warnings, warn...)
	}
	_, h.ShipTo = extractParty(rows, hits, keyShipTo, opts.OwnCompanyNames, false)
	h.PaymentTerms = extractTerms(rows, hits)
	h.Subtotal = extractMoneyWith(rows, hits, keySubtotal, true)
	h.Tax = extractMoney(rows, hits, keyTax)
	h.Shipping = extractMoney(rows, hits, keyShipping)
	h.Discount = extractMoney(rows, hits, keyDiscount)
	h.Total = extractMoneyWith(rows, hits, keyTotal, true)
	h.Currency, warn = detectCurrency(text)
	out.Warnings = append(out.Warnings, warn...)
	if taxInclRe.MatchString(text) {
		out.Warnings = append(out.Warnings, WarnTaxInclusive)
	}

	numbered := h.PONumber.Found() || h.InvoiceNumber.Found() || len(spans) > 0
	vp := resolveVendor(rows, hits, opts, numbered)
	if vp.customerDoc {
		out.Warnings = append(out.Warnings, WarnCustomerPO)
	} else {
		h.VendorName = vp.name
	}
	if vp.salesInvoice {
		out.Warnings = append(out.Warnings, WarnDocLooksLike+classSalesInvoice)
	}
	return out
}

// classSalesInvoice names an invoice we issued, uploaded as a vendor bill.
const classSalesInvoice = "sales_invoice"

// extractInvoiceNumber returns the vendor's invoice number: a labelled value
// ("Invoice # INV-1042", a label strip), or the single token sitting under a
// boxed or letter-spaced "I N V O I C E" title. A title never lends a
// multi-word value (a street address under it), and a date is never a number.
func extractInvoiceNumber(rows []hrow, hits []labelHit) Field {
	for _, h := range hitsFor(hits, keyInvNo, false) {
		for _, seg := range segments(rows, h) {
			tok := strings.TrimRight(poTokenRe.FindString(seg), ".-/_")
			if tok == "" || !hasDigit(tok) || isDateToken(tok) {
				continue
			}
			if h.spec.nearBelow && tok != strings.TrimSpace(seg) {
				continue
			}
			return hitField(rows, h, tok, ConfHigh)
		}
	}
	return notFound()
}
