package docextract

import "strings"

// Document classes returned by Classify.
const (
	ClassPurchaseOrder = "purchase_order"
	ClassSalesOrder    = "sales_order"
	ClassInvoice       = "invoice"
	ClassQuote         = "quote"
	ClassEstimate      = "estimate"
	ClassVendorBill    = "vendor_bill"
	ClassCreditMemo    = "credit_memo"
	// ClassStatement is an account statement (a list of invoices), never a
	// single bill or order.
	ClassStatement = "statement"
	// ClassPackingSlip is a delivery document (packing slip/list, delivery
	// ticket, bill of lading): goods, not a bill or an order.
	ClassPackingSlip = "packing_slip"
	// ClassOrderForm is a filled customer order/spec form (a builder's
	// countertop order form): an order, just not a priced PO. It is set only
	// when the form's fields were actually read (parseOrderForm), never from
	// a title, so a priced PDF headed "Order Form" is handled as a PO.
	ClassOrderForm = "order_form"
)

const (
	classifyTitleRows = 8 // rows from the top of page one that may hold a title
	classifyMaxWords  = 6 // a title row is short
)

type classKeyword struct {
	phrase string
	class  string
}

// classKeywords are checked in order against the start of short title rows.
var classKeywords = []classKeyword{
	{"purchase order", ClassPurchaseOrder},
	{"sales order", ClassSalesOrder},
	{"order confirmation", ClassSalesOrder},
	{"credit memo", ClassCreditMemo},
	{"credit note", ClassCreditMemo},
	{"statement", ClassStatement},
	{"packing slip", ClassPackingSlip},
	{"packing list", ClassPackingSlip},
	{"delivery ticket", ClassPackingSlip},
	{"bill of lading", ClassPackingSlip},
	{"pro forma", ClassQuote},
	{"proforma", ClassQuote},
	{"quotation", ClassQuote},
	{"quote", ClassQuote},
	{"estimate", ClassEstimate},
	{"invoice", ClassInvoice},
	{"vendor bill", ClassVendorBill},
	{"bill", ClassVendorBill},
}

// Classify guesses the document class from title-like rows near the top of the
// first page. It returns "" when nothing looks like a title.
func Classify(pages []PageRows) string {
	if len(pages) == 0 {
		return ""
	}
	rows := pages[0].Rows
	if len(rows) > classifyTitleRows {
		rows = rows[:classifyTitleRows]
	}
	for _, r := range rows {
		text := strings.ToLower(strings.TrimSpace(r.Text()))
		if letterSpaced(text) {
			if c := classifyLetterSpaced(text); c != "" {
				return c
			}
			continue
		}
		if len(strings.Fields(text)) > classifyMaxWords {
			continue
		}
		for _, k := range classKeywords {
			if !strings.HasPrefix(text, k.phrase) {
				continue
			}
			rest := strings.Fields(strings.TrimPrefix(text, k.phrase))
			if len(rest) > 0 && (rest[0] == "to" || rest[0] == "to:" || rest[0] == "date" || rest[0] == "date:") {
				continue
			}
			return k.class
		}
	}
	return ""
}

// minSpacedLetters is how many one-letter words make a letter-spaced title
// ("I N V O I C E").
const minSpacedLetters = 4

// letterSpaced reports whether a row is a title typeset one letter at a time.
func letterSpaced(text string) bool {
	ws := strings.Fields(text)
	if len(ws) < minSpacedLetters {
		return false
	}
	for _, w := range ws {
		if len([]rune(w)) != 1 {
			return false
		}
	}
	return true
}

// squash removes every space.
func squash(s string) string { return strings.Join(strings.Fields(s), "") }

// classifyLetterSpaced classifies a letter-spaced title row by comparing it
// with the keyword phrases with their spaces removed.
func classifyLetterSpaced(text string) string {
	t := squash(text)
	for _, k := range classKeywords {
		if strings.HasPrefix(t, squash(k.phrase)) {
			return k.class
		}
	}
	return ""
}

// isTitleText reports whether text reads as a document title, spaced or not.
func isTitleText(s string) bool {
	low := strings.ToLower(strings.TrimSpace(s))
	if letterSpaced(low) {
		return classifyLetterSpaced(low) != ""
	}
	for _, k := range classKeywords {
		if strings.HasPrefix(low, k.phrase) {
			return true
		}
	}
	return false
}

// ClassMismatch reports whether a classified title contradicts the target type.
// A customer PO is the expected input for a sales order.
func ClassMismatch(target DocType, class string) bool {
	if class == "" {
		return false
	}
	switch target {
	case DocTypeSalesOrder:
		return class != ClassPurchaseOrder && class != ClassSalesOrder && class != ClassOrderForm
	case DocTypePurchaseOrder:
		return class != ClassPurchaseOrder && class != ClassSalesOrder && class != ClassQuote && class != ClassOrderForm
	case DocTypeVendorBill:
		return class != ClassInvoice && class != ClassVendorBill
	default:
		return false
	}
}
