// Package authz implements StoneSuite's dynamic role-based access control:
// a stable permission CATALOG defined in Go (resource x action), tenant-scoped
// roles that bundle {resource, action, scope} grants, and an enforcer that
// resolves whether a caller may perform an action and at what scope.
//
// All RBAC data lives in the per-tenant database (roles, role_permissions,
// user_roles), so a caller's permissions are resolved against the tenant pool
// already attached to the request by the tenancy resolver middleware.
package authz

// Resource identifies a thing that actions are performed on. The catalog is a
// stable, code-defined list; super admins compose roles from it in the UI.
type Resource string

// Action identifies an operation performed on a resource.
type Action string

// Scope narrows how many rows an action may touch. Precedence: all > team > own.
type Scope string

const (
	ResourceWorkflow       Resource = "workflow"        // workflow definitions
	ResourceRecord         Resource = "record"          // generic workflow engine records
	ResourceLead           Resource = "lead"            // CRM leads
	ResourceProspect       Resource = "prospect"        // CRM prospects
	ResourceCustomer       Resource = "customer"        // CRM customers
	ResourceCRMActivity    Resource = "crm_activity"    // CRM activity log (calls/emails/meetings/notes/tasks)
	ResourceUser           Resource = "user"            // tenant users
	ResourceRole           Resource = "role"            // roles & permissions
	ResourceWorkflowConfig Resource = "workflow_config" // states/transitions/fields config
	ResourceSSOConfig      Resource = "sso_config"      // per-tenant SSO settings
	ResourceAudit          Resource = "audit"           // audit log

	// ResourceDashboardWidget covers which dashboard widgets each role's
	// members may see (role_dashboard_widgets) -- separate from ResourceRole
	// so a tenant can delegate dashboard curation without handing out full
	// role-editing rights.
	ResourceDashboardWidget Resource = "dashboard_widget"

	// Sales module resources
	ResourceInventoryItem Resource = "inventory_item"
	ResourceEstimate      Resource = "estimate"
	ResourceQuote         Resource = "quote"
	ResourceSalesOrder    Resource = "sales_order"
	ResourceInstallation  Resource = "installation"
	ResourceInvoice       Resource = "invoice"
	ResourcePayment       Resource = "payment"
	ResourceCreditMemo    Resource = "credit_memo"
	ResourceRefund        Resource = "refund"

	// Purchases module resources
	ResourceVendor        Resource = "vendor"
	ResourceRequisition   Resource = "requisition"
	ResourcePurchaseOrder Resource = "purchase_order"
	ResourceItemReceipt   Resource = "item_receipt"
	ResourceVendorBill    Resource = "vendor_bill"
	ResourceVendorPayment Resource = "vendor_payment"
	ResourceVendorCredit  Resource = "vendor_credit"
	ResourceExpense       Resource = "expense"

	// ResourcePortalAccess governs granting and withdrawing customer-portal
	// logins. Deliberately separate from ResourceCustomer: creating a portal
	// login mints an external credential into the workspace, which is a
	// security act, not a CRM edit. Keeping it apart lets a tenant give sales
	// staff customer:update without also letting them create outside logins.
	ResourcePortalAccess Resource = "portal_access"

	// Inventory module resources. inventory_item (above, under Sales) is the
	// catalogue; these cover the physical side of the warehouse.
	//
	// inventory_unit is deliberately separate from inventory_item so a yard or
	// warehouse clerk can be granted stock handling without also being granted
	// catalogue edit rights. Splitting them retires a permission that slab
	// routes used to sit under, so tenant/schema.sql carries a one-time backfill
	// granting inventory_unit:<action> to every role that already held
	// inventory_item:<action> — without it every custom role silently 403s.
	ResourceInventoryUnit   Resource = "inventory_unit"   // serialized units: slabs and remnants
	ResourceInventoryBin    Resource = "inventory_bin"    // bin/location master
	ResourceInventoryBundle Resource = "inventory_bundle" // bundles that move as a set
	ResourceWarehouse       Resource = "warehouse"        // lkp_warehouse master
	ResourceInventoryLookup Resource = "inventory_lookup" // material/colour/finish/reason vocabularies

	// Phase 3 stock documents. Each is a status document, so each carries
	// ActionTransition and ActionApprove on top of CRUD.
	//
	// They are separate resources rather than actions on inventory_unit because
	// the authority differs in kind: a yard clerk moves and receives stone all
	// day, but writing stock off or signing off a count variance is a
	// controller's job. One resource would make those inseparable.
	ResourceInventoryAdjustment Resource = "inventory_adjustment" // manual stock correction
	ResourceInventoryTransfer   Resource = "inventory_transfer"   // warehouse-to-warehouse movement
	ResourceInventoryCount      Resource = "inventory_count"      // cycle count / stock take

	// Finance
	ResourceChartOfAccount Resource = "chart_of_account"
	ResourceCashTransfer   Resource = "cash_transfer"

	// ResourceAccountingPeriod covers the fiscal calendar: generating a fiscal
	// year, and opening/closing the monthly periods that gate every GL write.
	//
	// create (generate next year's calendar) is deliberately separable from
	// update (close the books): the first is routine admin, the second is a
	// controller's signature. configure is the one-time base period setup.
	ResourceAccountingPeriod Resource = "accounting_period"

	// ResourceAny is the wildcard resource. Granting it matches every resource;
	// it is how the seeded super_admin role is expressed as a single row.
	ResourceAny Resource = "*"
)

const (
	ActionCreate     Action = "create"
	ActionRead       Action = "read"
	ActionUpdate     Action = "update"
	ActionDelete     Action = "delete"
	ActionTransition Action = "transition" // move a record between workflow states
	ActionApprove    Action = "approve"    // sign off on a record pending approval
	ActionConfigure  Action = "configure"  // edit definitions/settings

	// ActionAny is the wildcard action. Granting it matches every action.
	ActionAny Action = "*"
)

const (
	ScopeAll Scope = "all" // every row in the tenant
	ScopeOwn Scope = "own" // only rows the caller owns
)

// Permission is a single {resource, action} pair from the catalog.
type Permission struct {
	Resource Resource `json:"resource"`
	Action   Action   `json:"action"`
}

// catalog is the authoritative list of resource x action permissions a role
// may grant. Adding a capability is a one-line change here.
var catalog = []Permission{
	{ResourceWorkflow, ActionRead},

	{ResourceRecord, ActionCreate},
	{ResourceRecord, ActionRead},
	{ResourceRecord, ActionUpdate},
	{ResourceRecord, ActionDelete},
	{ResourceRecord, ActionTransition},
	{ResourceRecord, ActionApprove},

	{ResourceLead, ActionCreate},
	{ResourceLead, ActionRead},
	{ResourceLead, ActionUpdate},
	{ResourceLead, ActionDelete},
	{ResourceLead, ActionTransition},

	{ResourceProspect, ActionCreate},
	{ResourceProspect, ActionRead},
	{ResourceProspect, ActionUpdate},
	{ResourceProspect, ActionDelete},
	{ResourceProspect, ActionTransition},

	{ResourceCustomer, ActionCreate},
	{ResourceCustomer, ActionRead},
	{ResourceCustomer, ActionUpdate},
	{ResourceCustomer, ActionDelete},
	{ResourceCustomer, ActionTransition},

	{ResourceCRMActivity, ActionCreate},
	{ResourceCRMActivity, ActionRead},
	{ResourceCRMActivity, ActionUpdate},
	{ResourceCRMActivity, ActionDelete},

	{ResourceInventoryItem, ActionCreate},
	{ResourceInventoryItem, ActionRead},
	{ResourceInventoryItem, ActionUpdate},
	{ResourceInventoryItem, ActionDelete},

	// Inventory: physical stock. No ActionTransition on the four master-data
	// resources below — none is a status document with a workflow. The three
	// Phase 3 documents further down do carry transition and approve.
	{ResourceInventoryUnit, ActionCreate},
	{ResourceInventoryUnit, ActionRead},
	{ResourceInventoryUnit, ActionUpdate},
	{ResourceInventoryUnit, ActionDelete},

	{ResourceInventoryBin, ActionCreate},
	{ResourceInventoryBin, ActionRead},
	{ResourceInventoryBin, ActionUpdate},
	{ResourceInventoryBin, ActionDelete},

	{ResourceInventoryBundle, ActionCreate},
	{ResourceInventoryBundle, ActionRead},
	{ResourceInventoryBundle, ActionUpdate},
	{ResourceInventoryBundle, ActionDelete},

	{ResourceWarehouse, ActionCreate},
	{ResourceWarehouse, ActionRead},
	{ResourceWarehouse, ActionUpdate},
	{ResourceWarehouse, ActionDelete},

	// The three Phase 3 stock documents. ActionApprove gates the move into the
	// approved state and is deliberately distinct from ActionTransition: the
	// point of an approval step is that the person who raised a write-off is
	// not the person who signs it off, and one shared action would collapse
	// that into a single grant.
	{ResourceInventoryAdjustment, ActionCreate},
	{ResourceInventoryAdjustment, ActionRead},
	{ResourceInventoryAdjustment, ActionUpdate},
	{ResourceInventoryAdjustment, ActionDelete},
	{ResourceInventoryAdjustment, ActionTransition},
	{ResourceInventoryAdjustment, ActionApprove},

	{ResourceInventoryTransfer, ActionCreate},
	{ResourceInventoryTransfer, ActionRead},
	{ResourceInventoryTransfer, ActionUpdate},
	{ResourceInventoryTransfer, ActionDelete},
	{ResourceInventoryTransfer, ActionTransition},
	{ResourceInventoryTransfer, ActionApprove},

	{ResourceInventoryCount, ActionCreate},
	{ResourceInventoryCount, ActionRead},
	{ResourceInventoryCount, ActionUpdate},
	{ResourceInventoryCount, ActionDelete},
	{ResourceInventoryCount, ActionTransition},
	{ResourceInventoryCount, ActionApprove},

	// Read is separated from the write actions here on purpose: every inventory
	// form needs the vocabularies to populate its dropdowns, so a bin clerk
	// needs inventory_lookup:read without any right to edit the vocabulary.
	{ResourceInventoryLookup, ActionCreate},
	{ResourceInventoryLookup, ActionRead},
	{ResourceInventoryLookup, ActionUpdate},
	{ResourceInventoryLookup, ActionDelete},

	{ResourceEstimate, ActionCreate},
	{ResourceEstimate, ActionRead},
	{ResourceEstimate, ActionUpdate},
	{ResourceEstimate, ActionDelete},
	{ResourceEstimate, ActionTransition},

	{ResourceQuote, ActionCreate},
	{ResourceQuote, ActionRead},
	{ResourceQuote, ActionUpdate},
	{ResourceQuote, ActionDelete},
	{ResourceQuote, ActionTransition},

	{ResourceSalesOrder, ActionCreate},
	{ResourceSalesOrder, ActionRead},
	{ResourceSalesOrder, ActionUpdate},
	{ResourceSalesOrder, ActionDelete},
	{ResourceSalesOrder, ActionTransition},

	{ResourceInstallation, ActionCreate},
	{ResourceInstallation, ActionRead},
	{ResourceInstallation, ActionUpdate},
	{ResourceInstallation, ActionDelete},
	{ResourceInstallation, ActionTransition},

	{ResourceInvoice, ActionCreate},
	{ResourceInvoice, ActionRead},
	{ResourceInvoice, ActionUpdate},
	{ResourceInvoice, ActionDelete},
	{ResourceInvoice, ActionTransition},

	{ResourcePayment, ActionCreate},
	{ResourcePayment, ActionRead},
	{ResourcePayment, ActionUpdate},
	{ResourcePayment, ActionDelete},
	{ResourcePayment, ActionTransition},

	{ResourceCreditMemo, ActionCreate},
	{ResourceCreditMemo, ActionRead},
	{ResourceCreditMemo, ActionUpdate},
	{ResourceCreditMemo, ActionDelete},
	{ResourceCreditMemo, ActionTransition},
	// Approving a credit memo (DRFT->APPV) is what authorizes real credit
	// against AR, so it is a distinct capability from moving the record around:
	// a sales role can hold create/read/update without ever being able to
	// approve its own drafts. Every other CRDT transition uses ActionTransition.
	{ResourceCreditMemo, ActionApprove},

	{ResourceRefund, ActionCreate},
	{ResourceRefund, ActionRead},
	{ResourceRefund, ActionUpdate},
	{ResourceRefund, ActionDelete},
	{ResourceRefund, ActionTransition},
	// Approving a refund (PEND->APPV) is what authorizes it to draw down a
	// payment or credit memo, so it is a distinct capability from moving the
	// record around: a support role can hold create/read/update without ever
	// being able to approve its own initiated refunds. Every other transition
	// uses ActionTransition (mirrors ResourceCreditMemo above).
	{ResourceRefund, ActionApprove},

	{ResourceVendor, ActionCreate},
	{ResourceVendor, ActionRead},
	{ResourceVendor, ActionUpdate},
	{ResourceVendor, ActionDelete},
	{ResourceVendor, ActionTransition},

	{ResourceRequisition, ActionCreate},
	{ResourceRequisition, ActionRead},
	{ResourceRequisition, ActionUpdate},
	{ResourceRequisition, ActionDelete},
	{ResourceRequisition, ActionTransition},

	{ResourcePurchaseOrder, ActionCreate},
	{ResourcePurchaseOrder, ActionRead},
	{ResourcePurchaseOrder, ActionUpdate},
	{ResourcePurchaseOrder, ActionDelete},
	{ResourcePurchaseOrder, ActionTransition},

	{ResourceItemReceipt, ActionCreate},
	{ResourceItemReceipt, ActionRead},
	{ResourceItemReceipt, ActionUpdate},
	{ResourceItemReceipt, ActionDelete},
	{ResourceItemReceipt, ActionTransition},
	// Accepting a delivery beyond the over-receipt tolerance commits the
	// business to goods it never ordered and to paying for them, so it is a
	// distinct capability from posting a normal receipt: a warehouse role can
	// hold create/read/update/transition without ever being able to wave an
	// over-delivery through. Every other IRCT move uses ActionTransition
	// (mirrors ResourceCreditMemo and ResourceRefund above).
	{ResourceItemReceipt, ActionApprove},

	{ResourceVendorBill, ActionCreate},
	{ResourceVendorBill, ActionRead},
	{ResourceVendorBill, ActionUpdate},
	{ResourceVendorBill, ActionDelete},
	{ResourceVendorBill, ActionTransition},

	{ResourceVendorPayment, ActionCreate},
	{ResourceVendorPayment, ActionRead},
	{ResourceVendorPayment, ActionUpdate},
	{ResourceVendorPayment, ActionDelete},
	{ResourceVendorPayment, ActionTransition},

	{ResourceVendorCredit, ActionCreate},
	{ResourceVendorCredit, ActionRead},
	{ResourceVendorCredit, ActionUpdate},
	{ResourceVendorCredit, ActionDelete},
	{ResourceVendorCredit, ActionTransition},
	// Approving a vendor credit is what authorizes real credit against AP, so
	// it is a distinct capability from moving the record around: a purchasing
	// role can hold create/read/update without ever being able to approve its
	// own drafts. Every other transition uses ActionTransition (mirrors
	// ResourceCreditMemo, ResourceRefund, and ResourceItemReceipt above).
	{ResourceVendorCredit, ActionApprove},

	{ResourceExpense, ActionCreate},
	{ResourceExpense, ActionRead},
	{ResourceExpense, ActionUpdate},
	{ResourceExpense, ActionDelete},
	{ResourceExpense, ActionTransition},

	// Portal access has no `update`: a login is granted or withdrawn, never
	// edited. The customer it belongs to is fixed at creation — repointing an
	// existing credential at a different customer would be a silent data-access
	// change, so that path does not exist.
	{ResourcePortalAccess, ActionCreate},
	{ResourcePortalAccess, ActionRead},
	{ResourcePortalAccess, ActionDelete},

	{ResourceChartOfAccount, ActionCreate},
	{ResourceChartOfAccount, ActionRead},
	{ResourceChartOfAccount, ActionUpdate},
	{ResourceChartOfAccount, ActionDelete},
	{ResourceChartOfAccount, ActionConfigure},

	{ResourceCashTransfer, ActionCreate},
	{ResourceCashTransfer, ActionRead},
	{ResourceCashTransfer, ActionUpdate},
	{ResourceCashTransfer, ActionDelete},
	{ResourceCashTransfer, ActionTransition},

	// No ActionDelete: a period is never removed, only closed. Deleting one
	// would strand every journal entry that fell inside it.
	{ResourceAccountingPeriod, ActionRead},
	{ResourceAccountingPeriod, ActionCreate},
	{ResourceAccountingPeriod, ActionUpdate},
	{ResourceAccountingPeriod, ActionConfigure},

	{ResourceUser, ActionCreate},
	{ResourceUser, ActionRead},
	{ResourceUser, ActionUpdate},
	{ResourceUser, ActionDelete},

	{ResourceRole, ActionCreate},
	{ResourceRole, ActionRead},
	{ResourceRole, ActionUpdate},
	{ResourceRole, ActionDelete},

	{ResourceWorkflowConfig, ActionRead},
	{ResourceWorkflowConfig, ActionConfigure},

	{ResourceSSOConfig, ActionRead},
	{ResourceSSOConfig, ActionConfigure},

	{ResourceDashboardWidget, ActionRead},
	{ResourceDashboardWidget, ActionConfigure},

	{ResourceAudit, ActionRead},
}

// Catalog returns a copy of the permission catalog (safe for callers to mutate).
func Catalog() []Permission {
	out := make([]Permission, len(catalog))
	copy(out, catalog)
	return out
}

// validResources / validActions / validScopes are derived once for O(1) checks.
var (
	validResources = buildResourceSet()
	validActions   = buildActionSet()
	validScopes    = map[Scope]bool{ScopeAll: true, ScopeOwn: true}
)

func buildResourceSet() map[Resource]bool {
	m := map[Resource]bool{}
	for _, p := range catalog {
		m[p.Resource] = true
	}
	return m
}

func buildActionSet() map[Action]bool {
	m := map[Action]bool{}
	for _, p := range catalog {
		m[p.Action] = true
	}
	return m
}

// IsValidPermission reports whether {resource, action} exists in the catalog.
// Wildcards are intentionally rejected here: callers (the role editor) may only
// grant concrete catalog permissions; wildcards are reserved for system seeding.
func IsValidPermission(r Resource, a Action) bool {
	return validResources[r] && validActions[a] && permissionInCatalog(r, a)
}

func permissionInCatalog(r Resource, a Action) bool {
	for _, p := range catalog {
		if p.Resource == r && p.Action == a {
			return true
		}
	}
	return false
}

// IsValidScope reports whether s is one of all|team|own.
func IsValidScope(s Scope) bool { return validScopes[s] }
