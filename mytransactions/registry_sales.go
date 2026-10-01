package mytransactions

import "stonesuite-backend/authz"

// salesSources lists the customer-facing sales documents. Each module key is
// also its frontend route segment except fabrication_job, which the frontend
// routes under sales/installation (and RBAC guards as ResourceInstallation).
func salesSources() []Source {
	job := salesDoc("fabrication_job", "Installation Job", authz.ResourceInstallation,
		"installation", "fabrication_job", "fabrication_job", "")
	job.Owner = "job_owner_id"

	return []Source{
		salesDoc("quote", "Quote", authz.ResourceQuote, "quote", "quote", "quote", "quote_grand_total"),
		salesDoc("estimate", "Estimate", authz.ResourceEstimate, "estimate", "estimate", "estimate", "estimate_grand_total"),
		salesDoc("sales_order", "Sales Order", authz.ResourceSalesOrder, "sales_order", "sales_order", "sales_order", "sales_order_grand_total"),
		job,
		salesDoc("invoice", "Invoice", authz.ResourceInvoice, "invoice", "invoice", "invoice", "invoice_grand_total"),
		salesDoc("payment", "Payment", authz.ResourcePayment, "payment", "payment", "payment", "payment_amount"),
		salesDoc("credit_memo", "Credit Memo", authz.ResourceCreditMemo, "credit_memo", "credit_memo", "credit_memo", "credit_memo_grand_total"),
		salesDoc("refund", "Refund", authz.ResourceRefund, "refund", "refund", "refund", "refund_amount"),
	}
}
