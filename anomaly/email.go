package anomaly

import (
	"strings"

	"stonesuite-backend/services"
)

const (
	emailActionLabel = "Review anomaly"
	emailReason      = "an anomaly was sent to you to review."
	emailFollowUp    = "Open Anomalies in StoneSuite to review it, then confirm it as a problem, dismiss it, or leave a note."
)

// ruleLabels are the human names of the shipped rules, shown in the email's
// details box. They mirror anomalyRuleLabel in the web app.
var ruleLabels = map[string]string{
	RuleSelfApproval:          "Approved by its own owner",
	RuleUnbalancedJournal:     "Unbalanced journal entry",
	RuleDuplicateVendorBill:   "Possible duplicate vendor bill",
	RuleHighDiscount:          "Unusually high discount",
	RuleBelowCostSale:         "Sold below cost",
	RuleUnusualOrderTotal:     "Unusually large order",
	RuleDuplicateInvoice:      "Possible duplicate invoice",
	RuleOverCreditLimit:       "Over credit limit",
	RuleOverdueInvoice:        "Invoice well past due",
	RuleStaleUnappliedPayment: "Payment never applied",
	RuleRefundExceedsPayment:  "Refund larger than the payment",
	RuleQuickRefund:           "Refund right after payment",
	RuleCreditMemoBurst:       "Several credit memos for one customer",
	RuleVoidedAfterPosting:    "Voided after being issued",
	RuleBackdatedDocument:     "Date far from when it was entered",
	RulePriceDriftOnConvert:   "Order differs from its quote",
	RuleZeroTaxUnusual:        "No sales tax charged",
	RuleFilledNotInvoiced:     "Filled order never invoiced",
	RuleBillExceedsPO:         "Bills exceed the purchase order",
	RuleVendorOverpaid:        "Paid more than owed",
	RuleNewVendorLargePayment: "Large payment to a new vendor",
	RulePOPriceAboveCost:      "Purchase price above the usual cost",
	RuleSplitPurchaseOrders:   "Purchase orders split across one day",
	RulePOLate:                "Purchase order overdue",
	RulePOPartialStuck:        "Partly received order has stalled",
	RuleExpenseDuplicate:      "Repeated expense",
	RuleExpenseUnusuallyHigh:  "Unusually high expense",
	RuleOverAllocated:         "More reserved than in stock",
	RuleStockLedgerMismatch:   "Stock does not match its history",
	RuleLargeWriteOff:         "Large stock write-off",
	RuleAdjustmentBurst:       "Many stock adjustments",
	RuleCountVarianceHigh:     "Large stock count differences",
	RuleReservedForClosed:     "Stock reserved for a cancelled order",
	RuleDeadStock:             "Slabs not moving",
	RuleBinTransferStuck:      "Bin transfer waiting too long",
	RuleReceivedSlabMismatch:  "Received slab count differs",
	RuleShrinkageSpike:        "Stock loss spike",
	RuleJobStuck:              "Fabrication job not moving",
	RuleInstallOverdue:        "Install date missed",
	RuleScheduleOverdue:       "Late scheduled activity",
	RuleManyTemplateRevs:      "Template redone many times",
	RuleMultipleJobsPerSale:   "More than one job on an order",
	RulePostedInClosedPeriod:  "Posted into a closed period",
	RuleUnusualCashTransfer:   "Unusually large cash transfer",
	RuleChartOfAccountsEdit:   "Chart of accounts changed",
}

// severityBadges are the banner pill per severity.
var severityBadges = map[Severity]string{
	SeverityCritical: "Critical Alert",
	SeverityWarning:  "Needs Review",
	SeverityInfo:     "For Your Information",
}

// ruleLabel names a rule, falling back to its key for one not listed above.
func ruleLabel(key string) string {
	if l, ok := ruleLabels[key]; ok {
		return l
	}
	return strings.ReplaceAll(key, "_", " ")
}

// buildEmail fills the shared email template the same way the approval-chain
// emails do (banner badge and icon, greeting, details box, one action button),
// so an anomaly alert looks like the rest of StoneSuite's mail.
func buildEmail(a pendingAlert) *services.Email {
	badge, ok := severityBadges[Severity(a.severity)]
	if !ok {
		badge = severityBadges[SeverityInfo]
	}
	e := &services.Email{
		Preheader: a.title + ".",
		Badge:     badge,
		Icon:      services.IconDocClock,
		Heading:   a.title,
		Subtitle:  "Please review and take action.",
		Greet:     true,
		Details: []services.Detail{
			{Label: "Anomaly", Value: ruleLabel(a.ruleKey)},
			{Label: "Severity", Value: severityLabel(Severity(a.severity))},
			{Label: "Detected On", Value: services.FormatEmailDate(a.detectedAt)},
		},
		ActionURL:   services.AppURL(AnomaliesLink),
		ActionLabel: emailActionLabel,
		Reason:      emailReason,
	}
	if a.detail != "" {
		e.Paragraphs = append(e.Paragraphs, services.Paragraph{services.Text(a.detail)})
	}
	e.Paragraphs = append(e.Paragraphs, services.Paragraph{services.Text(emailFollowUp)})
	return e
}

// severityLabel is the title-case word for a severity.
func severityLabel(s Severity) string {
	if s == "" {
		return ""
	}
	return strings.ToUpper(string(s)[:1]) + string(s)[1:]
}
