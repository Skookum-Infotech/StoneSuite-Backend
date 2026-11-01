// Package vendortransactions lists every transaction one vendor has across the
// purchasing documents (requisition, purchase order, item receipt, vendor bill,
// vendor payment, vendor credit) plus the vendor's live accounts-payable
// summary. It backs the Transactions tab on the vendor page.
//
// It is a read-only browse surface modelled on mytransactions and
// customertransactions, distinct from the record filter engine in query/: one
// UNION ALL over the registry below, built only from the registry's static
// table/column names, with every client-supplied value (vendor id, filters,
// cursor, limit) bound as a $n parameter.
//
// The registry is the single source of truth for which documents participate.
// The open/overdue definitions are hard-coded per document type: when a document
// gains a status, its OpenWhere here needs a matching update. registry_test.go
// cross-checks the registry against mytransactions so a newly added
// vendor-facing module cannot be forgotten.
package vendortransactions

import (
	"strings"

	"stonesuite-backend/authz"
)

// Route and presentation constants shared by every source.
const (
	domainPurchases = "purchases"
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
	codeScheduled       = "SCHD"
	codePartial         = "PART"
	codeOverdue         = "ODUE"
	codePending         = "PEND"
	codeClosed          = "CLSD"
	codeCancelled       = "CANC"
	codeVoid            = "VOID"
)

// Keys of the sources the summary reads by name.
const (
	keyVendorBill    = "vendor_bill"
	keyVendorPayment = "vendor_payment"
	keyVendorCredit  = "vendor_credit"
)

// Source describes one document type's table and how to read its list-row
// fields. Every string is static SQL authored in this package, never request
// data. Expressions reference the base table as alias t and the status lookup as
// alias s; fields documented as "column" are bare column names on the base table.
// An empty DueDate, Amount or Balance means the document has no such field (NULL).
type Source struct {
	Key      string         // registry key == the row's "type"
	Label    string         // singular display name, e.g. "Vendor Bill"
	Resource authz.Resource // RBAC resource gating read access
	Domain   string         // frontend route segment 1
	Module   string         // frontend route segment 2

	Table     string // base table, aliased t
	VendorCol string // column: INTEGER FK to vendor.vendor_id
	StatusCol string // column: FK to lkp_record_status
	DeletedAt string // column: soft-delete marker; deleted rows never list
	CreatedAt string // column
	UpdatedAt string // column
	Owner     string // column the module's own "own" scope narrows on

	ID        string // expression: record uuid (the id the detail page routes on)
	Number    string // expression: human reference
	Date      string // expression: document date (a DATE)
	DueDate   string // expression: due / expected / needed-by date, "" when none
	Amount    string // expression: single monetary total, "" when none
	Balance   string // expression: outstanding or unapplied amount, "" when none
	OpenWhere string // predicate: the record is still "open" (needs action or money)

	// Overdue marks the one type whose open records can be past DueDate.
	Overdue bool
}

// Sources returns every document type on the vendor Transactions tab, in a
// stable order.
func Sources() []Source {
	return []Source{
		requisition(),
		purchaseOrder(),
		itemReceipt(),
		vendorBill(),
		vendorPayment(),
		vendorCredit(),
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

// doc builds the clone-twin column skeleton shared by the purchasing documents:
// <p>_uuid, <p>_number, <p>_status, <p>_vendor_id, <p>_date, <p>_owner_id,
// <p>_created_at/_updated_at/_deleted_at. Callers override what differs.
func doc(key, label string, res authz.Resource, table, p string) Source {
	return Source{
		Key: key, Label: label, Resource: res, Domain: domainPurchases, Module: key,
		Table:     table,
		VendorCol: p + "_vendor_id",
		StatusCol: p + "_status",
		DeletedAt: p + "_deleted_at",
		CreatedAt: p + "_created_at",
		UpdatedAt: p + "_updated_at",
		Owner:     p + "_owner_id",
		ID:        "t." + p + "_uuid",
		Number:    "t." + p + "_number",
		Date:      "t." + p + "_date",
	}
}

// requisition builds the requisition source: an internal request-to-buy, open
// until it is cancelled. It has no document date, so the date is its creation
// day; its own-scope column is the requester, not an owner.
func requisition() Source {
	s := doc("requisition", "Requisition", authz.ResourceRequisition, "requisition", "requisition")
	s.Owner = "requisition_requested_by_id"
	s.Date = "t.requisition_created_at::date"
	s.DueDate = "t.requisition_needed_by_date"
	s.Amount = "t.requisition_estimated_total"
	s.OpenWhere = statusIn(codeDraft, codePendingApproval, codeApproved)
	return s
}

// purchaseOrder builds the purchase order source: open until closed or cancelled.
func purchaseOrder() Source {
	s := doc("purchase_order", "Purchase Order", authz.ResourcePurchaseOrder, "purchase_order", "purchase_order")
	s.DueDate = "t.purchase_order_expected_date"
	s.Amount = "t.purchase_order_grand_total"
	s.OpenWhere = "NOT " + statusIn(codeClosed, codeCancelled)
	return s
}

// itemReceipt builds the item receipt source: open while goods are still
// arriving (pending or partially received). It carries no money.
func itemReceipt() Source {
	s := doc("item_receipt", "Item Receipt", authz.ResourceItemReceipt, "item_receipt", "item_receipt")
	s.OpenWhere = statusIn(codePending, codePartial)
	return s
}

// vendorBill builds the vendor bill source: open while a bill in a payable
// status (vendorbill.PayableStatuses: APPV, PART, ODUE) still owes money. Only
// bills can be overdue: open and either past due or already flagged ODUE.
func vendorBill() Source {
	s := doc(keyVendorBill, "Vendor Bill", authz.ResourceVendorBill, "vendor_bill", "vendor_bill")
	s.DueDate = "t.vendor_bill_due_date"
	s.Amount = "t.vendor_bill_grand_total"
	s.Balance = "t.vendor_bill_balance_due"
	s.OpenWhere = statusIn(codeApproved, codePartial, codeOverdue) + " AND t.vendor_bill_balance_due > 0"
	s.Overdue = true
	return s
}

// vendorPayment builds the vendor payment source: its balance is the unapplied
// money still sitting with the vendor. It is open while any non-void payment
// holds some, the same rule vendorpayment.applyTx enforces (it only refuses
// VOID) and the AR twin in automation/ar_source.go uses for customer payments.
func vendorPayment() Source {
	s := doc(keyVendorPayment, "Vendor Payment", authz.ResourceVendorPayment, "vendor_payment", "vendor_payment")
	s.Amount = "t.vendor_payment_amount"
	s.Balance = "t.vendor_payment_unapplied_amount"
	s.OpenWhere = statusCodeExpr + " <> '" + codeVoid + "' AND " + s.Balance + " > 0"
	return s
}

// vendorCredit builds the vendor credit source: only an approved credit's
// unapplied remainder is usable.
func vendorCredit() Source {
	s := doc(keyVendorCredit, "Vendor Credit", authz.ResourceVendorCredit, "vendor_credit", "vendor_credit")
	s.Amount = "t.vendor_credit_grand_total"
	s.Balance = "t.vendor_credit_unapplied_amount"
	s.OpenWhere = statusCodeExpr + " = '" + codeApproved + "' AND " + s.Balance + " > 0"
	return s
}
