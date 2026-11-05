// Package approvalinbox lists the records waiting for a caller's sign-off across
// every approvalchain module, and counts them with the exact same predicate so
// the dashboard "Needs Approval" tile always equals the inbox total.
//
// It imports approvalchain (registry: tables, columns, gates), authz (per-module
// RBAC) and workflow (Querier) only. Table and column identifiers come from the
// registry and the metadata map below, never from a request; every value is a
// $n parameter.
package approvalinbox

import (
	"regexp"
	"sort"

	"stonesuite-backend/approvalchain"
	"stonesuite-backend/authz"
)

// Meta is the inbox-only description of one approvalchain module. Registry keys
// without an entry here fail TestMetaCoversRegistry.
type Meta struct {
	Label    string
	Resource authz.Resource
	// Domain and RouteModule are the frontend route segments: /<Domain>/<RouteModule>/<uuid>.
	Domain      string
	RouteModule string
	// AmountColumn and PartyNameColumn are optional; the item omits the field when blank.
	AmountColumn    string
	PartyNameColumn string
}

const (
	domainSales     = "sales"
	domainPurchases = "purchases"
	domainInventory = "inventory"
)

var metas = map[string]Meta{
	"estimate":     {Label: "Estimate", Resource: authz.ResourceEstimate, Domain: domainSales, RouteModule: "estimate", AmountColumn: "estimate_grand_total", PartyNameColumn: "estimate_bill_customer_name"},
	"quote":        {Label: "Quote", Resource: authz.ResourceQuote, Domain: domainSales, RouteModule: "quote", AmountColumn: "quote_grand_total", PartyNameColumn: "quote_bill_customer_name"},
	"sales_order":  {Label: "Sales Order", Resource: authz.ResourceSalesOrder, Domain: domainSales, RouteModule: "sales_order", AmountColumn: "sales_order_grand_total", PartyNameColumn: "sales_order_bill_customer_name"},
	"installation": {Label: "Fabrication Job", Resource: authz.ResourceInstallation, Domain: domainSales, RouteModule: "installation", PartyNameColumn: "job_site_customer_name"},
	"invoice":      {Label: "Invoice", Resource: authz.ResourceInvoice, Domain: domainSales, RouteModule: "invoice", AmountColumn: "invoice_grand_total", PartyNameColumn: "invoice_bill_customer_name"},
	"payment":      {Label: "Payment", Resource: authz.ResourcePayment, Domain: domainSales, RouteModule: "payment", AmountColumn: "payment_amount"},
	"credit_memo":  {Label: "Credit Memo", Resource: authz.ResourceCreditMemo, Domain: domainSales, RouteModule: "credit_memo", AmountColumn: "credit_memo_grand_total", PartyNameColumn: "credit_memo_bill_customer_name"},
	"refund":       {Label: "Refund", Resource: authz.ResourceRefund, Domain: domainSales, RouteModule: "refund", AmountColumn: "refund_amount"},

	"purchase_order": {Label: "Purchase Order", Resource: authz.ResourcePurchaseOrder, Domain: domainPurchases, RouteModule: "purchase_order", AmountColumn: "purchase_order_grand_total", PartyNameColumn: "purchase_order_vendor_name"},
	"requisition":    {Label: "Requisition", Resource: authz.ResourceRequisition, Domain: domainPurchases, RouteModule: "requisition", AmountColumn: "requisition_estimated_total", PartyNameColumn: "requisition_vendor_name"},
	"vendor_bill":    {Label: "Vendor Bill", Resource: authz.ResourceVendorBill, Domain: domainPurchases, RouteModule: "vendor_bill", AmountColumn: "vendor_bill_grand_total", PartyNameColumn: "vendor_bill_vendor_name"},
	"vendor_payment": {Label: "Vendor Payment", Resource: authz.ResourceVendorPayment, Domain: domainPurchases, RouteModule: "vendor_payment", AmountColumn: "vendor_payment_amount", PartyNameColumn: "vendor_payment_vendor_name"},
	"vendor_credit":  {Label: "Vendor Credit", Resource: authz.ResourceVendorCredit, Domain: domainPurchases, RouteModule: "vendor_credit", AmountColumn: "vendor_credit_grand_total", PartyNameColumn: "vendor_credit_vendor_name"},
	"expense":        {Label: "Expense Claim", Resource: authz.ResourceExpense, Domain: domainPurchases, RouteModule: "expense", AmountColumn: "expense_total"},

	"bin_transfer": {Label: "Bin Transfer", Resource: authz.ResourceBinTransfer, Domain: domainInventory, RouteModule: "bin_transfer"},
}

// MetaFor returns the inbox metadata for a workflow key.
func MetaFor(key string) (Meta, bool) {
	m, ok := metas[key]
	return m, ok
}

// Keys returns every inbox module key, sorted for deterministic SQL and output.
func Keys() []string {
	out := approvalchain.Keys()
	sort.Strings(out)
	return out
}

// identPattern is the shape every registry/metadata identifier interpolated into
// SQL must match (same rule as automation.approvalSpec.ident).
var identPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*$`)

// identsOK reports the first interpolated identifier that is not a plain SQL
// identifier. Optional columns may be blank.
func identsOK(key string, cfg approvalchain.ModuleConfig, m Meta) (string, bool) {
	r := cfg.Record
	for _, f := range []struct {
		name, v  string
		optional bool
	}{
		{"approver table", cfg.ApproverTable, false}, {"approval table", cfg.ApprovalTable, false},
		{"table", r.Table, false}, {"history table", r.HistoryTable, false}, {"id column", r.IDColumn, false},
		{"uuid column", r.UUIDColumn, false}, {"status column", r.StatusColumn, false},
		{"approval status column", r.ApprovalStatusColumn, false}, {"deleted column", r.DeletedAtColumn, false},
		{"created column", r.CreatedAtColumn, false}, {"owner column", r.OwnerColumn, true},
		{"number column", r.NumberColumn, true}, {"amount column", m.AmountColumn, true},
		{"party column", m.PartyNameColumn, true},
	} {
		if f.optional && f.v == "" {
			continue
		}
		if !identPattern.MatchString(f.v) {
			return key + ": invalid " + f.name + " " + f.v, false
		}
	}
	return "", true
}
