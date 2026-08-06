# Dashboard Widgets Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the Dashboard Widgets backend: a code-defined widget catalog filtered per caller by RBAC grant and tenant config, with per-user visibility/layout preferences and a tenant-admin on/off switch.

**Architecture:** New `dashboard/` package (catalog + pure resolve/validate logic + DB store), following the exact shape of `crmactivity/` and `authz/catalog.go`. `controllers/dashboard.go` wires it into the standard `tenantChain` (`RequireAuth` → per-tenant rate limit → `TenantResolver`). Two new tenant tables. No new `authz.Resource` — every widget reuses an existing catalog `{Resource, Action}`.

**Tech Stack:** Go, pgx/v5 (pgxpool), stdlib `net/http` + `ServeMux` method-prefixed routing, testify (table-driven tests).

**Spec:** `docs/superpowers/specs/2026-08-06-dashboard-widgets-design.md` — read this first for the full rationale (AD-1..AD-8). This plan implements it task-by-task; where this plan and the spec ever seem to disagree, the spec's decision wins and the plan has a bug.

## Global Constraints

- Every `/api/tenant/` route runs behind `tenantChain` (`RequireAuth` → per-tenant rate limit → `TenantResolver`) — never register one without it.
- No new `authz.Resource`/`Action` — every widget's gate reuses an existing catalog entry (spec AD-2). The two admin config routes reuse `authz.ResourceWorkflowConfig` / `authz.ActionConfigure`.
- The widget catalog is Go code only (`dashboard/catalog.go`) — never a DB-seeded table (spec AD-1).
- All DDL is idempotent: `CREATE TABLE IF NOT EXISTS`; no down-migrations; nothing destructive.
- A widget the caller can't see (grant denied or tenant-disabled) is omitted from the response entirely — never returned with a "denied" flag.
- Every file stays under 300 lines.
- Errors are always wrapped: `fmt.Errorf("context: %w", err)`.
- Every response is `{success: bool, message?: string, ...}` via `writeJSON`/`fail` (`controllers/tenant.go`).
- Table-driven tests (`testify/assert`, `testify/require`) for every pure function.
- `go build ./... && go vet ./... && go test ./...` must pass at the end of every task.

---

### Task 1: Schema — `dashboard_widget_config` and `dashboard_user_widget`

**Files:**
- Modify: `database/migrations/tenant/schema.sql` (append at end of file, after the final `accounting_period` backfill `UPDATE` — line 6803)

**Interfaces:**
- Produces: two tables later tasks' `dashboard/store.go` reads/writes directly by name (no Go-side schema constants).

- [ ] **Step 1: Append the two tables to `tenant/schema.sql`**

Open `database/migrations/tenant/schema.sql`, go to the very end of the file (the last line is the `accounting_period` backfill `UPDATE` ending in `AND gl_lock_status = 'open';`), and append:

```sql


-- dashboard_widget_config -- tenant-wide widget on/off override.
-- Override-only: a widget with no row here is enabled. widget_key is not a
-- real FK -- the catalog is Go code (dashboard/catalog.go), not a table,
-- same non-FK pattern as role_permissions.resource.
CREATE TABLE IF NOT EXISTS dashboard_widget_config (
    widget_key  VARCHAR(64) PRIMARY KEY,
    enabled     BOOLEAN     NOT NULL DEFAULT TRUE,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- dashboard_user_widget -- one caller's visibility/layout for one widget.
-- position/width/height describe a conventional 12-column, 8-row grid; see
-- dashboard.MinSize/MaxWidth/MaxHeight (dashboard/catalog.go).
CREATE TABLE IF NOT EXISTS dashboard_user_widget (
    user_id     UUID        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    widget_key  VARCHAR(64) NOT NULL,
    visible     BOOLEAN     NOT NULL DEFAULT TRUE,
    position    INT         NOT NULL DEFAULT 0,
    width       INT         NOT NULL DEFAULT 4,
    height      INT         NOT NULL DEFAULT 2,
    updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, widget_key),
    CONSTRAINT chk_dashboard_user_widget_position CHECK (position >= 0),
    CONSTRAINT chk_dashboard_user_widget_size CHECK (width BETWEEN 1 AND 12 AND height BETWEEN 1 AND 8)
);
CREATE INDEX IF NOT EXISTS idx_dashboard_user_widget_user ON dashboard_user_widget(user_id);
```

- [ ] **Step 2: Verify the module still builds (the schema is embedded via `go:embed`)**

Run: `go build ./...`
Expected: no output, exit code 0. (This does not connect to a database — it only proves `database/tenant_migrations.go`'s `//go:embed migrations/tenant/schema.sql` still compiles with the file present and non-empty. Both new tables are brand-new `CREATE TABLE IF NOT EXISTS` bodies — the `CHECK` constraints are inline in that same body, so they're safe on both a fresh tenant and a re-apply, no `DO $$` guard needed per the `add-migration` skill's rules.)

- [ ] **Step 3: Commit**

```bash
git add database/migrations/tenant/schema.sql
git commit -m "$(cat <<'EOF'
feat(dashboard): add widget config and user preference tables

dashboard_widget_config is an override-only tenant on/off switch (a
missing row means enabled). dashboard_user_widget is one row per
(user, widget) holding visible/position/width/height, mirroring the
user_roles composite-PK pattern.
EOF
)"
```

---

### Task 2: Widget catalog — `dashboard/catalog.go`

**Files:**
- Create: `dashboard/catalog.go`
- Test: `dashboard/catalog_test.go`

**Interfaces:**
- Consumes: `authz.Resource`, `authz.Action`, `authz.IsValidPermission(r, a) bool` (`authz/catalog.go`).
- Produces: `type WidgetType string`, `type Category string`, consts `MinSize, MaxWidth, MaxHeight = 1, 12, 8`, `type Widget struct{Key, Title, Description string; Category Category; Type WidgetType; Resource authz.Resource; Action authz.Action; DataEndpoint string; DefaultVisible bool; DefaultPosition, DefaultWidth, DefaultHeight int}`, `func Catalog() []Widget`, `func ByKey(key string) (Widget, bool)` — every later task in this plan depends on these exact names.

- [ ] **Step 1: Write the failing test**

Create `dashboard/catalog_test.go`:

```go
package dashboard

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/authz"
)

// TestCatalogKeysAreUnique guards against a copy-paste duplicate Key silently
// shadowing another widget in ByKey lookups.
func TestCatalogKeysAreUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, w := range Catalog() {
		require.False(t, seen[w.Key], "duplicate widget key %q", w.Key)
		seen[w.Key] = true
	}
}

// TestCatalogPermissionsAreGrantable asserts every widget's {Resource, Action}
// exists in the authz catalog. A widget riding on a permission no role can
// ever hold is a widget nobody can ever see -- the same drift class
// controllers/rbac_catalog_drift_test.go guards for CRM resources.
func TestCatalogPermissionsAreGrantable(t *testing.T) {
	for _, w := range Catalog() {
		assert.Truef(t, authz.IsValidPermission(w.Resource, w.Action),
			"widget %q rides on {%s, %s} which is missing from the authz catalog (ungrantable widget). Add it to authz/catalog.go.",
			w.Key, w.Resource, w.Action)
	}
}

// TestCatalogDefaultsAreInBounds guards the grid bounds (MinSize..MaxWidth,
// MinSize..MaxHeight) so a catalog entry can't ship a default ValidatePrefs
// (Task 4) would itself reject.
func TestCatalogDefaultsAreInBounds(t *testing.T) {
	for _, w := range Catalog() {
		assert.GreaterOrEqualf(t, w.DefaultPosition, 0, "widget %q has a negative DefaultPosition", w.Key)
		assert.Truef(t, w.DefaultWidth >= MinSize && w.DefaultWidth <= MaxWidth,
			"widget %q DefaultWidth %d out of [%d,%d]", w.Key, w.DefaultWidth, MinSize, MaxWidth)
		assert.Truef(t, w.DefaultHeight >= MinSize && w.DefaultHeight <= MaxHeight,
			"widget %q DefaultHeight %d out of [%d,%d]", w.Key, w.DefaultHeight, MinSize, MaxHeight)
	}
}

// TestByKey covers both the hit and miss paths.
func TestByKey(t *testing.T) {
	w, ok := ByKey("sales.quotes")
	require.True(t, ok)
	assert.Equal(t, "Quotes", w.Title)

	_, ok = ByKey("does.not.exist")
	assert.False(t, ok)
}

// TestCatalogReturnsACopy proves mutating the returned slice cannot corrupt
// the package-level catalog for the next caller (mirrors authz.Catalog's
// contract).
func TestCatalogReturnsACopy(t *testing.T) {
	first := Catalog()
	require.NotEmpty(t, first)
	first[0].Title = "corrupted"
	second := Catalog()
	assert.NotEqual(t, "corrupted", second[0].Title)
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./dashboard/... -run TestCatalog -v`
Expected: FAIL to build — `undefined: Catalog` (package `dashboard` has no source files yet).

- [ ] **Step 3: Write the catalog**

Create `dashboard/catalog.go`:

```go
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
		DataEndpoint: "/api/tenant/crm/lead/records",
		DefaultVisible: true, DefaultPosition: 0, DefaultWidth: 6, DefaultHeight: 4},
	{Key: "crm.prospects", Title: "Prospects", Description: "Active prospects.",
		Category: CategoryCRM, Type: TypeList, Resource: authz.ResourceProspect, Action: authz.ActionRead,
		DataEndpoint: "/api/tenant/crm/prospect/records",
		DefaultVisible: true, DefaultPosition: 1, DefaultWidth: 6, DefaultHeight: 4},
	{Key: "crm.customers", Title: "Customers", Description: "Customer book.",
		Category: CategoryCRM, Type: TypeList, Resource: authz.ResourceCustomer, Action: authz.ActionRead,
		DataEndpoint: "/api/tenant/crm/customer/records",
		DefaultVisible: true, DefaultPosition: 2, DefaultWidth: 6, DefaultHeight: 4},

	{Key: "sales.estimates", Title: "Estimates", Description: "Draft and sent estimates.",
		Category: CategorySales, Type: TypeList, Resource: authz.ResourceEstimate, Action: authz.ActionRead,
		DataEndpoint: "/api/tenant/estimates",
		DefaultVisible: true, DefaultPosition: 3, DefaultWidth: 6, DefaultHeight: 4},
	{Key: "sales.quotes", Title: "Quotes", Description: "Open quotes.",
		Category: CategorySales, Type: TypeList, Resource: authz.ResourceQuote, Action: authz.ActionRead,
		DataEndpoint: "/api/tenant/quotes",
		DefaultVisible: true, DefaultPosition: 4, DefaultWidth: 6, DefaultHeight: 4},
	{Key: "sales.salesOrders", Title: "Sales Orders", Description: "Open sales orders.",
		Category: CategorySales, Type: TypeList, Resource: authz.ResourceSalesOrder, Action: authz.ActionRead,
		DataEndpoint: "/api/tenant/sales-orders",
		DefaultVisible: true, DefaultPosition: 5, DefaultWidth: 6, DefaultHeight: 4},
	{Key: "sales.invoices", Title: "Invoices", Description: "Outstanding invoices.",
		Category: CategorySales, Type: TypeList, Resource: authz.ResourceInvoice, Action: authz.ActionRead,
		DataEndpoint: "/api/tenant/invoices",
		DefaultVisible: true, DefaultPosition: 6, DefaultWidth: 6, DefaultHeight: 4},
	{Key: "sales.payments", Title: "Payments", Description: "Recent payments received.",
		Category: CategorySales, Type: TypeList, Resource: authz.ResourcePayment, Action: authz.ActionRead,
		DataEndpoint: "/api/tenant/payments",
		DefaultVisible: false, DefaultPosition: 7, DefaultWidth: 6, DefaultHeight: 4},
	{Key: "sales.creditMemos", Title: "Credit Memos", Description: "Open credit memos.",
		Category: CategorySales, Type: TypeList, Resource: authz.ResourceCreditMemo, Action: authz.ActionRead,
		DataEndpoint: "/api/tenant/credit-memos",
		DefaultVisible: false, DefaultPosition: 8, DefaultWidth: 6, DefaultHeight: 4},
	{Key: "sales.refunds", Title: "Refunds", Description: "Pending refunds.",
		Category: CategorySales, Type: TypeList, Resource: authz.ResourceRefund, Action: authz.ActionRead,
		DataEndpoint: "/api/tenant/refunds",
		DefaultVisible: false, DefaultPosition: 9, DefaultWidth: 6, DefaultHeight: 4},

	{Key: "purchases.vendors", Title: "Vendors", Description: "Vendor directory.",
		Category: CategoryPurchases, Type: TypeList, Resource: authz.ResourceVendor, Action: authz.ActionRead,
		DataEndpoint: "/api/tenant/vendors",
		DefaultVisible: false, DefaultPosition: 10, DefaultWidth: 6, DefaultHeight: 4},
	{Key: "purchases.purchaseOrders", Title: "Purchase Orders", Description: "Open purchase orders.",
		Category: CategoryPurchases, Type: TypeList, Resource: authz.ResourcePurchaseOrder, Action: authz.ActionRead,
		DataEndpoint: "/api/tenant/purchase-orders",
		DefaultVisible: false, DefaultPosition: 11, DefaultWidth: 6, DefaultHeight: 4},
	{Key: "purchases.itemReceipts", Title: "Item Receipts", Description: "Recent item receipts.",
		Category: CategoryPurchases, Type: TypeList, Resource: authz.ResourceItemReceipt, Action: authz.ActionRead,
		DataEndpoint: "/api/tenant/item-receipts",
		DefaultVisible: false, DefaultPosition: 12, DefaultWidth: 6, DefaultHeight: 4},

	{Key: "inventory.items", Title: "Inventory Items", Description: "Item catalogue.",
		Category: CategoryInventory, Type: TypeList, Resource: authz.ResourceInventoryItem, Action: authz.ActionRead,
		DataEndpoint: "/api/tenant/inventory/items",
		DefaultVisible: false, DefaultPosition: 13, DefaultWidth: 6, DefaultHeight: 4},
	{Key: "inventory.units", Title: "Units / Slabs", Description: "Serialized stock units.",
		Category: CategoryInventory, Type: TypeList, Resource: authz.ResourceInventoryUnit, Action: authz.ActionRead,
		DataEndpoint: "/api/tenant/inventory/units",
		DefaultVisible: false, DefaultPosition: 14, DefaultWidth: 6, DefaultHeight: 4},
	{Key: "inventory.counts", Title: "Inventory Counts", Description: "Open cycle counts.",
		Category: CategoryInventory, Type: TypeList, Resource: authz.ResourceInventoryCount, Action: authz.ActionRead,
		DataEndpoint: "/api/tenant/inventory/counts",
		DefaultVisible: false, DefaultPosition: 15, DefaultWidth: 6, DefaultHeight: 4},

	{Key: "finance.chartOfAccounts", Title: "Chart of Accounts", Description: "GL account tree.",
		Category: CategoryFinance, Type: TypeList, Resource: authz.ResourceChartOfAccount, Action: authz.ActionRead,
		DataEndpoint: "/api/tenant/finance/accounts",
		DefaultVisible: false, DefaultPosition: 16, DefaultWidth: 6, DefaultHeight: 4},
	{Key: "finance.cashTransfers", Title: "Cash Transfers", Description: "Recent cash transfers.",
		Category: CategoryFinance, Type: TypeList, Resource: authz.ResourceCashTransfer, Action: authz.ActionRead,
		DataEndpoint: "/api/tenant/finance/cash-transfers",
		DefaultVisible: false, DefaultPosition: 17, DefaultWidth: 6, DefaultHeight: 4},
	{Key: "finance.accountingPeriods", Title: "Accounting Periods", Description: "Fiscal calendar status.",
		Category: CategoryFinance, Type: TypeList, Resource: authz.ResourceAccountingPeriod, Action: authz.ActionRead,
		DataEndpoint: "/api/tenant/finance/accounting-periods",
		DefaultVisible: false, DefaultPosition: 18, DefaultWidth: 6, DefaultHeight: 4},

	{Key: "records.pendingApprovals", Title: "Pending Approvals", Description: "Records waiting on your approval.",
		Category: CategoryAdmin, Type: TypeList, Resource: authz.ResourceRecord, Action: authz.ActionApprove,
		DataEndpoint: "/api/tenant/records/approvals/pending",
		DefaultVisible: true, DefaultPosition: 19, DefaultWidth: 6, DefaultHeight: 4},
	{Key: "admin.auditLog", Title: "Audit Log", Description: "Recent security and audit events.",
		Category: CategoryAdmin, Type: TypeList, Resource: authz.ResourceAudit, Action: authz.ActionRead,
		DataEndpoint: "/api/tenant/audit",
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
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./dashboard/... -v`
Expected: all `TestCatalog*` and `TestByKey` PASS.

- [ ] **Step 5: Commit**

```bash
git add dashboard/catalog.go dashboard/catalog_test.go
git commit -m "$(cat <<'EOF'
feat(dashboard): add widget catalog

Code-defined catalog of ~20 dashboard widgets, each riding on an
existing authz {resource,action} -- no new permission is introduced.
Mirrors authz/catalog.go and the frontend's sidebarNav.ts.
EOF
)"
```

---

### Task 3: Types + pure `Resolve` — `dashboard/types.go`, `dashboard/resolve.go`

**Files:**
- Create: `dashboard/types.go`
- Create: `dashboard/resolve.go`
- Test: `dashboard/resolve_test.go`

**Interfaces:**
- Consumes: `Widget`, `Catalog()`, `ByKey()` (Task 2); `authz.Grant{Resource, Action, Scope}`, `authz.DecideAny(grants []authz.Grant, resources []authz.Resource, a authz.Action) authz.Decision{Allowed bool, Scope authz.Scope}`, `authz.ResourceAny`, `authz.ActionAny` (`authz/catalog.go`, `authz/enforcer.go`, `authz/store.go`).
- Produces: `type UserPref struct{WidgetKey string; Visible bool; Position, Width, Height int}`; `type ResolvedWidget struct{Key, Title, Description, Category, Type, DataEndpoint, Scope string; Visible bool; Position, Width, Height int}` (JSON-tagged); `type PrefInput`, `type ConfigInput`, `type ConfigEntry` (used by Task 4 and Task 6); `type ClientError struct{Msg string}` + `func IsClientError(err error) bool`; `type ForbiddenWidgetError struct{WidgetKey string}` + `func IsForbiddenWidgetError(err error) (string, bool)`; `func Resolve(catalog []Widget, grants []authz.Grant, overrides map[string]bool, prefs map[string]UserPref) []ResolvedWidget` — Task 6's controller calls this exact signature.

- [ ] **Step 1: Write the failing test**

Create `dashboard/resolve_test.go`:

```go
package dashboard

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/authz"
)

func testCatalog() []Widget {
	return []Widget{
		{Key: "a", Title: "A", Category: CategoryCRM, Type: TypeList,
			Resource: authz.ResourceLead, Action: authz.ActionRead, DataEndpoint: "/a",
			DefaultVisible: true, DefaultPosition: 1, DefaultWidth: 6, DefaultHeight: 4},
		{Key: "b", Title: "B", Category: CategorySales, Type: TypeList,
			Resource: authz.ResourceQuote, Action: authz.ActionRead, DataEndpoint: "/b",
			DefaultVisible: false, DefaultPosition: 0, DefaultWidth: 4, DefaultHeight: 2},
	}
}

func TestResolve(t *testing.T) {
	wildcardGrant := []authz.Grant{{Resource: authz.ResourceAny, Action: authz.ActionAny, Scope: authz.ScopeAll}}
	leadOnlyGrant := []authz.Grant{{Resource: authz.ResourceLead, Action: authz.ActionRead, Scope: authz.ScopeOwn}}

	tests := []struct {
		name      string
		grants    []authz.Grant
		overrides map[string]bool
		prefs     map[string]UserPref
		wantKeys  []string
	}{
		{
			name:     "wildcard grant sees every enabled widget, sorted by position",
			grants:   wildcardGrant,
			wantKeys: []string{"b", "a"}, // b's DefaultPosition=0, a's=1
		},
		{
			name:     "zero grants sees nothing",
			grants:   nil,
			wantKeys: []string{},
		},
		{
			name:      "tenant-disabled hides a widget even from a wildcard grant",
			grants:    wildcardGrant,
			overrides: map[string]bool{"a": false},
			wantKeys:  []string{"b"},
		},
		{
			name:     "grant narrows to the one authorized widget",
			grants:   leadOnlyGrant,
			wantKeys: []string{"a"},
		},
		{
			name:     "pref for a retired key not in the catalog is ignored",
			grants:   wildcardGrant,
			prefs:    map[string]UserPref{"retired.widget": {WidgetKey: "retired.widget", Visible: true}},
			wantKeys: []string{"b", "a"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out := Resolve(testCatalog(), tt.grants, tt.overrides, tt.prefs)
			gotKeys := make([]string, len(out))
			for i, rw := range out {
				gotKeys[i] = rw.Key
			}
			assert.Equal(t, tt.wantKeys, gotKeys)
		})
	}
}

// TestResolve_SavedPrefOverridesDefault proves a saved preference replaces
// the catalog default for visible/position/width/height.
func TestResolve_SavedPrefOverridesDefault(t *testing.T) {
	wildcardGrant := []authz.Grant{{Resource: authz.ResourceAny, Action: authz.ActionAny, Scope: authz.ScopeAll}}
	prefs := map[string]UserPref{"a": {WidgetKey: "a", Visible: false, Position: 9, Width: 3, Height: 1}}

	out := Resolve(testCatalog(), wildcardGrant, nil, prefs)
	require.Len(t, out, 2)
	for _, rw := range out {
		if rw.Key != "a" {
			continue
		}
		assert.False(t, rw.Visible)
		assert.Equal(t, 9, rw.Position)
		assert.Equal(t, 3, rw.Width)
		assert.Equal(t, 1, rw.Height)
		return
	}
	t.Fatal("widget a missing from output")
}

// TestResolve_IncludesScope proves the caller's granted scope for a widget's
// resource rides through into the response, so the frontend can render
// "My Quotes" vs "All Quotes" and filter the dataEndpoint call accordingly.
func TestResolve_IncludesScope(t *testing.T) {
	ownGrant := []authz.Grant{{Resource: authz.ResourceLead, Action: authz.ActionRead, Scope: authz.ScopeOwn}}
	out := Resolve(testCatalog(), ownGrant, nil, nil)
	require.Len(t, out, 1)
	assert.Equal(t, "own", out[0].Scope)
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./dashboard/... -run TestResolve -v`
Expected: FAIL to build — `undefined: Resolve` / `undefined: UserPref`.

- [ ] **Step 3: Write the types**

Create `dashboard/types.go`:

```go
package dashboard

import "errors"

// UserPref is one caller's saved visibility/layout for one widget, whether
// loaded from dashboard_user_widget or freshly validated from a request.
type UserPref struct {
	WidgetKey string
	Visible   bool
	Position  int
	Width     int
	Height    int
}

// ResolvedWidget is one item in the GET /dashboard/widgets response: catalog
// metadata plus the caller's effective grant scope and their own
// visibility/layout (or the catalog default when they have none saved).
type ResolvedWidget struct {
	Key          string `json:"key"`
	Title        string `json:"title"`
	Description  string `json:"description"`
	Category     string `json:"category"`
	Type         string `json:"type"`
	DataEndpoint string `json:"dataEndpoint"`
	Scope        string `json:"scope"`
	Visible      bool   `json:"visible"`
	Position     int    `json:"position"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
}

// PrefInput is one widget's requested visibility/layout from a
// PUT /dashboard/widgets/preferences request body.
type PrefInput struct {
	WidgetKey string `json:"widgetKey"`
	Visible   bool   `json:"visible"`
	Position  int    `json:"position"`
	Width     int    `json:"width"`
	Height    int    `json:"height"`
}

// ConfigInput is one widget's requested tenant-wide enabled flag from a
// PUT /dashboard/config request body.
type ConfigInput struct {
	WidgetKey string `json:"widgetKey"`
	Enabled   bool   `json:"enabled"`
}

// ConfigEntry is one item in the GET /dashboard/config response: catalog
// metadata plus the tenant's effective enabled flag for it.
type ConfigEntry struct {
	Key      string `json:"key"`
	Title    string `json:"title"`
	Category string `json:"category"`
	Enabled  bool   `json:"enabled"`
}

// ClientError signals a client-caused validation failure that a controller
// maps to HTTP 400, mirroring crmactivity.ClientError.
type ClientError struct{ Msg string }

func (e ClientError) Error() string { return e.Msg }

// IsClientError reports whether err is a ClientError.
func IsClientError(err error) bool {
	var ce ClientError
	return errors.As(err, &ce)
}

// ForbiddenWidgetError signals a request named a real catalog widget the
// caller does not currently hold the grant for -- distinct from ClientError
// so the controller can log it as a security event.
type ForbiddenWidgetError struct{ WidgetKey string }

func (e ForbiddenWidgetError) Error() string {
	return "not authorized for widget: " + e.WidgetKey
}

// IsForbiddenWidgetError reports whether err is a ForbiddenWidgetError, and
// returns the offending key.
func IsForbiddenWidgetError(err error) (string, bool) {
	var fe ForbiddenWidgetError
	if errors.As(err, &fe) {
		return fe.WidgetKey, true
	}
	return "", false
}
```

- [ ] **Step 4: Write `Resolve`**

Create `dashboard/resolve.go`:

```go
package dashboard

import (
	"sort"

	"stonesuite-backend/authz"
)

// Resolve filters the catalog down to what the caller may see -- tenant
// override first, then the caller's grants -- and overlays the caller's
// saved preferences on top of catalog defaults. Pure: grants/overrides/prefs
// are all pre-loaded by the controller, so this has no I/O and is fully
// unit-testable. Result is sorted by effective Position ascending, Key as
// the tiebreaker.
//
// Only two things ever remove a widget from the result: the caller lacking
// the grant, or the tenant disabling it (which beats every role, including a
// wildcard grant). A widget the caller has personally hidden (visible=false)
// still appears, so a "manage widgets" panel can offer it back.
func Resolve(catalog []Widget, grants []authz.Grant, overrides map[string]bool, prefs map[string]UserPref) []ResolvedWidget {
	out := make([]ResolvedWidget, 0, len(catalog))
	for _, w := range catalog {
		if enabled, ok := overrides[w.Key]; ok && !enabled {
			continue
		}
		decision := authz.DecideAny(grants, []authz.Resource{w.Resource}, w.Action)
		if !decision.Allowed {
			continue
		}
		rw := ResolvedWidget{
			Key: w.Key, Title: w.Title, Description: w.Description,
			Category: string(w.Category), Type: string(w.Type), DataEndpoint: w.DataEndpoint,
			Scope:    string(decision.Scope),
			Visible:  w.DefaultVisible,
			Position: w.DefaultPosition,
			Width:    w.DefaultWidth,
			Height:   w.DefaultHeight,
		}
		if pref, ok := prefs[w.Key]; ok {
			rw.Visible = pref.Visible
			rw.Position = pref.Position
			rw.Width = pref.Width
			rw.Height = pref.Height
		}
		out = append(out, rw)
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Position != out[j].Position {
			return out[i].Position < out[j].Position
		}
		return out[i].Key < out[j].Key
	})
	return out
}
```

- [ ] **Step 5: Run the test to verify it passes**

Run: `go test ./dashboard/... -v`
Expected: all tests in the package PASS, including Task 2's.

- [ ] **Step 6: Commit**

```bash
git add dashboard/types.go dashboard/resolve.go dashboard/resolve_test.go
git commit -m "$(cat <<'EOF'
feat(dashboard): add response types and pure Resolve

Resolve composes tenant overrides, RBAC grants, and saved user
preferences into the GET /dashboard/widgets response -- no I/O, fully
unit-testable. Tenant-disabled beats every role, including wildcard
grants.
EOF
)"
```

---

### Task 4: Input validation — `dashboard/validate.go`

**Files:**
- Create: `dashboard/validate.go`
- Test: `dashboard/validate_test.go`

**Interfaces:**
- Consumes: `ByKey()` (Task 2); `PrefInput`, `ConfigInput`, `UserPref`, `ClientError`, `ForbiddenWidgetError`, `IsClientError()`, `IsForbiddenWidgetError()` (Task 3); `authz.Grant`, `authz.DecideAny` (`authz` package); `MinSize`, `MaxWidth`, `MaxHeight` (Task 2).
- Produces: `func ValidatePrefs(inputs []PrefInput, grants []authz.Grant) ([]UserPref, error)`, `func ValidateConfigUpdates(inputs []ConfigInput) error` — Task 6's controller calls both by these exact names.

- [ ] **Step 1: Write the failing test**

Create `dashboard/validate_test.go`:

```go
package dashboard

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/authz"
)

func TestValidatePrefs(t *testing.T) {
	grants := []authz.Grant{{Resource: authz.ResourceQuote, Action: authz.ActionRead, Scope: authz.ScopeAll}}

	tests := []struct {
		name    string
		inputs  []PrefInput
		wantErr string // "" = no error; "client" or "forbidden" otherwise
	}{
		{name: "empty input rejected", inputs: nil, wantErr: "client"},
		{name: "unknown widget key rejected",
			inputs:  []PrefInput{{WidgetKey: "does.not.exist", Width: 4, Height: 2}},
			wantErr: "client"},
		{name: "widget the caller has no grant for is rejected",
			inputs:  []PrefInput{{WidgetKey: "crm.leads", Width: 4, Height: 2}},
			wantErr: "forbidden"},
		{name: "negative position rejected",
			inputs:  []PrefInput{{WidgetKey: "sales.quotes", Position: -1, Width: 4, Height: 2}},
			wantErr: "client"},
		{name: "width over MaxWidth rejected",
			inputs:  []PrefInput{{WidgetKey: "sales.quotes", Width: 13, Height: 2}},
			wantErr: "client"},
		{name: "height under MinSize rejected",
			inputs:  []PrefInput{{WidgetKey: "sales.quotes", Width: 4, Height: 0}},
			wantErr: "client"},
		{name: "valid input accepted",
			inputs:  []PrefInput{{WidgetKey: "sales.quotes", Visible: true, Position: 2, Width: 6, Height: 4}},
			wantErr: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := ValidatePrefs(tt.inputs, grants)
			switch tt.wantErr {
			case "":
				require.NoError(t, err)
				require.Len(t, out, len(tt.inputs))
				assert.Equal(t, tt.inputs[0].WidgetKey, out[0].WidgetKey)
			case "client":
				require.Error(t, err)
				assert.True(t, IsClientError(err), "expected ClientError, got %T: %v", err, err)
			case "forbidden":
				require.Error(t, err)
				key, ok := IsForbiddenWidgetError(err)
				require.True(t, ok, "expected ForbiddenWidgetError, got %T: %v", err, err)
				assert.Equal(t, tt.inputs[0].WidgetKey, key)
			}
		})
	}
}

func TestValidateConfigUpdates(t *testing.T) {
	tests := []struct {
		name    string
		inputs  []ConfigInput
		wantErr bool
	}{
		{name: "empty rejected", inputs: nil, wantErr: true},
		{name: "unknown key rejected", inputs: []ConfigInput{{WidgetKey: "nope"}}, wantErr: true},
		{name: "known key accepted", inputs: []ConfigInput{{WidgetKey: "sales.quotes", Enabled: false}}, wantErr: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateConfigUpdates(tt.inputs)
			if tt.wantErr {
				require.Error(t, err)
				assert.True(t, IsClientError(err))
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./dashboard/... -run TestValidate -v`
Expected: FAIL to build — `undefined: ValidatePrefs`.

- [ ] **Step 3: Write the validators**

Create `dashboard/validate.go`:

```go
package dashboard

import "stonesuite-backend/authz"

// ValidatePrefs checks each input's WidgetKey resolves to a real catalog
// entry the caller currently holds the grant for, and that layout values are
// within the grid bounds (MinSize/MaxWidth/MaxHeight). Returns the resolved
// UserPref slice ready to persist, or the first problem found: ClientError
// for an unknown key or out-of-bounds value, ForbiddenWidgetError for a real
// key the caller lacks the grant for.
//
// It validates against the live production catalog (via ByKey) rather than
// taking one as a parameter -- unlike Resolve, there is no legitimate caller
// that would want to validate a request against anything but the real
// catalog.
func ValidatePrefs(inputs []PrefInput, grants []authz.Grant) ([]UserPref, error) {
	if len(inputs) == 0 {
		return nil, ClientError{Msg: "widgets must not be empty."}
	}
	out := make([]UserPref, 0, len(inputs))
	for _, in := range inputs {
		w, ok := ByKey(in.WidgetKey)
		if !ok {
			return nil, ClientError{Msg: "Unknown widget key: " + in.WidgetKey}
		}
		decision := authz.DecideAny(grants, []authz.Resource{w.Resource}, w.Action)
		if !decision.Allowed {
			return nil, ForbiddenWidgetError{WidgetKey: in.WidgetKey}
		}
		if in.Position < 0 {
			return nil, ClientError{Msg: "position must be >= 0 for widget " + in.WidgetKey}
		}
		if in.Width < MinSize || in.Width > MaxWidth {
			return nil, ClientError{Msg: "width must be between 1 and 12 for widget " + in.WidgetKey}
		}
		if in.Height < MinSize || in.Height > MaxHeight {
			return nil, ClientError{Msg: "height must be between 1 and 8 for widget " + in.WidgetKey}
		}
		out = append(out, UserPref{
			WidgetKey: in.WidgetKey, Visible: in.Visible,
			Position: in.Position, Width: in.Width, Height: in.Height,
		})
	}
	return out, nil
}

// ValidateConfigUpdates checks each input's WidgetKey resolves to a real
// catalog entry. No grant check: the caller already proved
// workflow_config:configure at the controller before reaching here, a
// workspace-wide capability independent of any one widget's own resource.
func ValidateConfigUpdates(inputs []ConfigInput) error {
	if len(inputs) == 0 {
		return ClientError{Msg: "widgets must not be empty."}
	}
	for _, in := range inputs {
		if _, ok := ByKey(in.WidgetKey); !ok {
			return ClientError{Msg: "Unknown widget key: " + in.WidgetKey}
		}
	}
	return nil
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./dashboard/... -v`
Expected: all tests in the package PASS.

- [ ] **Step 5: Commit**

```bash
git add dashboard/validate.go dashboard/validate_test.go
git commit -m "$(cat <<'EOF'
feat(dashboard): add preference and config input validation

ValidatePrefs rejects an unknown widget key (400) and separately a
real key the caller currently lacks the grant for
(ForbiddenWidgetError, so the controller can log it as a security
event) -- a caller cannot persist state for a widget it cannot see.
EOF
)"
```

---

### Task 5: DB store — `dashboard/store.go`

**Files:**
- Create: `dashboard/store.go`

**Interfaces:**
- Consumes: `UserPref`, `ConfigInput` (Task 3); the `dashboard_widget_config` / `dashboard_user_widget` tables (Task 1).
- Produces: `func WidgetConfigOverrides(ctx context.Context, pool *pgxpool.Pool) (map[string]bool, error)`, `func SetWidgetConfig(ctx context.Context, pool *pgxpool.Pool, updates []ConfigInput) error`, `func UserPrefs(ctx context.Context, pool *pgxpool.Pool, userID string) (map[string]UserPref, error)`, `func SaveUserPrefs(ctx context.Context, pool *pgxpool.Pool, userID string, prefs []UserPref) error`, `func ClearUserPrefs(ctx context.Context, pool *pgxpool.Pool, userID string) error` — Task 6's controller calls all five by these exact names.

This task has no dedicated unit test file: it is plain DB CRUD (upsert/select/delete) with no branching logic beyond what the `CHECK` constraints (Task 1) and Go-level validation (Task 4) already cover, the same posture the codebase already takes toward e.g. `crmactivity/store.go`'s CRUD functions (CLAUDE.md's table-driven-test rule targets *pure* functions; this is I/O). It's exercised end-to-end by the controller in Task 6 and reviewed by the `tenancy-security-reviewer` agent in Task 7.

- [ ] **Step 1: Write the store**

Create `dashboard/store.go`:

```go
package dashboard

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
)

// WidgetConfigOverrides returns the tenant's widget_key -> enabled overrides.
// A key absent from the result is enabled -- the table stores overrides
// only, never a full mirror of the catalog.
func WidgetConfigOverrides(ctx context.Context, pool *pgxpool.Pool) (map[string]bool, error) {
	rows, err := pool.Query(ctx, `SELECT widget_key, enabled FROM dashboard_widget_config`)
	if err != nil {
		return nil, fmt.Errorf("query dashboard widget config: %w", err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var key string
		var enabled bool
		if err := rows.Scan(&key, &enabled); err != nil {
			return nil, fmt.Errorf("scan dashboard widget config: %w", err)
		}
		out[key] = enabled
	}
	return out, rows.Err()
}

// SetWidgetConfig upserts the tenant-wide enabled flag for each given widget.
// Partial: widgets not named in updates keep whatever state they already
// had (enabled, by default).
func SetWidgetConfig(ctx context.Context, pool *pgxpool.Pool, updates []ConfigInput) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin set widget config: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, u := range updates {
		if _, err := tx.Exec(ctx, `
			INSERT INTO dashboard_widget_config (widget_key, enabled, updated_at)
			VALUES ($1, $2, NOW())
			ON CONFLICT (widget_key) DO UPDATE SET enabled = $2, updated_at = NOW()`,
			u.WidgetKey, u.Enabled); err != nil {
			return fmt.Errorf("upsert dashboard widget config %q: %w", u.WidgetKey, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit set widget config: %w", err)
	}
	return nil
}

// UserPrefs returns userID's saved widget_key -> preference map.
func UserPrefs(ctx context.Context, pool *pgxpool.Pool, userID string) (map[string]UserPref, error) {
	rows, err := pool.Query(ctx, `
		SELECT widget_key, visible, position, width, height
		FROM dashboard_user_widget WHERE user_id = $1`, userID)
	if err != nil {
		return nil, fmt.Errorf("query dashboard user widgets: %w", err)
	}
	defer rows.Close()
	out := map[string]UserPref{}
	for rows.Next() {
		var p UserPref
		if err := rows.Scan(&p.WidgetKey, &p.Visible, &p.Position, &p.Width, &p.Height); err != nil {
			return nil, fmt.Errorf("scan dashboard user widget: %w", err)
		}
		out[p.WidgetKey] = p
	}
	return out, rows.Err()
}

// SaveUserPrefs upserts userID's visibility/layout for each given widget.
// Partial: widgets not named in prefs keep whatever the caller last saved
// (or the catalog default, if never saved).
func SaveUserPrefs(ctx context.Context, pool *pgxpool.Pool, userID string, prefs []UserPref) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin save dashboard prefs: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	for _, p := range prefs {
		if _, err := tx.Exec(ctx, `
			INSERT INTO dashboard_user_widget (user_id, widget_key, visible, position, width, height, updated_at)
			VALUES ($1, $2, $3, $4, $5, $6, NOW())
			ON CONFLICT (user_id, widget_key) DO UPDATE
				SET visible = $3, position = $4, width = $5, height = $6, updated_at = NOW()`,
			userID, p.WidgetKey, p.Visible, p.Position, p.Width, p.Height); err != nil {
			return fmt.Errorf("upsert dashboard user widget %q: %w", p.WidgetKey, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit save dashboard prefs: %w", err)
	}
	return nil
}

// ClearUserPrefs deletes all of userID's saved preferences, reverting every
// widget to its catalog default.
func ClearUserPrefs(ctx context.Context, pool *pgxpool.Pool, userID string) error {
	if _, err := pool.Exec(ctx, `DELETE FROM dashboard_user_widget WHERE user_id = $1`, userID); err != nil {
		return fmt.Errorf("clear dashboard user widgets: %w", err)
	}
	return nil
}
```

- [ ] **Step 2: Verify the package builds**

Run: `go build ./... && go vet ./...`
Expected: no output, exit code 0.

- [ ] **Step 3: Commit**

```bash
git add dashboard/store.go
git commit -m "$(cat <<'EOF'
feat(dashboard): add widget config and preference store

Plain upsert/select/delete against dashboard_widget_config and
dashboard_user_widget, transactional for the bulk-upsert paths.
EOF
)"
```

---

### Task 6: Controller + routes — `controllers/dashboard.go`, `main.go`

**Files:**
- Create: `controllers/dashboard.go`
- Test: `controllers/dashboard_test.go`
- Modify: `main.go` (route registration)

**Interfaces:**
- Consumes: everything from Tasks 2-5 (`dashboard.Catalog`, `dashboard.Resolve`, `dashboard.ValidatePrefs`, `dashboard.ValidateConfigUpdates`, `dashboard.WidgetConfigOverrides`, `dashboard.SetWidgetConfig`, `dashboard.UserPrefs`, `dashboard.SaveUserPrefs`, `dashboard.ClearUserPrefs`, `dashboard.ConfigEntry`, `dashboard.IsForbiddenWidgetError`); `middleware.GetUserFromContext(ctx) (payload, error)` with `payload.ID string`; `tenancy.PoolFromContext(ctx) (*pgxpool.Pool, error)`; `authz.EffectiveGrants(ctx, pool, identityID) ([]authz.Grant, error)`; `authz.Check(ctx, pool, identityID, resource, action) (authz.Decision, error)`; `workflow.UserIDByIdentity(ctx, pool, identityID) (string, error)`; `writeJSON`, `fail`, `logSecurityEvent` (all in package `controllers`, `controllers/tenant.go` and `controllers/security_log.go`).
- Produces: `type DashboardOps struct{}`, `func NewDashboardOps() *DashboardOps`, methods `ListWidgets`, `SavePreferences`, `ResetPreferences`, `GetConfig`, `SetConfig` (all `func(w http.ResponseWriter, r *http.Request)`) — `main.go` wires these five directly.

- [ ] **Step 1: Write the failing test**

Create `controllers/dashboard_test.go`:

```go
package controllers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestDashboardOps_RequiresAuth proves every dashboard handler rejects an
// unauthenticated request before touching the tenant pool, mirroring
// TestQuoteOps_RequiresAuth (controllers/quote_test.go).
func TestDashboardOps_RequiresAuth(t *testing.T) {
	h := NewDashboardOps()
	handlers := map[string]http.HandlerFunc{
		"ListWidgets":       h.ListWidgets,
		"SavePreferences":   h.SavePreferences,
		"ResetPreferences":  h.ResetPreferences,
		"GetConfig":         h.GetConfig,
		"SetConfig":         h.SetConfig,
	}
	for name, fn := range handlers {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/tenant/dashboard/widgets", nil)
			rr := httptest.NewRecorder()
			fn(rr, req)
			assert.Equal(t, http.StatusUnauthorized, rr.Code, "%s must require auth", name)
		})
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./controllers/... -run TestDashboardOps -v`
Expected: FAIL to build — `undefined: NewDashboardOps`.

- [ ] **Step 3: Write the controller**

Create `controllers/dashboard.go`:

```go
package controllers

import (
	"encoding/json"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/dashboard"
	"stonesuite-backend/middleware"
	"stonesuite-backend/tenancy"
	"stonesuite-backend/workflow"
)

// DashboardOps groups the Dashboard Widgets handlers: the RBAC/tenant-config
// filtered widget catalog, per-user visibility/layout preferences, and the
// tenant-wide widget on/off admin config.
type DashboardOps struct{}

// NewDashboardOps constructs the handler group.
func NewDashboardOps() *DashboardOps { return &DashboardOps{} }

// resolveForCaller loads everything dashboard.Resolve needs for the current
// caller and returns the filtered, preference-overlaid widget list. Shared by
// ListWidgets, SavePreferences and ResetPreferences so all three return the
// same canonical shape. On failure it writes the response itself and returns
// ok=false.
func (h *DashboardOps) resolveForCaller(w http.ResponseWriter, r *http.Request) ([]dashboard.ResolvedWidget, bool) {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return nil, false
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return nil, false
	}
	grants, err := authz.EffectiveGrants(r.Context(), pool, payload.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load permissions.")
		return nil, false
	}
	overrides, err := dashboard.WidgetConfigOverrides(r.Context(), pool)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load dashboard configuration.")
		return nil, false
	}
	var prefs map[string]dashboard.UserPref
	if userID, idErr := workflow.UserIDByIdentity(r.Context(), pool, payload.ID); idErr == nil && userID != "" {
		prefs, err = dashboard.UserPrefs(r.Context(), pool, userID)
		if err != nil {
			fail(w, http.StatusInternalServerError, "Failed to load dashboard preferences.")
			return nil, false
		}
	}
	return dashboard.Resolve(dashboard.Catalog(), grants, overrides, prefs), true
}

// ListWidgets GET /api/tenant/dashboard/widgets
func (h *DashboardOps) ListWidgets(w http.ResponseWriter, r *http.Request) {
	widgets, ok := h.resolveForCaller(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "widgets": widgets})
}

// SavePreferences PUT /api/tenant/dashboard/widgets/preferences
// body: {"widgets":[{"widgetKey":"sales.quotes","visible":true,"position":0,"width":6,"height":4}]}
func (h *DashboardOps) SavePreferences(w http.ResponseWriter, r *http.Request) {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return
	}
	var req struct {
		Widgets []dashboard.PrefInput `json:"widgets"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	grants, err := authz.EffectiveGrants(r.Context(), pool, payload.ID)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load permissions.")
		return
	}
	prefs, err := dashboard.ValidatePrefs(req.Widgets, grants)
	if err != nil {
		if key, isForbidden := dashboard.IsForbiddenWidgetError(err); isForbidden {
			logSecurityEvent(r, "dashboard_pref_denied", "identity", payload.ID, "widget", key)
		}
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	userID, err := workflow.UserIDByIdentity(r.Context(), pool, payload.ID)
	if err != nil || userID == "" {
		fail(w, http.StatusInternalServerError, "Failed to resolve user record.")
		return
	}
	if err := dashboard.SaveUserPrefs(r.Context(), pool, userID, prefs); err != nil {
		fail(w, http.StatusInternalServerError, "Failed to save dashboard preferences.")
		return
	}
	widgets, ok := h.resolveForCaller(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "widgets": widgets})
}

// ResetPreferences DELETE /api/tenant/dashboard/widgets/preferences
func (h *DashboardOps) ResetPreferences(w http.ResponseWriter, r *http.Request) {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return
	}
	userID, err := workflow.UserIDByIdentity(r.Context(), pool, payload.ID)
	if err != nil || userID == "" {
		fail(w, http.StatusInternalServerError, "Failed to resolve user record.")
		return
	}
	if err := dashboard.ClearUserPrefs(r.Context(), pool, userID); err != nil {
		fail(w, http.StatusInternalServerError, "Failed to reset dashboard preferences.")
		return
	}
	widgets, ok := h.resolveForCaller(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "widgets": widgets})
}

// requireDashboardConfig checks the caller may configure workspace-wide
// dashboard settings, mirroring CRMAdminOps.requireConfig
// (controllers/crm_admin.go). Returns the tenant pool on success; on failure
// it writes the response itself.
func (h *DashboardOps) requireDashboardConfig(w http.ResponseWriter, r *http.Request) (*pgxpool.Pool, bool) {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return nil, false
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return nil, false
	}
	decision, err := authz.Check(r.Context(), pool, payload.ID, authz.ResourceWorkflowConfig, authz.ActionConfigure)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, false
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", payload.ID, "resource", "dashboard_config", "action", string(authz.ActionConfigure))
		fail(w, http.StatusForbidden, "You do not have permission to configure this workspace.")
		return nil, false
	}
	return pool, true
}

// configEntries builds the full GET /dashboard/config response: every
// catalog widget plus its tenant-effective enabled flag (defaulting true).
func configEntries(overrides map[string]bool) []dashboard.ConfigEntry {
	catalog := dashboard.Catalog()
	out := make([]dashboard.ConfigEntry, 0, len(catalog))
	for _, wgt := range catalog {
		enabled := true
		if v, ok := overrides[wgt.Key]; ok {
			enabled = v
		}
		out = append(out, dashboard.ConfigEntry{
			Key: wgt.Key, Title: wgt.Title, Category: string(wgt.Category), Enabled: enabled,
		})
	}
	return out
}

// GetConfig GET /api/tenant/dashboard/config
func (h *DashboardOps) GetConfig(w http.ResponseWriter, r *http.Request) {
	pool, ok := h.requireDashboardConfig(w, r)
	if !ok {
		return
	}
	overrides, err := dashboard.WidgetConfigOverrides(r.Context(), pool)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load dashboard configuration.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "widgets": configEntries(overrides)})
}

// SetConfig PUT /api/tenant/dashboard/config
// body: {"widgets":[{"widgetKey":"sales.payments","enabled":false}]}
func (h *DashboardOps) SetConfig(w http.ResponseWriter, r *http.Request) {
	pool, ok := h.requireDashboardConfig(w, r)
	if !ok {
		return
	}
	var req struct {
		Widgets []dashboard.ConfigInput `json:"widgets"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	if err := dashboard.ValidateConfigUpdates(req.Widgets); err != nil {
		fail(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := dashboard.SetWidgetConfig(r.Context(), pool, req.Widgets); err != nil {
		fail(w, http.StatusInternalServerError, "Failed to update dashboard configuration.")
		return
	}
	overrides, err := dashboard.WidgetConfigOverrides(r.Context(), pool)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load dashboard configuration.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "widgets": configEntries(overrides)})
}
```

- [ ] **Step 4: Run the test to verify it passes**

Run: `go test ./controllers/... -run TestDashboardOps -v`
Expected: `TestDashboardOps_RequiresAuth` PASS for all 5 handlers.

- [ ] **Step 5: Wire the routes into `main.go`**

In `main.go`, find this exact block (currently around line 421):

```go
		mux.Handle("DELETE /api/tenant/sso-configs/{id}", tenantChain(sso.DeleteConfig))

		// Tenant-wide audit-log browser (audit:read, scope-narrowed on the actor).
```

Replace it with:

```go
		mux.Handle("DELETE /api/tenant/sso-configs/{id}", tenantChain(sso.DeleteConfig))

		// Dashboard Widgets: RBAC + tenant-config filtered widget catalog, plus
		// per-user visibility/layout preferences. Config routes require
		// workflow_config:configure (mirrors CRMAdminOps' admin gate).
		dashboardOps := controllers.NewDashboardOps()
		mux.Handle("GET /api/tenant/dashboard/widgets", tenantChain(dashboardOps.ListWidgets))
		mux.Handle("PUT /api/tenant/dashboard/widgets/preferences", tenantChain(dashboardOps.SavePreferences))
		mux.Handle("DELETE /api/tenant/dashboard/widgets/preferences", tenantChain(dashboardOps.ResetPreferences))
		mux.Handle("GET /api/tenant/dashboard/config", tenantChain(dashboardOps.GetConfig))
		mux.Handle("PUT /api/tenant/dashboard/config", tenantChain(dashboardOps.SetConfig))

		// Tenant-wide audit-log browser (audit:read, scope-narrowed on the actor).
```

- [ ] **Step 6: Verify the full build**

Run: `go build ./... && go vet ./...`
Expected: no output, exit code 0.

- [ ] **Step 7: Commit**

```bash
git add controllers/dashboard.go controllers/dashboard_test.go main.go
git commit -m "$(cat <<'EOF'
feat(dashboard): add Dashboard Widgets API and wire routes

GET/PUT/DELETE /api/tenant/dashboard/widgets(/preferences) and
GET/PUT /api/tenant/dashboard/config, all behind tenantChain. Config
routes reuse workflow_config:configure -- no new authz resource.
EOF
)"
```

---

### Task 7: Full verification and security review

**Files:** none (verification only).

- [ ] **Step 1: Run the full test suite**

Run: `go build ./... && go vet ./... && go test ./...`
Expected: exit code 0, all tests PASS (including `dashboard/...`, `controllers/...`, and the pre-existing suite — this proves nothing in the rest of the app regressed).

- [ ] **Step 2: Run the RBAC catalog drift test explicitly**

Run: `go test ./controllers/... -run TestResourceForKeyResourcesAreGrantable -v` and `go test ./dashboard/... -run TestCatalogPermissionsAreGrantable -v`
Expected: both PASS — confirms every widget's `{Resource, Action}` is real and every CRM resource stays grantable (this task didn't touch `crm.go`, so the second is a regression guard, not new coverage).

- [ ] **Step 3: Dispatch the `tenancy-security-reviewer` agent**

This module adds new tenant-scoped handlers (`controllers/dashboard.go`) and new store queries (`dashboard/store.go`) — exactly the trigger condition documented for this agent in `CLAUDE.md`. Run it against the diff on this branch (`feat/dashboard-widgets` vs `master`) and address any findings it reports before moving on. Pay particular attention to whether it flags:
   - Any route registered without `tenantChain`.
   - Any query in `dashboard/store.go` missing a `user_id = $1` (or equivalent tenant-scoping) predicate — note `dashboard_widget_config` is intentionally tenant-wide (no user filter) while `dashboard_user_widget` must always filter by the caller's own `user_id`.
   - The `PUT /dashboard/widgets/preferences` path writing to a `userID` other than the caller's own (it must always be `workflow.UserIDByIdentity(ctx, pool, payload.ID)` — never a client-supplied id).

- [ ] **Step 4: Confirm the git log for this branch**

Run: `git log master..HEAD --oneline`
Expected: one commit per task (schema, catalog, types+resolve, validate, store, controller+routes), plus the two `docs:` commits (design spec, this plan) already on the branch.
