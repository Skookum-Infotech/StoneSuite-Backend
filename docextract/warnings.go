package docextract

// Warning identifiers added to Result.Warnings.
const (
	WarnMultipleDocuments = "multiple_documents"
	WarnNonUSDCurrency    = "non_usd_currency"
	WarnTaxInclusive      = "tax_inclusive_total"
	WarnInvalidDate       = "invalid_date"
	WarnAIUnavailable     = "ai_unavailable"
	WarnContextBudget     = "context_budget"
	WarnMissingPONumber   = "missing_po_number"
	WarnMissingOrderDate  = "missing_order_date"
	WarnNoLineTable       = "no_line_table"
	WarnInjectionPhrases  = "injection_phrases_detected"
	WarnChargeIgnored     = "charge_not_mapped"
	WarnDocLooksLike      = "document_looks_like_"
	WarnNotRecognized     = "document_not_recognized"
	WarnNoPrices          = "no_prices"
	// WarnMissingInvoiceNumber: a vendor bill without the vendor's invoice number.
	WarnMissingInvoiceNumber = "missing_invoice_number"
	// WarnMissingInvoiceDate: a vendor bill without an invoice date.
	WarnMissingInvoiceDate = "missing_invoice_date"
	// WarnDueFromTerms: the bill's due date was computed from its payment terms.
	WarnDueFromTerms = "due_date_from_terms"
	// WarnCustomerPO: a purchase order upload that names us as the vendor, so
	// it is a customer's order to us (a sales order), not our order to a vendor.
	WarnCustomerPO = "looks_like_customer_po"
	// WarnCreditDocument: a vendor credit (negative total), not a bill.
	WarnCreditDocument = "credit_document"
)
