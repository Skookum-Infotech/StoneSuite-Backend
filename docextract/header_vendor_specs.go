package docextract

import "regexp"

// Vendor-side label keys, used by purchase orders and vendor bills only. The
// sales-order vocabulary in labelSpecs is untouched.
const (
	keyVendor  = "vendor"       // the party that is (probably) not us
	keyRemit   = "remit"        // remit-to / pay-to block: weaker than the letterhead
	keyInvNo   = "invoice_no"   // the vendor's invoice number
	keyInvDate = "invoice_date" // the vendor's invoice date
	keyDue     = "due_date"     // payment due date
	keyIgnore  = "ignored"      // claimed so a shorter label can't match inside it
)

const (
	prioStrong = 3
	prioMid    = 2
	prioWeak   = 1
)

var (
	vDueRe        = regexp.MustCompile(`(?i)\b(?:due\s+date|payment\s+due(?:\s+date)?|date\s+due|pay\s+by)\b`)
	vPayDueRe     = regexp.MustCompile(`(?i)\bpayment\s+due(?:\s+date)?\b`)
	vInvDateRe    = regexp.MustCompile(`(?i)\b(?:(?:invoice|inv\.?|bill(?:ing)?)\s+date|date\s+of\s+(?:invoice|bill))\b`)
	vOtherDateRe  = regexp.MustCompile(`(?i)\b(?:order\s+date|po\s+date|ship(?:ping)?\s+date|delivery\s+date|date\s+(?:shipped|ordered)|service\s+date)\b`)
	vInvNoRe      = regexp.MustCompile(`(?i)\b(?:invoice|inv|bill)\s*(?:#|no\b\.?|number)`)
	vInvTitleRe   = regexp.MustCompile(`(?i)\bi\s?n\s?v\s?o\s?i\s?c\s?e\b`)
	vBillPORe     = regexp.MustCompile(`(?i)\b(?:(?:customer|your|buyer'?s?)\s+)?(?:p\.?\s?o\.?|purchase\s+order)(?:\s*(?:#|no\b\.?|number)|[\s:]|$)`)
	vYourOrderRe  = regexp.MustCompile(`(?i)\byour\s+order\s*(?:#|no\b\.?|number)`)
	vDeliveryRe   = regexp.MustCompile(`(?i)\b(?:delivery\s+date|ship(?:ping)?\s+date|date\s+required|required\s+(?:by|date)|need(?:ed)?\s+by|due\s+date|expected(?:\s+(?:delivery|ship))?(?:\s+date)?|requested\s+(?:delivery|ship)(?:\s+date)?|delivery\s+by|ship\s+by|promised(?:\s+date)?)\b`)
	vPODateRe     = regexp.MustCompile(`(?i)\b(?:order\s+date|date\s+ordered|po\s+date|date\s+of\s+order|date\s+issued|issue\s+date|(?:quote|quotation|pro-?\s?forma|invoice)\s+date)\b`)
	vQuoteNoRe    = regexp.MustCompile(`(?i)\b(?:quote|quotation|estimate|proposal|pro-?\s?forma(?:\s+invoice)?)\s*(?:#|no\b\.?|number)`)
	vOurBlockRe   = regexp.MustCompile(`(?i)\b(?:quot(?:e|ed)\s+to|prepared\s+for|bill(?:ed)?\s+to|sold\s+to|invoice\s+to)\b`)
	vCustomerRe   = regexp.MustCompile(`(?i)\b(?:customer(?:\s+name)?|buyer)\b`)
	vVendorRe     = regexp.MustCompile(`(?i)\b(?:vendor|supplier|sold\s+by|bill\s+from|issued\s+by)(?:\s+(?:name|address|info(?:rmation)?))?\b`)
	vRemitRe      = regexp.MustCompile(`(?i)\b(?:remit(?:tance)?\s+(?:to|address)|pay\s+to|make\s+checks\s+payable\s+to)\b`)
	vFromRe       = regexp.MustCompile(`(?i)\bfrom\b`)
	vToRe         = regexp.MustCompile(`(?i)\bto\b`)
	vInvoiceTotRe = regexp.MustCompile(`(?i)\b(?:invoice|grand)\s+total\b|\btotal\s+invoice(?:d)?\b`)
	vBalanceDueRe = regexp.MustCompile(`(?i)\b(?:total|amount|balance)\s+due\b`)
	vTotalRe      = regexp.MustCompile(`(?i)\btotal(?:\s+amount)?\b`)
	vIDLikeRe     = regexp.MustCompile(`(?i)^\s*(?:#|no\b|number|id\b|code|acct|account)`)
	vPlainDateRe  = regexp.MustCompile(`(?i)\bdate\b`)
	vShipToRe     = regexp.MustCompile(`(?i)\b(?:ship(?:ping)?\s+to|deliver(?:y)?\s+to|ship(?:ping)?\s+address|delivery\s+address|job\s*site)\b`)
	vTermsRe      = regexp.MustCompile(`(?i)\b(?:payment\s+terms|terms(?:\s+of\s+payment)?|terms)\b`)
	vSubtotalRe   = regexp.MustCompile(`(?i)\bsub\s*-?\s*total\b`)
	vTaxRe        = regexp.MustCompile(`(?i)\b(?:sales\s+tax|taxes|tax|vat|gst)\b`)
	vShippingRe   = regexp.MustCompile(`(?i)\b(?:shipping(?:\s*(?:&|and)\s*handling)?|freight|handling|delivery\s+(?:fee|charge))\b`)
	vDiscountRe   = regexp.MustCompile(`(?i)\bdiscount\b`)
	vPOOrderRe    = regexp.MustCompile(`(?i)\b(?:(?:customer|your|buyer'?s?)\s+)?(?:p\.?\s?o\.?|purchase\s+order|order)\s*(?:#|no\b\.?|number)`)
	vPOTitleRe    = regexp.MustCompile(`(?i)\b(?:(?:customer|your|buyer'?s?)\s+)?(?:p\.?o\.?|purchase\s+order)\b`)
	vOrderTotalRe = regexp.MustCompile(`(?i)\b(?:grand\s+total|order\s+total|invoice\s+total|total\s+due|amount\s+due|balance\s+due)\b`)
)

// idRejects rejects a label followed by "#", "No", "ID" ...: "Vendor No. 4412"
// is our account number at the vendor, not the vendor.
func idRejects(rest string) bool { return vIDLikeRe.MatchString(rest) }

// vendorSpecs returns the label specs for a purchase order or vendor bill.
// Specs earlier in the list claim their text first. quoteFirst makes a
// "Quote #" outrank other numbers on a purchase order (a vendor quote's own
// number is the reference; an own PO's number comes from its PO label).
func vendorSpecs(dt DocType, quoteFirst bool) []labelSpec {
	if dt == DocTypeVendorBill {
		return vendorBillSpecs()
	}
	return purchaseOrderSpecs(quoteFirst)
}

func vendorBillSpecs() []labelSpec {
	return []labelSpec{
		{key: keyDue, prio: prioMid, below: true, re: vDueRe},
		{key: keyInvDate, prio: prioMid, below: true, re: vInvDateRe},
		{key: keyIgnore, prio: prioMid, below: true, re: vOtherDateRe},
		{key: keyInvNo, prio: prioMid, below: true, re: vInvNoRe},
		{key: keyPO, prio: prioMid, below: true, re: vBillPORe},
		{key: keyPO, prio: prioMid, below: true, re: vYourOrderRe},
		{key: keyShipTo, prio: prioMid, below: true, re: vShipToRe},
		{key: keyBillTo, prio: prioStrong, below: true, re: vOurBlockRe},
		{key: keyBillTo, prio: prioWeak, below: true, labelOnly: true, reject: idRejects, re: vCustomerRe},
		{key: keyVendor, prio: prioStrong, below: true, reject: idRejects, re: vVendorRe},
		{key: keyRemit, prio: prioMid, below: true, re: vRemitRe},
		{key: keyTerms, prio: prioWeak, below: true, re: vTermsRe},
		{key: keySubtotal, prio: prioWeak, below: true, re: vSubtotalRe},
		{key: keyTotal, prio: prioStrong, below: true, re: vInvoiceTotRe},
		{key: keyTotal, prio: prioWeak, below: true, re: vBalanceDueRe},
		{key: keyTotal, prio: prioMid, below: true, re: vTotalRe, reject: func(rest string) bool { return totalRejectRe.MatchString(rest) }},
		{key: keyTax, prio: prioWeak, below: true, re: vTaxRe},
		{key: keyShipping, prio: prioWeak, below: true, re: vShippingRe},
		{key: keyDiscount, prio: prioWeak, below: true, re: vDiscountRe},
		{key: keyInvNo, prio: prioWeak, nearBelow: true, re: vInvTitleRe},
		{key: keyInvDate, prio: prioWeak, below: true, re: vPlainDateRe},
		{key: keyVendor, prio: prioWeak, below: true, labelOnly: true, re: vFromRe},
	}
}

func purchaseOrderSpecs(quoteFirst bool) []labelSpec {
	quotePrio := prioWeak
	if quoteFirst {
		quotePrio = prioStrong
	}
	return []labelSpec{
		{key: keyIgnore, prio: prioMid, below: true, re: vPayDueRe},
		{key: keyDelivery, prio: prioMid, below: true, re: vDeliveryRe},
		{key: keyOrderDate, prio: prioMid, below: true, re: vPODateRe},
		{key: keyPO, prio: quotePrio, below: true, re: vQuoteNoRe},
		{key: keyPO, prio: prioMid, below: true, re: vPOOrderRe},
		{key: keyPO, prio: prioWeak, nearBelow: true, re: vPOTitleRe},
		{key: keyShipTo, prio: prioMid, below: true, re: vShipToRe},
		{key: keyBillTo, prio: prioStrong, below: true, re: vOurBlockRe},
		{key: keyBillTo, prio: prioWeak, below: true, labelOnly: true, reject: idRejects, re: vCustomerRe},
		{key: keyVendor, prio: prioStrong, below: true, reject: idRejects, re: vVendorRe},
		{key: keyRemit, prio: prioMid, below: true, re: vRemitRe},
		{key: keyTerms, prio: prioWeak, below: true, re: vTermsRe},
		{key: keySubtotal, prio: prioWeak, below: true, re: vSubtotalRe},
		{key: keyTotal, prio: prioMid, below: true, re: vOrderTotalRe},
		{key: keyTotal, prio: prioWeak, below: true, re: vTotalRe, reject: func(rest string) bool { return totalRejectRe.MatchString(rest) }},
		{key: keyTax, prio: prioWeak, below: true, re: vTaxRe},
		{key: keyShipping, prio: prioWeak, below: true, re: vShippingRe},
		{key: keyDiscount, prio: prioWeak, below: true, re: vDiscountRe},
		{key: keyOrderDate, prio: prioWeak, below: true, re: vPlainDateRe},
		{key: keyVendor, prio: prioWeak, below: true, labelOnly: true, re: vFromRe},
		{key: keyVendor, prio: prioWeak, below: true, labelOnly: true, re: vToRe},
	}
}
