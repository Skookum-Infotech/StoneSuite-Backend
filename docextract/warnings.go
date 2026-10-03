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
)
