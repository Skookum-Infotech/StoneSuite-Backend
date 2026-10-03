package docextract

// Required-field keys reported in Result.Unresolved.
const (
	KeyCustomerName = "customer_name"
	KeyLines        = "lines"
	KeyPONumber     = "po_number"
	KeyOrderDate    = "order_date"
	KeyDeliveryDate = "delivery_date"
	KeyPaymentTerms = "payment_terms"
)

// salesOrderUnresolved lists required sales-order fields the parser could not
// resolve: a customer and at least one product line.
func salesOrderUnresolved(r *Result) []string {
	var out []string
	if !r.Header.CustomerName.Found() {
		out = append(out, KeyCustomerName)
	}
	products := 0
	for _, l := range r.Lines {
		if l.Kind == KindProduct {
			products++
		}
	}
	if products == 0 {
		out = append(out, KeyLines)
	}
	return out
}

// salesOrderRecommendedWarnings warns about missing recommended fields.
func salesOrderRecommendedWarnings(r *Result) []string {
	var out []string
	if !r.Header.PONumber.Found() {
		out = append(out, WarnMissingPONumber)
	}
	if !r.Header.OrderDate.Found() {
		out = append(out, WarnMissingOrderDate)
	}
	return out
}

// salesOrderLLMKeys lists header keys the LLM may be asked to fill: the
// unresolved required header fields plus missing recommended ones. Lines are
// never LLM-extracted.
func salesOrderLLMKeys(r *Result) []string {
	var out []string
	if !r.Header.CustomerName.Found() {
		out = append(out, KeyCustomerName)
	}
	if !r.Header.PONumber.Found() {
		out = append(out, KeyPONumber)
	}
	if !r.Header.OrderDate.Found() {
		out = append(out, KeyOrderDate)
	}
	return out
}
