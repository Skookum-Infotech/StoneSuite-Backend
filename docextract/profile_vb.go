package docextract

import (
	"regexp"
	"strconv"
	"time"
)

const (
	isoDateLayout = "2006-01-02"
	maxTermsDays  = 365
)

var termsDaysRe = regexp.MustCompile(`(?i)\bnet\s*(\d{1,3})\b|\b(\d{1,3})\s*days?\b`)

// vendorBillProfile is the profile for a vendor bill created from an upload.
func vendorBillProfile() docProfile {
	return docProfile{
		build: buildVendorBill, unresolved: vendorBillUnresolved, recommended: vendorBillRecommendedWarnings,
		llmKeys: vendorBillLLMKeys, notRecognized: vendorBillNotRecognized, post: postVendorBill,
	}
}

// buildVendorBill runs the deterministic stages over res.Pages. A bill never
// gets a revision or a quote reference: "Change Order" or "Credit" wording on
// it is prose or a different document, not an amended bill.
func buildVendorBill(res *Result, lim Limits, opts Options) error {
	tbl, ho, err := buildVendorSide(res, lim, opts)
	if err != nil {
		return err
	}
	postVendorBill(res)
	finishDoc(res, ho.Keys, tbl.Roles, vendorBillProfile())
	return nil
}

// postVendorBill derives the due date from the payment terms when the bill
// has none, flags a due date before the invoice date and a negative total.
// It is safe to run again after the model fills the invoice date.
func postVendorBill(res *Result) {
	h := &res.Header
	if !h.DueDate.Found() {
		if due, ok := dueFromTerms(h.InvoiceDate, h.PaymentTerms); ok {
			h.DueDate = due
			addWarning(res, WarnDueFromTerms)
		}
	}
	if h.DueDate.Found() && h.InvoiceDate.Found() {
		inv, e1 := time.Parse(isoDateLayout, h.InvoiceDate.Value)
		due, e2 := time.Parse(isoDateLayout, h.DueDate.Value)
		if e1 == nil && e2 == nil && due.Before(inv) {
			h.DueDate.Confidence = ConfCheck
			addWarning(res, WarnInvalidDate)
		}
	}
	if fieldCents(h.Total) < 0 || fieldCents(h.Subtotal) < 0 {
		addWarning(res, WarnCreditDocument)
	}
}

// dueFromTerms adds the days of a "Net 30" / "30 days" term to the invoice
// date. The result is an inferred value: default source, check confidence.
func dueFromTerms(invoice, terms Field) (Field, bool) {
	if !invoice.Found() || !terms.Found() {
		return Field{}, false
	}
	m := termsDaysRe.FindStringSubmatch(terms.Value)
	if m == nil {
		return Field{}, false
	}
	digits := m[1]
	if digits == "" {
		digits = m[2]
	}
	days, err := strconv.Atoi(digits)
	if err != nil || days > maxTermsDays {
		return Field{}, false
	}
	start, err := time.Parse(isoDateLayout, invoice.Value)
	if err != nil {
		return Field{}, false
	}
	return Field{
		Value: start.AddDate(0, 0, days).Format(isoDateLayout), Source: SourceDefault, Confidence: ConfCheck,
		Snippet: clip(terms.Value), Page: terms.Page, Row: terms.Row,
	}, true
}

// vendorBillUnresolved lists required bill fields the parser could not
// resolve: the vendor, the vendor's invoice number, the invoice and due dates
// and at least one product line.
func vendorBillUnresolved(r *Result) []string {
	var out []string
	h := r.Header
	for _, k := range []struct {
		key string
		f   Field
	}{{KeyVendorName, h.VendorName}, {KeyInvoiceNumber, h.InvoiceNumber}, {KeyInvoiceDate, h.InvoiceDate}, {KeyDueDate, h.DueDate}} {
		if !k.f.Found() {
			out = append(out, k.key)
		}
	}
	if productCount(r) == 0 {
		out = append(out, KeyLines)
	}
	return out
}

// vendorBillRecommendedWarnings warns about a missing invoice number or date.
func vendorBillRecommendedWarnings(r *Result) []string {
	var out []string
	if !r.Header.InvoiceNumber.Found() {
		out = append(out, WarnMissingInvoiceNumber)
	}
	if !r.Header.InvoiceDate.Found() {
		out = append(out, WarnMissingInvoiceDate)
	}
	return out
}

// vendorBillLLMKeys lists the header keys the model may be asked for.
func vendorBillLLMKeys(r *Result) []string {
	var out []string
	h := r.Header
	for _, k := range []struct {
		key string
		f   Field
	}{{KeyVendorName, h.VendorName}, {KeyInvoiceNumber, h.InvoiceNumber}, {KeyInvoiceDate, h.InvoiceDate}, {KeyDueDate, h.DueDate}, {KeyPONumber, h.PONumber}} {
		if !k.f.Found() {
			out = append(out, k.key)
		}
	}
	return out
}

// vendorBillNotRecognized: no title class, no lines, no invoice number and no vendor.
func vendorBillNotRecognized(res *Result) bool {
	return res.ClassifiedAs == "" && len(res.Lines) == 0 &&
		!res.Header.InvoiceNumber.Found() && !res.Header.VendorName.Found()
}
