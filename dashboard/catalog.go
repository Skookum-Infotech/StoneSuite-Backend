// Package dashboard implements the Dashboard Widgets module: a fixed,
// code-defined catalog of dashboard widgets (mirrors authz.Catalog and the
// frontend's sidebarNav.ts), filtered per caller by RBAC grant and tenant
// configuration, with per-user visibility/layout preferences layered on top.
//
// The catalog carries no I/O -- every widget declares the existing
// authz.Resource/Action it rides on, so a widget can never show more than the
// caller's role already grants elsewhere in the app. See
// docs/superpowers/specs/2026-08-06-dashboard-widgets-design.md for the full
// design rationale.
package dashboard

import "stonesuite-backend/authz"

// WidgetType describes how a widget renders.
type WidgetType string

const (
	TypeMetric WidgetType = "metric"
	TypeList   WidgetType = "list"
	TypeChart  WidgetType = "chart"
)

// Category groups widgets for display, mirroring the sidebar's domain groups.
type Category string

const (
	CategoryCRM       Category = "crm"
	CategorySales     Category = "sales"
	CategoryPurchases Category = "purchases"
	CategoryInventory Category = "inventory"
	CategoryFinance   Category = "finance"
	CategoryAdmin     Category = "admin"
)

// Layout bounds for DefaultWidth/DefaultHeight and any user-saved
// width/height -- a conventional 12-column, 8-row grid (design spec AD-6).
const (
	MinSize   = 1
	MaxWidth  = 12
	MaxHeight = 8
)

// Widget is one catalog entry: the full universe of dashboard widgets
// StoneSuite ships, defined in code so adding one is a one-line append here,
// no migration required (AD-1).
type Widget struct {
	Key             string // stable id, e.g. "sales.quotes"
	Title           string
	Description     string
	Category        Category
	Type            WidgetType
	Resource        authz.Resource // the existing catalog permission this widget rides on (AD-2)
	Action          authz.Action
	DataEndpoint    string // existing module route the frontend fetches for this widget's data
	DefaultVisible  bool
	DefaultPosition int
	DefaultWidth    int
	DefaultHeight   int
}

// catalog is the authoritative widget list. Every {Resource, Action} pair
// here must already exist in authz.Catalog() -- catalog_test.go guards this.
var catalog = []Widget{
	{Key: "crm.leads", Title: "Leads", Description: "Open leads in the pipeline.",
		Category: CategoryCRM, Type: TypeList, Resource: authz.ResourceLead, Action: authz.ActionRead,
		DataEndpoint:   "/api/tenant/crm/lead/records",
		DefaultVisible: true, DefaultPosition: 0, DefaultWidth: 6, DefaultHeight: 4},
	{Key: "crm.prospects", Title: "Prospects", Description: "Active prospects.",
		Category: CategoryCRM, Type: TypeList, Resource: authz.ResourceProspect, Action: authz.ActionRead,
		DataEndpoint:   "/api/tenant/crm/prospect/records",
		DefaultVisible: true, DefaultPosition: 1, DefaultWidth: 6, DefaultHeight: 4},
	{Key: "crm.customers", Title: "Customers", Description: "Customer book.",
		Category: CategoryCRM, Type: TypeList, Resource: authz.ResourceCustomer, Action: authz.ActionRead,
		DataEndpoint:   "/api/tenant/crm/customer/records",
		DefaultVisible: true, DefaultPosition: 2, DefaultWidth: 6, DefaultHeight: 4},

	{Key: "sales.estimates", Title: "Estimates", Description: "Draft and sent estimates.",
		Category: CategorySales, Type: TypeList, Resource: authz.ResourceEstimate, Action: authz.ActionRead,
		DataEndpoint:   "/api/tenant/estimates",
		DefaultVisible: true, DefaultPosition: 3, DefaultWidth: 6, DefaultHeight: 4},
	{Key: "sales.quotes", Title: "Quotes", Description: "Open quotes.",
		Category: CategorySales, Type: TypeList, Resource: authz.ResourceQuote, Action: authz.ActionRead,
		DataEndpoint:   "/api/tenant/quotes",
		DefaultVisible: true, DefaultPosition: 4, DefaultWidth: 6, DefaultHeight: 4},
	{Key: "sales.salesOrders", Title: "Sales Orders", Description: "Open sales orders.",
		Category: CategorySales, Type: TypeList, Resource: authz.ResourceSalesOrder, Action: authz.ActionRead,
		DataEndpoint:   "/api/tenant/sales-orders",
		DefaultVisible: true, DefaultPosition: 5, DefaultWidth: 6, DefaultHeight: 4},
	{Key: "sales.invoices", Title: "Invoices", Description: "Outstanding invoices.",
		Category: CategorySales, Type: TypeList, Resource: authz.ResourceInvoice, Action: authz.ActionRead,
		DataEndpoint:   "/api/tenant/invoices",
		DefaultVisible: true, DefaultPosition: 6, DefaultWidth: 6, DefaultHeight: 4},
	{Key: "sales.payments", Title: "Payments", Description: "Recent payments received.",
		Category: CategorySales, Type: TypeList, Resource: authz.ResourcePayment, Action: authz.ActionRead,
		DataEndpoint:   "/api/tenant/payments",
		DefaultVisible: false, DefaultPosition: 7, DefaultWidth: 6, DefaultHeight: 4},
	{Key: "sales.creditMemos", Title: "Credit Memos", Description: "Open credit memos.",
		Category: CategorySales, Type: TypeList, Resource: authz.ResourceCreditMemo, Action: authz.ActionRead,
		DataEndpoint:   "/api/tenant/credit-memos",
		DefaultVisible: false, DefaultPosition: 8, DefaultWidth: 6, DefaultHeight: 4},
	{Key: "sales.refunds", Title: "Refunds", Description: "Pending refunds.",
		Category: CategorySales, Type: TypeList, Resource: authz.ResourceRefund, Action: authz.ActionRead,
		DataEndpoint:   "/api/tenant/refunds",
		DefaultVisible: false, DefaultPosition: 9, DefaultWidth: 6, DefaultHeight: 4},

	{Key: "purchases.vendors", Title: "Vendors", Description: "Vendor directory.",
		Category: CategoryPurchases, Type: TypeList, Resource: authz.ResourceVendor, Action: authz.ActionRead,
		DataEndpoint:   "/api/tenant/vendors",
		DefaultVisible: false, DefaultPosition: 10, DefaultWidth: 6, DefaultHeight: 4},
	{Key: "purchases.purchaseOrders", Title: "Purchase Orders", Description: "Open purchase orders.",
		Category: CategoryPurchases, Type: TypeList, Resource: authz.ResourcePurchaseOrder, Action: authz.ActionRead,
		DataEndpoint:   "/api/tenant/purchase-orders",
		DefaultVisible: false, DefaultPosition: 11, DefaultWidth: 6, DefaultHeight: 4},
	{Key: "purchases.itemReceipts", Title: "Item Receipts", Description: "Recent item receipts.",
		Category: CategoryPurchases, Type: TypeList, Resource: authz.ResourceItemReceipt, Action: authz.ActionRead,
		DataEndpoint:   "/api/tenant/item-receipts",
		DefaultVisible: false, DefaultPosition: 12, DefaultWidth: 6, DefaultHeight: 4},

	{Key: "inventory.items", Title: "Inventory Items", Description: "Item catalogue.",
		Category: CategoryInventory, Type: TypeList, Resource: authz.ResourceInventoryItem, Action: authz.ActionRead,
		DataEndpoint:   "/api/tenant/inventory/items",
		DefaultVisible: false, DefaultPosition: 13, DefaultWidth: 6, DefaultHeight: 4},
	{Key: "inventory.units", Title: "Units / Slabs", Description: "Serialized stock units.",
		Category: CategoryInventory, Type: TypeList, Resource: authz.ResourceInventoryUnit, Action: authz.ActionRead,
		DataEndpoint:   "/api/tenant/inventory/units",
		DefaultVisible: false, DefaultPosition: 14, DefaultWidth: 6, DefaultHeight: 4},
	{Key: "inventory.counts", Title: "Inventory Counts", Description: "Open cycle counts.",
		Category: CategoryInventory, Type: TypeList, Resource: authz.ResourceInventoryCount, Action: authz.ActionRead,
		DataEndpoint:   "/api/tenant/inventory/counts",
		DefaultVisible: false, DefaultPosition: 15, DefaultWidth: 6, DefaultHeight: 4},

	{Key: "finance.chartOfAccounts", Title: "Chart of Accounts", Description: "GL account tree.",
		Category: CategoryFinance, Type: TypeList, Resource: authz.ResourceChartOfAccount, Action: authz.ActionRead,
		DataEndpoint:   "/api/tenant/finance/accounts",
		DefaultVisible: false, DefaultPosition: 16, DefaultWidth: 6, DefaultHeight: 4},
	{Key: "finance.cashTransfers", Title: "Cash Transfers", Description: "Recent cash transfers.",
		Category: CategoryFinance, Type: TypeList, Resource: authz.ResourceCashTransfer, Action: authz.ActionRead,
		DataEndpoint:   "/api/tenant/finance/cash-transfers",
		DefaultVisible: false, DefaultPosition: 17, DefaultWidth: 6, DefaultHeight: 4},
	{Key: "finance.accountingPeriods", Title: "Accounting Periods", Description: "Fiscal calendar status.",
		Category: CategoryFinance, Type: TypeList, Resource: authz.ResourceAccountingPeriod, Action: authz.ActionRead,
		DataEndpoint:   "/api/tenant/finance/accounting-periods",
		DefaultVisible: false, DefaultPosition: 18, DefaultWidth: 6, DefaultHeight: 4},

	{Key: "records.pendingApprovals", Title: "Pending Approvals", Description: "Records waiting on your approval.",
		Category: CategoryAdmin, Type: TypeList, Resource: authz.ResourceRecord, Action: authz.ActionApprove,
		DataEndpoint:   "/api/tenant/records/approvals/pending",
		DefaultVisible: true, DefaultPosition: 19, DefaultWidth: 6, DefaultHeight: 4},
	{Key: "admin.auditLog", Title: "Audit Log", Description: "Recent security and audit events.",
		Category: CategoryAdmin, Type: TypeList, Resource: authz.ResourceAudit, Action: authz.ActionRead,
		DataEndpoint:   "/api/tenant/audit",
		DefaultVisible: false, DefaultPosition: 20, DefaultWidth: 6, DefaultHeight: 4},
}

// Catalog returns a copy of the widget catalog (safe for callers to mutate).
func Catalog() []Widget {
	out := make([]Widget, len(catalog))
	copy(out, catalog)
	return out
}

// ByKey returns the catalog widget with the given key, or ok=false.
func ByKey(key string) (Widget, bool) {
	for _, w := range catalog {
		if w.Key == key {
			return w, true
		}
	}
	return Widget{}, false
}
