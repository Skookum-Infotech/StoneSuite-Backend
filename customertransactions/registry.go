// Package customertransactions lists every transaction one customer has across
// the sales documents (quote, estimate, sales order, installation job, invoice,
// payment, credit memo, refund) plus the customer's live accounts-receivable
// summary. It backs the Transactions tab on the customer page.
//
// It is a read-only browse surface modelled on mytransactions and distinct from
// the record filter engine in query/: one UNION ALL over the registry below,
// built only from the registry's static table/column names, with every
// client-supplied value (customer id, filters, paging) bound as a $n parameter.
//
// The registry is the single source of truth for which documents participate.
// The open/overdue definitions are hard-coded per document type: when a document
// gains a status, its OpenWhere here needs a matching update. registry_test.go
// cross-checks the registry against mytransactions so a newly added
// customer-facing module cannot be forgotten.
package customertransactions

import (
	"strings"

	"stonesuite-backend/authz"
)

// Route and presentation constants shared by every source.
const (
	domainSales = "sales"
	// statusAlias is the lkp_record_status alias every branch joins.
	statusAlias = "s"
	// statusCodeExpr is the SQL expression for a branch's status code.
	statusCodeExpr = statusAlias + ".record_status_code"
)

// Record-status codes the open and balance definitions depend on.
const (
	codeDraft           = "DRFT"
	codePendingApproval = "PAPV"
	codeApproved        = "APPV"
	codeSent            = "SENT"
	codePartial         = "PART"
	codeOverdue         = "ODUE"
	codeOpen            = "OPEN"
	codeComplete        = "COMP"
	codeCancelled       = "CANC"
	codeVoid            = "VOID"
	codePending         = "PEND"
)

// Source describes one document type's table and how to read its list-row
// fields. Every string is static SQL authored in this package, never request
// data. Expressions reference the base table as alias t and the status lookup as
// alias s; fields documented as "column" are bare column names on the base table.
// An empty DueDate, Amount or Balance means the document has no such field (NULL).
type Source struct {
	Key      string         // registry key == the row's "type"
	Label    string         // singular display name, e.g. "Sales Order"
	Resource authz.Resource // RBAC resource gating read access
	Domain   string         // frontend route segment 1
	Module   string         // frontend route segment 2 (not always == Key, e.g. installation)

	Table       string // base table, aliased t
	CustomerCol string // column: INTEGER FK to customer.customer_id
	StatusCol   string // column: FK to lkp_record_status
	DeletedAt   string // column: soft-delete marker; deleted rows never list
	CreatedAt   string // column
	UpdatedAt   string // column
	Owner       string // column the module's own "own" scope narrows on

	ID        string // expression: record uuid (the id the detail page routes on)
	Number    string // expression: human reference
	Date      string // expression: document date (a DATE)
	DueDate   string // expression: due / valid-until / promised date, "" when none
	Amount    string // expression: single monetary total, "" when none
	Balance   string // expression: outstanding or unapplied amount, "" when none
	OpenWhere string // predicate: the record is still "open" (needs action or money)

	// Overdue marks the one type whose open records can be past DueDate.
	Overdue bool
}

// Sources returns every document type on the customer Transactions tab, in a
// stable order.
func Sources() []Source {
	return []Source{
		quoteLike("quote", "Quote", authz.ResourceQuote, "quote", "quote_valid_until"),
		quoteLike("estimate", "Estimate", authz.ResourceEstimate, "estimate", "estimate_valid_until"),
		salesOrder(),
		installationJob(),
		invoice(),
		payment(),
		creditMemo(),
		refund(),
	}
}

// sourceByKey finds a registered source by its key.
func sourceByKey(key string) (Source, bool) {
	for _, s := range Sources() {
		if s.Key == key {
			return s, true
		}
	}
	return Source{}, false
}

// statusIn renders a predicate on the status code being one of codes.
func statusIn(codes ...string) string {
	return statusCodeExpr + " IN ('" + strings.Join(codes, "','") + "')"
}

// doc builds the clone-twin column skeleton shared by the sales documents:
// <p>_uuid, <p>_number, <p>_status, <p>_customer_id, <p>_date, <p>_owner_id,
// <p>_created_at/_updated_at/_deleted_at. Callers override what differs.
func doc(key, label string, res authz.Resource, module, table, p string) Source {
	return Source{
		Key: key, Label: label, Resource: res, Domain: domainSales, Module: module,
		Table:       table,
		CustomerCol: p + "_customer_id",
		StatusCol:   p + "_status",
		DeletedAt:   p + "_deleted_at",
		CreatedAt:   p + "_created_at",
		UpdatedAt:   p + "_updated_at",
		Owner:       p + "_owner_id",
		ID:          "t." + p + "_uuid",
		Number:      "t." + p + "_number",
		Date:        "t." + p + "_date",
	}
}

// quoteLike builds a quote or estimate: an offer that is open until it is
// accepted, rejected or expires.
func quoteLike(key, label string, res authz.Resource, p, validUntil string) Source {
	s := doc(key, label, res, key, key, p)
	s.DueDate = "t." + validUntil
	s.Amount = "t." + p + "_grand_total"
	s.OpenWhere = statusIn(codeDraft, codePendingApproval, codeApproved, codeSent)
	return s
}

// salesOrder builds the sales order source: open until filled or cancelled.
func salesOrder() Source {
	s := doc("sales_order", "Sales Order", authz.ResourceSalesOrder, "sales_order", "sales_order", "sales_order")
	s.Amount = "t.sales_order_grand_total"
	s.OpenWhere = statusIn(codeDraft, codePendingApproval, codeApproved, codeOpen, codePartial)
	return s
}

// installationJob builds the fabrication job source. The frontend routes it
// under sales/installation and RBAC guards it as ResourceInstallation. It has no
// document date, so the date is its creation day, and it has no money.
func installationJob() Source {
	s := doc("fabrication_job", "Installation Job", authz.ResourceInstallation, "installation", "fabrication_job", "fabrication_job")
	s.Owner = "job_owner_id"
	s.Date = "t.fabrication_job_created_at::date"
	s.DueDate = "t.job_promised_install_date"
	s.OpenWhere = "NOT " + statusIn(codeComplete, codeCancelled)
	return s
}

// invoice builds the invoice source: open while it still owes money (the
// invoice/aging.go outstanding definition); only invoices can be overdue.
func invoice() Source {
	s := doc("invoice", "Invoice", authz.ResourceInvoice, "invoice", "invoice", "invoice")
	s.DueDate = "t.invoice_due_date"
	s.Amount = "t.invoice_grand_total"
	s.Balance = "t.invoice_balance_due"
	s.OpenWhere = statusIn(codeSent, codePartial, codeOverdue) + " AND t.invoice_balance_due > 0"
	s.Overdue = true
	return s
}

// payment builds the payment source: its balance is the unapplied money still
// available to the customer (automation/ar_source.go available-credit
// definition), open while a non-void payment holds some.
func payment() Source {
	s := doc("payment", "Payment", authz.ResourcePayment, "payment", "payment", "payment")
	s.Amount = "t.payment_amount"
	s.Balance = "GREATEST(t.payment_unapplied_amount - t.payment_refunded_total - t.payment_credited_total, 0)"
	s.OpenWhere = statusCodeExpr + " <> '" + codeVoid + "' AND " + s.Balance + " > 0"
	return s
}

// creditMemo builds the credit memo source: only an approved memo's unapplied
// remainder is usable credit.
func creditMemo() Source {
	s := doc("credit_memo", "Credit Memo", authz.ResourceCreditMemo, "credit_memo", "credit_memo", "credit_memo")
	s.Amount = "t.credit_memo_grand_total"
	s.Balance = "GREATEST(t.credit_memo_unapplied_amount - t.credit_memo_refunded_total, 0)"
	s.OpenWhere = statusCodeExpr + " = '" + codeApproved + "' AND " + s.Balance + " > 0"
	return s
}

// refund builds the refund source: open while it is pending.
func refund() Source {
	s := doc("refund", "Refund", authz.ResourceRefund, "refund", "refund", "refund")
	s.Amount = "t.refund_amount"
	s.OpenWhere = statusCodeExpr + " = '" + codePending + "'"
	return s
}
