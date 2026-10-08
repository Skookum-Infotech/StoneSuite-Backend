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
		return class != ClassPurchaseOrder && class != ClassSalesOrder && class != ClassQuote
	case DocTypeVendorBill:
		return class != ClassInvoice && class != ClassVendorBill
	default:
		return false
	}
}
