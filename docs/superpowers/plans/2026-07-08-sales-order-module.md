# Sales Order Module Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a production-grade relational Sales Order module (header + line items + status lifecycle + inventory allocation + listing/search) plus the shared Inventory domain it depends on, integrated seamlessly into the existing v2 CRM backend.

**Architecture:** New relational tables that are siblings of `customer` (hybrid `SERIAL`+`UUID` PK, `employee`-based audit columns, reused `lkp_*` lookups). Two generic, opt-in enhancements to the shared `query/` engine (sorting whitelist + global search) so every list module benefits. New `salesorder/` and `inventory/` Go packages holding domain logic, thin `controllers/` handlers mirroring `CRMOps`, and listing that reuses the `query/` engine verbatim through a per-entity `FieldResolver`.

**Tech Stack:** Go (net/http, pgx/pgxpool), PostgreSQL (per-tenant DB), `testify` for tests, Cloudflare R2 for attachments.

## Global Constraints

- **No `tenant_id`/`ss_tenant_id` column on any tenant-DB table** — the DB connection is the tenant scope.
- **Migrations are idempotent + append-only** — edit `database/migrations/tenant/schema.sql`; `CREATE TABLE IF NOT EXISTS`, `INSERT ... ON CONFLICT DO NOTHING`, `ADD COLUMN IF NOT EXISTS` + `DEFAULT`. Never DROP/rename/`ALTER TYPE`/`CREATE INDEX CONCURRENTLY`.
- **Every `/api/tenant/` route** goes through `tenantChain` (`RequireAuth → per-tenant rate limit → TenantResolver`) and enforces, in-handler: RBAC `authz.Check(resource, action)` before any write; scope filtering on lists; single-record IDOR guard returning **404 (not 403)** on denial with `logSecurityEvent(r, "idor_denied", ...)`.
- **Filter engine invariants:** filter × scope ANDed (never OR); field keys resolved through `FieldResolver` whitelist (unknown → `*query.InvalidFilterError` → HTTP 400); all client values bound as `$n`; keyset pagination only (no `OFFSET`, no `COUNT(*)`); `query` package stays dependency-free.
- **Money `DECIMAL(15,2)`, quantity `DECIMAL(14,3)`, percent `DECIMAL(6,4)`, exchange rate `DECIMAL(18,6)`.**
- **Custom fields:** ≤15 per workflow, validated against `workflow_field_definitions` via `workflow.ValidateCustomFields`/`ValidateCustomFieldsPartial`.
- **Response envelope:** `{ "success": bool, "message"?: string, ... }` via `controllers.writeJSON`/`fail`.
- **Conventional Commits.** New features need tests. `go build ./... && go vet ./... && go test ./...` must pass before each commit.
- Spec: `docs/superpowers/specs/2026-07-08-sales-order-module-design.md` (authoritative; cite section numbers).

---

## File Structure

**Created:**
- `query/search.go` — `SearchResolver` interface + doc (kept separate from `filter.go` to isolate the new capability).
- `salesorder/types.go` — DTOs / row structs.
- `salesorder/calc.go` — pure money math (line + header totals).
- `salesorder/transitions.go` — pure status transition map + validation.
- `salesorder/numbering.go` — pure `SORD-000001` formatter.
- `salesorder/store.go` — relational store (create/update/get/list/delete/search/transition), transactional.
- `salesorder/resolver.go` — `salesOrderResolver` implementing `query.FieldResolver` + `query.SortResolver` + `query.SearchResolver`.
- `salesorder/allocation.go` — inventory allocation reserve/release + inventory-tab aggregation.
- `salesorder/*_test.go` — unit + integration tests.
- `inventory/types.go`, `inventory/store.go`, `inventory/resolver.go`, `inventory/*_test.go` — inventory item/stock CRUD + list.
- `controllers/salesorder.go` — `SalesOrderOps` HTTP handlers.
- `controllers/inventory.go` — `InventoryOps` HTTP handlers.

**Modified:**
- `query/filter.go` — add `Search` field to `Request`; add `SortResolver` interface.
- `query/builder.go` — consult `SortResolver` in `effectiveSort`/sort-expr; AND the `SearchResolver` predicate.
- `database/migrations/tenant/schema.sql` — append new tables, indexes, seeds.
- `authz/catalog.go` — add `ResourceInventoryItem` + actions (`sales_order` already present).
- `main.go` — register sales-order + inventory routes via `tenantChain`.

---

## Phase 0 — Shared query engine: generic sorting + global search

Spec §11.5, §11.6. Backward-compatible: modules that don't opt in are byte-identical. Guard with `filter-invariant-checker` after this phase.

### Task 0.1: Generic sortable-field extension (`SortResolver`)

**Files:**
- Modify: `query/filter.go` (add interface near `FieldResolver`, line ~107)
- Modify: `query/builder.go:12-16` (`sortableFields`), `:73-86` (sort expr in `Build`), `:98` (keyset DT), `:130-148` (`effectiveSort`)
- Test: `query/sort_resolver_test.go`

**Interfaces:**
- Produces: `type SortResolver interface { SortExpr(key string) (expr string, dt DataType, ok bool) }` — a `FieldResolver` may also implement this to declare extra **NOT NULL** sort columns; `expr`/`dt` are used for `ORDER BY` and keyset comparison. Default fields (`created_at`,`updated_at`,`record_number`) keep resolving via `FieldResolver.Resolve`.

- [ ] **Step 1: Write the failing test**

```go
// query/sort_resolver_test.go
package query

import "testing"

// fakeSortRes resolves id + a NOT NULL numeric "grand_total" column and declares
// grand_total sortable via SortResolver.
type fakeSortRes struct{}

func (fakeSortRes) Resolve(key string) (string, DataType, bool) {
	switch key {
	case "id":
		return "t.uuid::text", TypeString, true
	case "created_at":
		return "t.created_at", TypeDate, true
	case "grand_total":
		return "t.grand_total::text", TypeString, true // filter expr (irrelevant to sort)
	}
	return "", "", false
}
func (fakeSortRes) SortExpr(key string) (string, DataType, bool) {
	if key == "grand_total" {
		return "t.grand_total", TypeNumber, true // sort uses the raw numeric column
	}
	return "", "", false
}

func TestBuild_SortResolver_AllowsExtraSortField(t *testing.T) {
	b, err := Build(Request{Sort: []SortKey{{Field: "grand_total", Dir: DirAsc}}}, fakeSortRes{}, 1)
	if err != nil {
		t.Fatalf("expected grand_total to be sortable, got %v", err)
	}
	if b.OrderBy != "t.grand_total ASC, t.uuid::text ASC" {
		t.Fatalf("unexpected order by: %q", b.OrderBy)
	}
}

func TestBuild_SortResolver_RejectsUnsortableField(t *testing.T) {
	_, err := Build(Request{Sort: []SortKey{{Field: "memo", Dir: DirAsc}}}, fakeSortRes{}, 1)
	if _, ok := err.(*InvalidFilterError); !ok {
		t.Fatalf("expected InvalidFilterError for unsortable field, got %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./query/ -run TestBuild_SortResolver -v`
Expected: FAIL — `grand_total` rejected by `effectiveSort` (`field is not sortable`) / `SortResolver` undefined.

- [ ] **Step 3: Add the `SortResolver` interface**

In `query/filter.go`, directly below the `FieldResolver` interface (after line 107):

```go
// SortResolver is an optional interface a FieldResolver may also implement to
// declare additional sortable columns beyond the built-in created_at/updated_at/
// record_number. The returned expression MUST be NOT NULL so keyset pagination
// stays correct (the builder always appends the row id as a tiebreaker). dt is
// used to coerce the cursor value for the keyset comparison.
type SortResolver interface {
	SortExpr(key string) (expr string, dt DataType, ok bool)
}
```

- [ ] **Step 4: Consult `SortResolver` in the builder**

In `query/builder.go`, replace the sort-expr lookups in `Build` (lines 77 and 98) and the gate in `effectiveSort` (lines 138-140) with resolver-aware helpers. Replace line 77 `sortExpr, _, _ := r.Resolve(sort.Field)` and the later `_, sortDT, _ := r.Resolve(sort.Field)` (line 98) by computing once, above the `orderBy` assembly:

```go
	sortExpr, sortDT := sortExprFor(sort.Field, r)
```

Use `sortExpr` in `orderBy` (line 86) and pass `sortDT` into `keysetSQL` (line 99) instead of re-resolving. Then add these helpers at the bottom of `builder.go`:

```go
// sortExprFor returns the ORDER BY / keyset expression + data type for a sort
// field: from SortResolver when the resolver provides one, else from Resolve
// (the built-in stable columns).
func sortExprFor(field string, r FieldResolver) (string, DataType) {
	if sr, ok := r.(SortResolver); ok {
		if expr, dt, ok := sr.SortExpr(field); ok {
			return expr, dt
		}
	}
	expr, dt, _ := r.Resolve(field)
	return expr, dt
}

// isSortable reports whether a field may be sorted: a built-in stable column or
// one the resolver declares via SortResolver.
func isSortable(field string, r FieldResolver) bool {
	if sortableFields[field] {
		return true
	}
	if sr, ok := r.(SortResolver); ok {
		_, _, ok := sr.SortExpr(field)
		return ok
	}
	return false
}
```

Change `effectiveSort` (line 138) from `if !sortableFields[k.Field] {` to `if !isSortable(k.Field, r) {`.

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./query/ -v`
Expected: PASS (new tests + all existing `query` tests still green).

- [ ] **Step 6: Commit**

```bash
git add query/filter.go query/builder.go query/sort_resolver_test.go
git commit -m "feat(query): allow resolvers to declare extra stable sort fields via SortResolver"
```

### Task 0.2: Generic global search (`SearchResolver` + `Request.Search`)

**Files:**
- Modify: `query/filter.go:78-83` (`Request`)
- Create: `query/search.go`
- Modify: `query/builder.go:55-70` (append search predicate inside `Build`)
- Test: `query/search_resolver_test.go`

**Interfaces:**
- Produces: `Request.Search string` (json `search`); `type SearchResolver interface { SearchPredicate(placeholder string) string }` — resolver returns a self-contained boolean SQL fragment matching the term bound at `placeholder` (e.g. `"$3"`); the fragment supplies its own `'%'||$n||'%'` wildcarding and may include correlated `EXISTS`. Builder ANDs it with scope+filters. Empty `Search` or a resolver without `SearchResolver` → search is a no-op predicate for the former, `*InvalidFilterError` for the latter when `Search` is set.

- [ ] **Step 1: Write the failing test**

```go
// query/search_resolver_test.go
package query

import (
	"strings"
	"testing"
)

type fakeSearchRes struct{ fakeSortRes }

func (fakeSearchRes) SearchPredicate(ph string) string {
	return "(t.number ILIKE '%'||" + ph + "||'%' OR t.memo ILIKE '%'||" + ph + "||'%')"
}

func TestBuild_Search_AppendsParameterizedPredicate(t *testing.T) {
	b, err := Build(Request{Search: "acme"}, fakeSearchRes{}, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(b.Where, "t.number ILIKE '%'||$1||'%'") {
		t.Fatalf("search predicate not in WHERE: %q", b.Where)
	}
	if len(b.Args) != 1 || b.Args[0] != "acme" {
		t.Fatalf("search term must be bound as a parameter, got args=%v", b.Args)
	}
}

func TestBuild_Search_UnsupportedResolver_Is400(t *testing.T) {
	_, err := Build(Request{Search: "acme"}, fakeSortRes{}, 1) // fakeSortRes has no SearchPredicate
	if _, ok := err.(*InvalidFilterError); !ok {
		t.Fatalf("expected InvalidFilterError when search unsupported, got %v", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./query/ -run TestBuild_Search -v`
Expected: FAIL — `Request` has no `Search` field / `SearchResolver` undefined.

- [ ] **Step 3: Add `Search` to `Request` and the interface**

In `query/filter.go`, add to `Request` (inside the struct, after `Cursor`):

```go
	Search  string    `json:"search"` // optional global search term (see SearchResolver)
```

Create `query/search.go`:

```go
package query

// SearchResolver is an optional interface a FieldResolver may implement to power
// a single global-search box. SearchPredicate returns a self-contained SQL
// boolean expression that matches the search term bound at placeholder (e.g.
// "$3"); it supplies its own wildcarding ('%'||$n||'%') and may reference other
// tables via correlated EXISTS. The engine binds the raw term as one parameter
// and ANDs the fragment onto scope+filters, so the OR lives inside the fragment
// and the "filter x scope = AND" invariant is preserved. The fragment is trusted
// per-entity code; it must reference only the given placeholder for the value.
type SearchResolver interface {
	SearchPredicate(placeholder string) string
}
```

- [ ] **Step 4: Append the search predicate in `Build`**

In `query/builder.go`, inside `Build`, immediately after the filter loop (after line 70, before the sort block at line 72):

```go
	// --- global search (optional) ---
	if req.Search != "" {
		sr, ok := r.(SearchResolver)
		if !ok {
			return Built{}, invalid("search", "search is not supported for this resource")
		}
		frag := sr.SearchPredicate(p.add(req.Search))
		if frag != "" {
			preds = append(preds, frag)
		}
	}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `go test ./query/ -v`
Expected: PASS (new tests + existing green).

- [ ] **Step 6: Run the filter-invariant guard**

Run: `grep -rE '"stonesuite-backend/(workflow|crmstore|controllers|authz|tenancy)"' query/`
Expected: no output (the `query` package remains dependency-free).

- [ ] **Step 7: Commit**

```bash
git add query/filter.go query/search.go query/builder.go query/search_resolver_test.go
git commit -m "feat(query): add generic global search via SearchResolver + Request.Search"
```

---

## Phase 1 — Database schema + RBAC

Spec §5, §6, §11.9. Follow the **add-migration** skill: append idempotent blocks to `database/migrations/tenant/schema.sql`; run the **migration-auditor** agent after. Create order (FK dependency): lookups → `inventory_item` → `inventory_stock` → `sales_order` → `sales_order_item` → `inventory_allocation` → `sales_order_history`.

### Task 1.1: Inventory reference lookups + inventory tables

**Files:**
- Modify: `database/migrations/tenant/schema.sql` (append at end)

- [ ] **Step 1: Append the reference-lookup + inventory blocks**

Copy the SQL from spec §5.1 (`lkp_unit`, `lkp_warehouse`, `lkp_tax_rate` with seeds) and §5.2 (`inventory_item`, `inventory_stock`) verbatim into `schema.sql`, each under a `-- ── <NNN>_<name> ── ` comment header matching the existing convention. Omit `inventory_allocation` here (it FKs `sales_order`, added in Task 1.2).

- [ ] **Step 2: Build to verify SQL is well-formed at the app layer**

Run: `go build ./...`
Expected: PASS (no Go changes yet; this confirms nothing references removed symbols).

- [ ] **Step 3: Apply the schema to a scratch DB twice (idempotency)**

Run (bash), using a local Postgres if available:
```bash
psql "$SCRATCH_DB_URL" -f database/migrations/tenant/schema.sql
psql "$SCRATCH_DB_URL" -f database/migrations/tenant/schema.sql
```
Expected: both runs succeed with **no errors** (second run is a no-op). If no local DB, skip and rely on CI; note it in the commit.

- [ ] **Step 4: Commit**

```bash
git add database/migrations/tenant/schema.sql
git commit -m "feat(db): add inventory lookups (unit/warehouse/tax_rate) and inventory_item/stock"
```

### Task 1.2: Sales Order tables, allocation, history, indexes

**Files:**
- Modify: `database/migrations/tenant/schema.sql` (append)

- [ ] **Step 1: Append sales-order tables + allocation + history + indexes**

Copy verbatim from spec §5.3 (`sales_order`), §5.4 (`sales_order_item`, `sales_order_history`), §5.2 (`inventory_allocation` — it FKs `sales_order`/`sales_order_item`, so it must come **after** those two), and all indexes from §6 and §11.9. De-duplicate the §6/§11.9 index sets (the migration keeps one `CREATE INDEX IF NOT EXISTS` per index name).

- [ ] **Step 2: Build + apply twice (idempotency)**

Run:
```bash
go build ./...
psql "$SCRATCH_DB_URL" -f database/migrations/tenant/schema.sql && psql "$SCRATCH_DB_URL" -f database/migrations/tenant/schema.sql
```
Expected: build PASS; both `psql` runs succeed with no errors.

- [ ] **Step 3: Run the migration-auditor agent**

Dispatch the `migration-auditor` agent on the `schema.sql` diff. Expected output: `No schema idempotency/safety issues found...`. Fix any CRITICAL/HIGH findings before committing.

- [ ] **Step 4: Commit**

```bash
git add database/migrations/tenant/schema.sql
git commit -m "feat(db): add sales_order, sales_order_item, sales_order_history, inventory_allocation + indexes"
```

### Task 1.3: RBAC — `inventory_item` resource

**Files:**
- Modify: `authz/catalog.go` (resource consts ~line 21-57; `catalog` slice ~line 85-228)
- Test: `authz/catalog_test.go` (existing) + run `controllers/rbac_catalog_drift_test.go`

**Interfaces:**
- Produces: `authz.ResourceInventoryItem Resource = "inventory_item"` with actions `Create/Read/Update/Delete`. (`authz.ResourceSalesOrder` already exists with all 5 actions.)

- [ ] **Step 1: Write the failing test**

Add to `authz/catalog_test.go`:

```go
func TestCatalog_InventoryItemPermissions(t *testing.T) {
	for _, a := range []Action{ActionCreate, ActionRead, ActionUpdate, ActionDelete} {
		if !IsValidPermission(ResourceInventoryItem, a) {
			t.Fatalf("inventory_item:%s must be a valid permission", a)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./authz/ -run TestCatalog_InventoryItemPermissions -v`
Expected: FAIL — `ResourceInventoryItem` undefined.

- [ ] **Step 3: Add the resource + catalog entries**

In `authz/catalog.go`, add the const in the resource block: `ResourceInventoryItem Resource = "inventory_item"`. In the `catalog` slice add: `{ResourceInventoryItem, ActionCreate}, {ResourceInventoryItem, ActionRead}, {ResourceInventoryItem, ActionUpdate}, {ResourceInventoryItem, ActionDelete},`.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./authz/ ./controllers/ -run 'Catalog|RBAC' -v`
Expected: PASS (including the drift test).

- [ ] **Step 5: Commit**

```bash
git add authz/catalog.go authz/catalog_test.go
git commit -m "feat(authz): add inventory_item RBAC resource"
```

---

## Phase 2 — Inventory domain (item + stock CRUD + listing)

Spec §3, §10 (Inventory endpoints), §11. Mirror the `customer` store/controller pattern. This phase ships working item management independent of Sales Order.

### Task 2.1: Inventory types + row scanning

**Files:**
- Create: `inventory/types.go`
- Test: `inventory/types_test.go`

**Interfaces:**
- Produces: `type Item struct { ID string; SKU string; Name string; Description string; UnitID int; UnitPrice float64; CurrencyID *int; TaxRateID *int; IsActive bool; CustomFields map[string]any; CreatedAt, UpdatedAt time.Time }`; `type CreateItemInput struct { SKU, Name, Description string; UnitID int; UnitPrice float64; CurrencyID, TaxRateID *int; CustomFields map[string]any }`. (ID is the `inventory_item_uuid`.)

- [ ] **Step 1: Write the failing test** — assert the structs marshal with the expected JSON keys.

```go
// inventory/types_test.go
package inventory

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestItem_JSONKeys(t *testing.T) {
	b, _ := json.Marshal(Item{ID: "u", SKU: "SLB-1", Name: "Slab"})
	for _, k := range []string{`"id"`, `"sku"`, `"name"`} {
		if !strings.Contains(string(b), k) {
			t.Fatalf("missing key %s in %s", k, b)
		}
	}
}
```

- [ ] **Step 2: Run to verify it fails** — Run: `go test ./inventory/ -run TestItem_JSONKeys -v` → FAIL (package/struct missing).

- [ ] **Step 3: Implement `inventory/types.go`** with the structs above and json tags (`id`,`sku`,`name`,`description`,`unitId`,`unitPrice`,`currencyId`,`taxRateId`,`isActive`,`customFields`,`createdAt`,`updatedAt`).

- [ ] **Step 4: Run to verify it passes** — Run: `go test ./inventory/ -run TestItem_JSONKeys -v` → PASS.

- [ ] **Step 5: Commit** — `git commit -m "feat(inventory): add item types"`.

### Task 2.2: Inventory store (CRUD + list) + resolver

**Files:**
- Create: `inventory/store.go`, `inventory/resolver.go`
- Test: `inventory/store_test.go` (integration, gated on `INVENTORY_TEST_DB_URL`)

**Interfaces:**
- Consumes: `inventory.Item`, `inventory.CreateItemInput`; `query.Request`, `query.Build`.
- Produces: `func Create(ctx, pool, in CreateItemInput, actorEmployeeID int) (*Item, error)`; `func Get(ctx, pool, uuid string) (*Item, error)` (ErrNotFound sentinel); `func Update(ctx, pool, uuid string, in CreateItemInput, actorEmployeeID int) error`; `func SoftDelete(ctx, pool, uuid string, actorEmployeeID int) error`; `func Search(ctx, pool string, req query.Request) (Page, error)` where `type Page struct { Records []Item; NextCursor string; HasMore bool }`; `type resolver struct{}` implementing `query.FieldResolver` for keys `id, sku, name, is_active, unit_id, tax_rate_id, created_at, updated_at` + `cf:<key>`.

- [ ] **Step 1: Write the failing integration test**

```go
// inventory/store_test.go
package inventory

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func testPool(t *testing.T) *pgxpool.Pool {
	url := os.Getenv("INVENTORY_TEST_DB_URL")
	if url == "" {
		t.Skip("INVENTORY_TEST_DB_URL not set")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func TestCreateAndGetItem(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	it, err := Create(ctx, pool, CreateItemInput{SKU: "SLB-CAR-3CM", Name: "Carrara Slab 3cm", UnitID: 5, UnitPrice: 42.00}, 1)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := Get(ctx, pool, it.ID)
	if err != nil || got.SKU != "SLB-CAR-3CM" {
		t.Fatalf("get: %v got=%+v", err, got)
	}
}
```

- [ ] **Step 2: Run to verify it fails** — Run: `go test ./inventory/ -run TestCreateAndGetItem -v` → FAIL (funcs undefined; or SKIP without DB — if skipped, still implement).

- [ ] **Step 3: Implement `inventory/store.go`**

Mirror `crmstore/relational_store.go` conventions (pgx named args, `scanRecord`-style row mapping, `ErrRecordNotFound` sentinel). `Create` inserts into `inventory_item` (SKU/name/description/unit/price/tax/currency/custom_fields, `inventory_item_created_by = actorEmployeeID`) and returns the row via `RETURNING inventory_item_uuid, ...`. Validate `CustomFields` with `workflow.ValidateCustomFields` against the inventory workflow's field defs if one is configured (else skip). `Search` builds SQL: `SELECT ... FROM inventory_item WHERE inventory_item_deleted_at IS NULL` + `query.Build(req, resolver{}, startIdx)` (`AND Where`, `AND Keyset`, `ORDER BY`, `LIMIT EffLimit+1`), returns `Page` with `query.NextCursor(...)`.

- [ ] **Step 4: Implement `inventory/resolver.go`**

```go
package inventory

import (
	"regexp"
	"strings"

	"stonesuite-backend/query"
)

var validCustomKey = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

type resolver struct{}

var systemFields = map[string]struct {
	expr string
	dt   query.DataType
}{
	"id":         {"i.inventory_item_uuid::text", query.TypeString},
	"sku":        {"i.inventory_item_sku", query.TypeString},
	"name":       {"i.inventory_item_name", query.TypeString},
	"is_active":  {"i.inventory_item_is_active", query.TypeBool},
	"unit_id":    {"i.inventory_item_unit_id::text", query.TypeString},
	"tax_rate_id": {"i.inventory_item_tax_rate_id::text", query.TypeString},
	"created_at": {"i.inventory_item_created_at", query.TypeDate},
	"updated_at": {"i.inventory_item_updated_at", query.TypeDate},
}

func (resolver) Resolve(key string) (string, query.DataType, bool) {
	if s, ok := systemFields[key]; ok {
		return s.expr, s.dt, true
	}
	if k, ok := strings.CutPrefix(key, "cf:"); ok && validCustomKey.MatchString(k) {
		return "i.inventory_item_custom_fields->>'" + k + "'", query.TypeString, true
	}
	return "", "", false
}

var _ query.FieldResolver = resolver{}
```

(Table alias `i` = `inventory_item`. Sorting stays on the default `created_at`/`updated_at`; `record_number` is not applicable here.)

- [ ] **Step 5: Run to verify it passes** — Run: `go test ./inventory/ -v` (PASS or SKIP if no DB). Also `go build ./...`.

- [ ] **Step 6: Commit** — `git commit -m "feat(inventory): add item store (CRUD + keyset search) and field resolver"`.

### Task 2.3: Inventory HTTP handlers + routes

**Files:**
- Create: `controllers/inventory.go`
- Modify: `main.go` (register routes near `main.go:451-476`)
- Test: `controllers/inventory_test.go`

**Interfaces:**
- Consumes: `inventory` store funcs; `middleware.GetUserFromContext`, `tenancy.PoolFromContext`, `authz.Check`, `controllers.writeJSON/fail`.
- Produces: `type InventoryOps struct{}`; handlers `Create`, `Get`, `List` (search), `Update`, `Delete` with signature `func (h *InventoryOps) X(w http.ResponseWriter, r *http.Request)`. Routes: `POST /api/tenant/inventory/items/search`, `GET|POST /api/tenant/inventory/items`, `GET|PATCH|DELETE /api/tenant/inventory/items/{uuid}`.

- [ ] **Step 1: Write the failing test** — a handler-level test asserting `Create` returns 403 without `inventory_item:create` and 201 with it (mirror the auth-test shape used in `controllers/` for CRM; use the existing test helpers/fixtures).

- [ ] **Step 2: Run to verify it fails** — Run: `go test ./controllers/ -run Inventory -v` → FAIL.

- [ ] **Step 3: Implement `controllers/inventory.go`** — mirror `controllers/crm.go` handler bodies, substituting: auth helper does `authz.Check(ctx, pool, identityID, authz.ResourceInventoryItem, action)`; map the employee id via the same identity→employee lookup the CRM store uses (`employeeIDByIdentity`); on store `ErrRecordNotFound` return 404. Single-record `Get/Update/Delete` are master-data (not owner-scoped), so no per-record IDOR scope check is required beyond the resource permission — document this in a comment (inventory is tenant-global reference data, like lookups).

- [ ] **Step 4: Register routes in `main.go`** — add, alongside the CRM routes:

```go
inv := controllers.NewInventoryOps()
mux.Handle("POST /api/tenant/inventory/items/search", tenantChain(inv.List))
mux.Handle("GET /api/tenant/inventory/items", tenantChain(inv.List))
mux.Handle("POST /api/tenant/inventory/items", tenantChain(inv.Create))
mux.Handle("GET /api/tenant/inventory/items/{uuid}", tenantChain(inv.Get))
mux.Handle("PATCH /api/tenant/inventory/items/{uuid}", tenantChain(inv.Update))
mux.Handle("DELETE /api/tenant/inventory/items/{uuid}", tenantChain(inv.Delete))
```

- [ ] **Step 5: Run to verify it passes** — Run: `go build ./... && go test ./controllers/ -run Inventory -v` → PASS.

- [ ] **Step 6: Commit** — `git commit -m "feat(inventory): add item HTTP handlers and routes"`.

---

## Phase 3 — Sales Order pure domain logic (TDD-first)

Spec §8, §9, AD-7. Pure functions, table-driven tests. No DB.

### Task 3.1: Line + header money math

**Files:**
- Create: `salesorder/calc.go`, `salesorder/calc_test.go`

**Interfaces:**
- Produces: `type LineInput struct { Quantity, UnitPrice, DiscountPercent, TaxPercent float64 }`; `type LineMoney struct { Subtotal, Discount, Tax, Total float64 }`; `func ComputeLine(in LineInput) LineMoney`; `type HeaderMoney struct { Subtotal, DiscountTotal, TaxTotal, GrandTotal float64 }`; `func ComputeHeader(lines []LineMoney, shipping, adjustment float64) HeaderMoney`. All amounts rounded to 2 decimals (banker's-agnostic `math.Round(x*100)/100`).

- [ ] **Step 1: Write the failing test**

```go
// salesorder/calc_test.go
package salesorder

import "testing"

func TestComputeLine(t *testing.T) {
	tests := []struct {
		name string
		in   LineInput
		want LineMoney
	}{
		{"qty*price", LineInput{Quantity: 25.5, UnitPrice: 42.00}, LineMoney{1071.00, 0, 0, 1071.00}},
		{"5pct discount", LineInput{Quantity: 10, UnitPrice: 100, DiscountPercent: 5}, LineMoney{1000, 50, 0, 950}},
		{"discount+tax", LineInput{Quantity: 10, UnitPrice: 100, DiscountPercent: 5, TaxPercent: 8.25}, LineMoney{1000, 50, 78.38, 1028.38}},
		{"zero qty", LineInput{Quantity: 0, UnitPrice: 100}, LineMoney{0, 0, 0, 0}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ComputeLine(tt.in)
			if got != tt.want {
				t.Fatalf("ComputeLine(%+v) = %+v, want %+v", tt.in, got, tt.want)
			}
		})
	}
}

func TestComputeHeader(t *testing.T) {
	lines := []LineMoney{{1000, 50, 78.38, 1028.38}, {300, 0, 0, 300}}
	got := ComputeHeader(lines, 150.00, 0)
	want := HeaderMoney{Subtotal: 1300, DiscountTotal: 50, TaxTotal: 78.38, GrandTotal: 1478.38}
	if got != want {
		t.Fatalf("ComputeHeader = %+v, want %+v", got, want)
	}
}
```

- [ ] **Step 2: Run to verify it fails** — Run: `go test ./salesorder/ -run Compute -v` → FAIL.

- [ ] **Step 3: Implement `salesorder/calc.go`**

```go
package salesorder

import "math"

func round2(x float64) float64 { return math.Round(x*100) / 100 }

type LineInput struct {
	Quantity, UnitPrice, DiscountPercent, TaxPercent float64
}
type LineMoney struct{ Subtotal, Discount, Tax, Total float64 }

// ComputeLine derives a line's stored money (spec §9).
func ComputeLine(in LineInput) LineMoney {
	sub := round2(in.Quantity * in.UnitPrice)
	disc := round2(sub * in.DiscountPercent / 100)
	tax := round2((sub - disc) * in.TaxPercent / 100)
	return LineMoney{Subtotal: sub, Discount: disc, Tax: tax, Total: round2(sub - disc + tax)}
}

type HeaderMoney struct{ Subtotal, DiscountTotal, TaxTotal, GrandTotal float64 }

// ComputeHeader sums line money and applies shipping + adjustment (spec §9).
func ComputeHeader(lines []LineMoney, shipping, adjustment float64) HeaderMoney {
	var h HeaderMoney
	for _, l := range lines {
		h.Subtotal += l.Subtotal
		h.DiscountTotal += l.Discount
		h.TaxTotal += l.Tax
	}
	h.Subtotal = round2(h.Subtotal)
	h.DiscountTotal = round2(h.DiscountTotal)
	h.TaxTotal = round2(h.TaxTotal)
	h.GrandTotal = round2(h.Subtotal - h.DiscountTotal + h.TaxTotal + shipping + adjustment)
	return h
}
```

- [ ] **Step 4: Run to verify it passes** — Run: `go test ./salesorder/ -run Compute -v` → PASS.

- [ ] **Step 5: Commit** — `git commit -m "feat(salesorder): add line + header money calculation"`.

### Task 3.2: Status transition map

**Files:**
- Create: `salesorder/transitions.go`, `salesorder/transitions_test.go`

**Interfaces:**
- Produces: `func CanTransition(fromCode, toCode string) bool`; `var ErrInvalidTransition = errors.New(...)`; `func ValidateTransition(fromCode, toCode string) error`. Codes are `lkp_record_status` codes for SORD: `DRFT,PAPV,APPV,OPEN,PART,FILL,CANC`.

- [ ] **Step 1: Write the failing test**

```go
// salesorder/transitions_test.go
package salesorder

import "testing"

func TestCanTransition(t *testing.T) {
	ok := [][2]string{{"DRFT", "PAPV"}, {"PAPV", "APPV"}, {"APPV", "OPEN"}, {"OPEN", "PART"}, {"OPEN", "FILL"}, {"PART", "FILL"}, {"OPEN", "CANC"}, {"PAPV", "DRFT"}}
	bad := [][2]string{{"FILL", "OPEN"}, {"CANC", "DRFT"}, {"DRFT", "FILL"}, {"APPV", "DRFT"}, {"FILL", "CANC"}}
	for _, p := range ok {
		if !CanTransition(p[0], p[1]) {
			t.Errorf("expected %s->%s allowed", p[0], p[1])
		}
	}
	for _, p := range bad {
		if CanTransition(p[0], p[1]) {
			t.Errorf("expected %s->%s denied", p[0], p[1])
		}
	}
}
```

- [ ] **Step 2: Run to verify it fails** — Run: `go test ./salesorder/ -run Transition -v` → FAIL.

- [ ] **Step 3: Implement `salesorder/transitions.go`**

```go
package salesorder

import "errors"

// ErrInvalidTransition is returned when a status change is not permitted.
var ErrInvalidTransition = errors.New("invalid sales order status transition")

// allowedTransitions maps a status code to the set of codes reachable from it
// (spec §8). Terminal states (FILL, CANC) map to an empty set.
var allowedTransitions = map[string]map[string]bool{
	"DRFT": {"PAPV": true, "CANC": true},
	"PAPV": {"APPV": true, "DRFT": true, "CANC": true},
	"APPV": {"OPEN": true, "CANC": true},
	"OPEN": {"PART": true, "FILL": true, "CANC": true},
	"PART": {"FILL": true, "CANC": true},
	"FILL": {},
	"CANC": {},
}

// CanTransition reports whether moving fromCode->toCode is allowed.
func CanTransition(fromCode, toCode string) bool {
	return allowedTransitions[fromCode][toCode]
}

// ValidateTransition returns ErrInvalidTransition when the move is not allowed.
func ValidateTransition(fromCode, toCode string) error {
	if !CanTransition(fromCode, toCode) {
		return ErrInvalidTransition
	}
	return nil
}
```

- [ ] **Step 4: Run to verify it passes** — Run: `go test ./salesorder/ -run Transition -v` → PASS.

- [ ] **Step 5: Commit** — `git commit -m "feat(salesorder): add status transition validation"`.

### Task 3.3: Order-number formatting

**Files:**
- Create: `salesorder/numbering.go`, `salesorder/numbering_test.go`

**Interfaces:**
- Produces: `func FormatNumber(serialID int64) string` → `"SORD-000001"`.

- [ ] **Step 1: Write the failing test**

```go
// salesorder/numbering_test.go
package salesorder

import "testing"

func TestFormatNumber(t *testing.T) {
	for in, want := range map[int64]string{1: "SORD-000001", 42: "SORD-000042", 1234567: "SORD-1234567"} {
		if got := FormatNumber(in); got != want {
			t.Errorf("FormatNumber(%d) = %s, want %s", in, got, want)
		}
	}
}
```

- [ ] **Step 2: Run to verify it fails** — Run: `go test ./salesorder/ -run FormatNumber -v` → FAIL.

- [ ] **Step 3: Implement `salesorder/numbering.go`**

```go
package salesorder

import "fmt"

// numberPrefix is the SORD record-type code (lkp_record_type.record_type_code).
const numberPrefix = "SORD"

// FormatNumber renders the human-readable document number from the row's serial
// PK, zero-padded to 6 digits (spec AD-7): SORD-000001.
func FormatNumber(serialID int64) string {
	return fmt.Sprintf("%s-%06d", numberPrefix, serialID)
}
```

- [ ] **Step 4: Run to verify it passes** — Run: `go test ./salesorder/ -run FormatNumber -v` → PASS.

- [ ] **Step 5: Commit** — `git commit -m "feat(salesorder): add order-number formatting"`.

---

## Phase 4 — Sales Order store + resolver

Spec §5, §9, §11. Transactional writes; reuse `query/` for listing.

### Task 4.1: Sales Order types

**Files:**
- Create: `salesorder/types.go`
- Test: `salesorder/types_test.go`

**Interfaces:**
- Produces: `type Order struct { ID string; Number string; Status string; CustomerID string; OrderDate time.Time; Subtotal, DiscountTotal, TaxTotal, ShippingCharge, Adjustment, GrandTotal float64; Items []Line; ... }`; `type Line struct { ID string; LineNumber int; InventoryItemID *string; SKU, ItemName, Description, UnitCode string; Quantity, UnitPrice, DiscountPercent, TaxPercent, LineSubtotal, LineDiscount, LineTax, LineTotal float64 }`; `type CreateOrderInput struct { CustomerUUID, PONumber, ReferenceNumber string; OrderDate, ExpectedDelivery *time.Time; PaymentTermsID, PriceLevelID, CurrencyID, SalesRepEmployeeID, OwnerEmployeeID *int; SalesTaxPercent float64; Memo, Notes, InternalNotes, TermsConditions string; ShipSameAsBilling bool; Billing, Shipping AddressInput; ShippingCharge, Adjustment float64; CustomFields map[string]any; Items []LineInput2 }`; `type LineInput2 struct { LineNumber int; InventoryItemUUID, Description string; Quantity, UnitPrice, DiscountPercent float64; TaxRateID, WarehouseID *int }`; `type AddressInput struct { CustomerName, Attention, AddrLine1, AddrLine2, SuiteUnit, City string; StateID, CountryID *int; Zip, Phone, Fax, Email string }`.

- [ ] **Step 1–5:** Write a JSON-key test (as in Task 2.1), implement `types.go`, run, commit (`feat(salesorder): add order/line/input types`).

### Task 4.2: Sales Order store — create (transactional, snapshots, totals, numbering)

**Files:**
- Create: `salesorder/store.go`
- Test: `salesorder/store_test.go` (integration, gated on `SALESORDER_TEST_DB_URL`)

**Interfaces:**
- Consumes: `ComputeLine`, `ComputeHeader`, `FormatNumber`; `salesorder` types; `workflow.ValidateCustomFields`.
- Produces: `func Create(ctx, pool, in CreateOrderInput, actorEmployeeID int) (*Order, error)`. Sentinels: `ErrNotFound`, `ClientError` (→400).

- [ ] **Step 1: Write the failing integration test**

```go
// salesorder/store_test.go  (excerpt)
func TestCreate_SnapshotsAndTotals(t *testing.T) {
	pool := testPool(t) // skips without SALESORDER_TEST_DB_URL
	ctx := context.Background()
	custUUID, itemUUID := seedCustomerAndItem(t, pool) // helper: inserts a customer + inventory_item, returns their UUIDs
	in := CreateOrderInput{
		CustomerUUID:    custUUID,
		SalesTaxPercent: 8.25,
		Items: []LineInput2{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 10, UnitPrice: 100, DiscountPercent: 5}},
	}
	o, err := Create(ctx, pool, in, 1)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if o.Number != "SORD-"+pad6(o) { /* number assigned */ }
	if o.Status != "DRFT" {
		t.Fatalf("new order must start DRFT, got %s", o.Status)
	}
	if o.Items[0].SKU == "" || o.Items[0].ItemName == "" {
		t.Fatalf("line item snapshot (sku/name) not populated")
	}
	// header tax % applied as line default (line had no tax_rate): tax = (1000-50)*8.25%
	if o.GrandTotal != 1028.38 {
		t.Fatalf("grand total = %v, want 1028.38", o.GrandTotal)
	}
}
```

- [ ] **Step 2: Run to verify it fails** — Run: `go test ./salesorder/ -run TestCreate_ -v` → FAIL/SKIP.

- [ ] **Step 3: Implement `Create` in `salesorder/store.go`**

Mirror `crmstore/relational_store.go` create+numbering. Algorithm, all inside one `pool.Begin()` transaction (rollback on any error):
1. Resolve `customer` internal id + billing/shipping snapshot columns from `sales_order_customer_id`'s `customer` row (unless the caller overrides via `Billing`/`Shipping`). Copy `customer_bill_*`/`customer_ship_*` (or primary) into the `sales_order_bill_*`/`sales_order_ship_*` snapshot columns. If `ShipSameAsBilling`, copy billing→shipping.
2. Resolve `record_type` = SORD id (`SELECT record_type_id FROM lkp_record_type WHERE record_type_code='SORD'`) and initial `sales_order_status` = DRFT id (`... FROM lkp_record_status WHERE record_status_code='DRFT' AND record_status_record_type=<SORD id>`).
3. For each item: resolve `inventory_item` by UUID → snapshot `sku`, `item_name`, `description`, `unit_id`, `unit_code`, and default `unit_price`/`tax_percent` from the item (or from the line override). Determine per-line `tax_percent`: line's `tax_rate_id` → `lkp_tax_rate.tax_rate_percent`; else header `SalesTaxPercent`. Compute `ComputeLine(...)`.
4. `ComputeHeader(lines, ShippingCharge, Adjustment)`.
5. Validate `CustomFields` via `workflow.ValidateCustomFields` against the `sales_order` workflow field defs (return `ClientError` on failure).
6. `INSERT INTO sales_order (...) RETURNING sales_order_id, sales_order_uuid`. Then `UPDATE sales_order SET sales_order_number = $1 WHERE sales_order_id = $2` with `FormatNumber(id)`.
7. Bulk-insert `sales_order_item` rows (snapshots + line money).
8. Insert a `sales_order_history` row: `action='create'`, `to_status_id = DRFT`, `actor_employee_id`.
9. Commit; return the assembled `Order` (re-select or build in-memory).

- [ ] **Step 4: Run to verify it passes** — Run: `go test ./salesorder/ -run TestCreate_ -v` → PASS (or SKIP without DB).

- [ ] **Step 5: Commit** — `git commit -m "feat(salesorder): transactional create with snapshots, totals, numbering"`.

### Task 4.3: Sales Order store — get / update / soft-delete

**Files:** Modify `salesorder/store.go`; Test `salesorder/store_test.go`.

**Interfaces:**
- Produces: `func Get(ctx, pool, uuid string) (*Order, error)` (loads header + lines; `ErrNotFound`); `func Update(ctx, pool, uuid string, in UpdateOrderInput, actorEmployeeID int) (*Order, error)` (recompute totals; replace lines in a tx; history `action='update'`); `func SoftDelete(ctx, pool, uuid string, actorEmployeeID int) error` (set `sales_order_deleted_at/by`); `type UpdateOrderInput` (same shape as create minus customer). `Update` must reject edits when status is terminal (`FILL`/`CANC`) with `ClientError`.

- [ ] **Steps:** Write integration tests (`get returns lines`, `update recomputes grand_total`, `update on FILL is rejected`, `soft-deleted order not returned by Get`), run→fail, implement (mirror `relationalStore` get/update/delete + `scanRecord`), run→pass, commit (`feat(salesorder): add get/update/soft-delete`).

### Task 4.4: Sales Order resolver (FieldResolver + SortResolver + SearchResolver)

**Files:**
- Create: `salesorder/resolver.go`, `salesorder/resolver_test.go`

**Interfaces:**
- Produces: `type resolver struct{}` implementing all three `query` interfaces. Table alias `so`.

- [ ] **Step 1: Write the failing test**

```go
// salesorder/resolver_test.go
package salesorder

import (
	"testing"

	"stonesuite-backend/query"
)

func TestResolver_Fields(t *testing.T) {
	r := resolver{}
	for _, k := range []string{"id", "document_number", "customer_id", "status", "order_date", "grand_total", "created_at", "cf:install_required"} {
		if _, _, ok := r.Resolve(k); !ok {
			t.Errorf("expected %q resolvable", k)
		}
	}
	if _, _, ok := r.Resolve("memo"); ok {
		t.Error("memo must not be filterable (not whitelisted)")
	}
}

func TestResolver_SortAndSearch(t *testing.T) {
	r := resolver{}
	if _, _, ok := r.SortExpr("grand_total"); !ok {
		t.Error("grand_total must be sortable")
	}
	if _, _, ok := r.SortExpr("memo"); ok {
		t.Error("memo must not be sortable")
	}
	if r.SearchPredicate("$3") == "" {
		t.Error("SearchPredicate must produce a fragment")
	}
	var _ query.FieldResolver = r
	var _ query.SortResolver = r
	var _ query.SearchResolver = r
}
```

- [ ] **Step 2: Run to verify it fails** — Run: `go test ./salesorder/ -run TestResolver -v` → FAIL.

- [ ] **Step 3: Implement `salesorder/resolver.go`** using the mapping table in spec §11.3 for `Resolve`, the whitelist in §11.6 for `SortExpr` (return the **raw** column + correct `DataType`: `grand_total`→`(so.sales_order_grand_total, query.TypeNumber)`, `order_date`→`(so.sales_order_date, query.TypeDate)`, `status`→`(so.sales_order_status, query.TypeNumber)`, `customer_id`→`(so.sales_order_customer_id, query.TypeNumber)`, `document_number`/`record_number`→`(so.sales_order_number, query.TypeString)`, `created_at`/`updated_at`→date), and the §11.5 SQL for `SearchPredicate(ph)`:

```go
func (resolver) SearchPredicate(ph string) string {
	return "(" +
		"so.sales_order_number ILIKE '%'||" + ph + "||'%'" +
		" OR so.sales_order_po_number ILIKE '%'||" + ph + "||'%'" +
		" OR so.sales_order_memo ILIKE '%'||" + ph + "||'%'" +
		" OR so.sales_order_notes ILIKE '%'||" + ph + "||'%'" +
		" OR so.sales_order_bill_customer_name ILIKE '%'||" + ph + "||'%'" +
		" OR EXISTS (SELECT 1 FROM sales_order_item soi WHERE soi.sales_order_id = so.sales_order_id" +
		"   AND (soi.sku ILIKE '%'||" + ph + "||'%' OR soi.item_name ILIKE '%'||" + ph + "||'%'))" +
		" OR EXISTS (SELECT 1 FROM customer c WHERE c.customer_id = so.sales_order_customer_id" +
		"   AND (c.customer_name ILIKE '%'||" + ph + "||'%' OR c.customer_doc_num ILIKE '%'||" + ph + "||'%'))" +
		")"
}
```

- [ ] **Step 4: Run to verify it passes** — Run: `go test ./salesorder/ -run TestResolver -v` → PASS.

- [ ] **Step 5: Commit** — `git commit -m "feat(salesorder): add field/sort/search resolver"`.

### Task 4.5: Sales Order store — `Search` (scope + filter + keyset)

**Files:** Modify `salesorder/store.go`; Test `salesorder/search_test.go`.

**Interfaces:**
- Produces: `func Search(ctx, pool, scope, actorIdentityID string, req query.Request) (Page, error)`; `type Page struct { Records []Order; NextCursor string; HasMore bool }`. Scope clause built like `relationalStore.SearchRecords`: base `WHERE so.sales_order_deleted_at IS NULL` (+ `AND so.sales_order_owner_id = $n` for `own`/`team` scope, mapping identity→employee), then `query.Build(req, resolver{}, startIdx)` appended (`AND Where`, `AND Keyset`, `ORDER BY`, `LIMIT EffLimit+1`). Trim the extra row → `HasMore`; mint `NextCursor` via `query.NextCursor(lastID, Built.Sort.Field, lastSortValue)`.

- [ ] **Steps:** Write integration tests — (a) filter `status in [OPEN,PART]` returns only those; (b) `grand_total` sort works; (c) `search:"SORD-0004"` matches by number; (d) unknown filter field → `*query.InvalidFilterError`; (e) scope `own` returns only caller-owned; (f) `filter × scope` stays ANDed. Run→fail, implement mirroring `crmstore/relational_store.go:403-...`, run→pass. Then run the `filter-invariant-checker` agent on `salesorder/` + `query/` diffs. Commit (`feat(salesorder): add keyset search with scope + filter + sort + global search`).

---

## Phase 5 — Sales Order HTTP layer (CRUD + listing)

Spec §10, §11.1, §11.7. Mirror `controllers/crm.go`. Follow the **new-tenant-endpoint** skill.

### Task 5.1: `SalesOrderOps` auth helpers + CRUD handlers

**Files:**
- Create: `controllers/salesorder.go`
- Modify: `main.go`
- Test: `controllers/salesorder_test.go`

**Interfaces:**
- Consumes: `salesorder` store funcs; `authz.ResourceSalesOrder`; `middleware.GetUserFromContext`, `tenancy.PoolFromContext`, `authz.Check`; `controllers.writeJSON/fail`; the shared IDOR guard (`recordInScope` / an `authSOByUUID` helper mirroring `authCRMByRecordID`).
- Produces: `type SalesOrderOps struct{}`; handlers `List`, `Search`, `Get`, `Create`, `Update`, `Delete`, `Transition`, `Inventory`, `Audit`.

- [ ] **Step 1: Write the failing tests** — mirror the CRM controller tests: `Create` 403 without `sales_order:create`, 201 with; `Get` on another owner's order with `own` scope → **404** and logs `idor_denied`; `Search` returns `{success, records, nextCursor, hasMore}`.

- [ ] **Step 2: Run to verify it fails** — Run: `go test ./controllers/ -run SalesOrder -v` → FAIL.

- [ ] **Step 3: Implement `controllers/salesorder.go`**

Mirror these exact CRM handlers with substitutions (`workflowKey`→fixed `sales_order`; `crmstore.Store`→`salesorder` funcs; `authz.ResourceSalesOrder`):
- Auth helper `authSO(w, r, action) (pool, identityID, scope, ok)` — mirror `authCRM` (`crm.go:110-135`): `authz.Check(ctx, pool, payload.ID, authz.ResourceSalesOrder, action)`.
- Single-record helper `authSOByUUID(w, r, uuid, action) (pool, identityID, scope, ok)` — mirror `authCRMByRecordID` (`crm.go:207-235`): load the order, if `scope != all` run `recordInScope(ctx, pool, scope, identityID, order.OwnerUserID, "")`; on denial `logSecurityEvent(r, "idor_denied", ...)` + `fail(w, 404, "Record not found.")`.
- `Create` (`crm.go:368-395` shape): decode `salesorder.CreateOrderInput`, call `salesorder.Create`, `auditSO(r, pool, employeeID, "create", uuid, nil, order)`, 201 `{success, salesOrder}`.
- `Get`/`Update`/`Delete`/`List`/`Search` mirror `crm.go:400-469, 311-358`. `Search` returns `map[string]any{"success":true,"scope":scope,"records":page.Records,"nextCursor":page.NextCursor,"hasMore":page.HasMore}`.
- Map store errors via a `soFail` helper mirroring `crmFail` (`crm.go:240-262`): `ErrNotFound`→404, `ClientError`/`ErrInvalidTransition`→400/409, `*query.InvalidFilterError`→400, else 500.

- [ ] **Step 4: Register routes in `main.go`**

```go
so := controllers.NewSalesOrderOps()
mux.Handle("GET /api/tenant/sales-orders", tenantChain(so.List))
mux.Handle("POST /api/tenant/sales-orders/search", tenantChain(so.Search))
mux.Handle("POST /api/tenant/sales-orders", tenantChain(so.Create))
mux.Handle("GET /api/tenant/sales-orders/{uuid}", tenantChain(so.Get))
mux.Handle("PATCH /api/tenant/sales-orders/{uuid}", tenantChain(so.Update))
mux.Handle("DELETE /api/tenant/sales-orders/{uuid}", tenantChain(so.Delete))
mux.Handle("POST /api/tenant/sales-orders/{uuid}/transition", tenantChain(so.Transition))
mux.Handle("GET /api/tenant/sales-orders/{uuid}/inventory", tenantChain(so.Inventory))
mux.Handle("GET /api/tenant/sales-orders/{uuid}/audit", tenantChain(so.Audit))
```

- [ ] **Step 5: Run to verify it passes** — Run: `go build ./... && go test ./controllers/ -run SalesOrder -v` → PASS.

- [ ] **Step 6: Commit** — `git commit -m "feat(salesorder): add CRUD + list/search HTTP handlers and routes"`.

---

## Phase 6 — Transitions, allocation, inventory tab, audit, attachments

Spec §8, §9 (inventory), §7, AD-4. Depends on Phases 4–5.

### Task 6.1: Transition endpoint + history + audit helper

**Files:** Modify `salesorder/store.go` (`Transition`), `controllers/salesorder.go` (`Transition`); Create `controllers/salesorder_audit.go` (mirror `crm_audit.go`).

**Interfaces:**
- Produces: `func Transition(ctx, pool, uuid, toStatusCode string, actorEmployeeID int) (*Order, error)` — loads current status code, `ValidateTransition(cur, toStatusCode)` (→`ErrInvalidTransition`→409), row-locks the order (`SELECT ... FOR UPDATE`), updates `sales_order_status`, writes `sales_order_history` (`action='transition'`, from/to status ids), and on `CANC` releases open allocations (`UPDATE inventory_allocation SET allocation_status='released' WHERE sales_order_id=$1 AND allocation_status IN ('reserved','partially_fulfilled')`). `auditSO` writes `audit_logs` with `actor_user_id=NULL` + `details.employee_id` (spec §10 note) plus the typed `sales_order_history` row.

- [ ] **Steps:** Write integration test (`DRFT→PAPV ok`, `FILL→OPEN 409`, `cancel releases allocations`), run→fail, implement, run→pass. Handler `Transition` decodes `{toStatusCode}`, calls store, `soFail` maps `ErrInvalidTransition`→409, returns `{success, salesOrder}`. Commit (`feat(salesorder): add status transitions with history + allocation release`).

### Task 6.2: Inventory allocation + inventory-tab aggregation

**Files:** Create `salesorder/allocation.go`, `salesorder/allocation_test.go`; add `Inventory` handler.

**Interfaces:**
- Produces: `func Reserve(ctx, tx, itemID, warehouseID, orderID, orderItemID int, qty float64) error` (row-locks `inventory_stock`, checks `qty <= available`, inserts `inventory_allocation` status `reserved`); `func InventoryForOrder(ctx, pool, uuid string) ([]InventoryRow, error)` where `type InventoryRow struct { ItemID, SKU string; OnHand, Available, Allocated, SalesOrderQuantity float64 }`. Available/allocated derived per spec §9 (no stored cache).

- [ ] **Steps:** Write integration test — `InventoryForOrder` returns `available = on_hand - Σ open allocations` and `salesOrderQuantity = Σ line qty for that item`. Run→fail, implement the aggregation SQL (join `sales_order_item` + `inventory_stock` + `LEFT JOIN` allocations filtered to open statuses via `idx_alloc_open`), run→pass. `Inventory` handler calls `authSOByUUID(...read...)` then `InventoryForOrder`, returns `{success, items}`. Commit (`feat(salesorder): add inventory allocation + inventory-tab endpoint`).

### Task 6.3: Attachments wiring (drawings / uploaded files)

**Files:** Modify `workflow/attachments.go` (`RecordKeyForAttachment`) if needed; Test in `workflow/attachments_test.go` or `controllers/salesorder_test.go`.

**Interfaces:**
- Consumes: existing generic `/api/tenant/records/{uuid}/attachments/*` handlers + `workflow.RecordKeyForAttachment`.

- [ ] **Steps:** Write a test asserting `RecordKeyForAttachment` resolves a `sales_order` UUID to a usable workflow key (the existing fallback at `workflow/attachments.go:139-143` returns `strings.ToLower(typeCode)` for non-CRM types — verify it resolves a Sales Order row; add a lookup branch that finds the order by `sales_order_uuid` and returns `"sales_order"` if the generic fallback doesn't already cover it). Run→fail (if a branch is needed), implement, run→pass. No new endpoint — the shared attachment routes work once resolution succeeds. Commit (`feat(salesorder): wire attachments to sales_order records`).

---

## Phase 7 — Verification & security review

### Task 7.1: Full build, vet, test, and agent review

- [ ] **Step 1: Full test suite** — Run: `go build ./... && go vet ./... && go test ./...` → all PASS.
- [ ] **Step 2: filter-invariant-checker** — dispatch the agent on `query/`, `salesorder/resolver.go`, `salesorder/store.go` diffs. Expected: `No filter-engine invariant violations found...`. Fix any CRITICAL findings.
- [ ] **Step 3: tenancy-security-reviewer** — dispatch on `controllers/salesorder.go`, `controllers/inventory.go`, `salesorder/store.go`, `inventory/store.go`. Expected: `No tenancy/RBAC/IDOR issues found...`. Fix any CRITICAL/HIGH findings (esp. 404-not-403 on scope denial, mutation permission-before-write, scope in list SQL).
- [ ] **Step 4: migration-auditor** — dispatch on `database/migrations/tenant/schema.sql` diff. Expected: `No schema idempotency/safety issues found...`.
- [ ] **Step 5: Commit any fixes** — `git commit -m "fix(salesorder): address security/invariant review findings"` (only if fixes were needed).

---

## Self-Review (completed against the spec)

**Spec coverage:** §5 tables → Phase 1; §6/§11.9 indexes → Tasks 1.1/1.2; §7 FKs → schema; §8 transitions → Task 3.2/6.1; §9 money+inventory → Task 3.1/6.2; §10 API → Phases 5–6; §11.1 endpoints → Task 5.1; §11.2 pagination → reused (query engine, Task 4.5); §11.3 resolver → Task 4.4; §11.4 filtering → Task 4.4/4.5; §11.5 search → Task 0.2/4.4; §11.6 sorting → Task 0.1/4.4; §11.7 response → Task 4.5/5.1; §11.8 saved filters → out of scope (no task, by decision); §12 validation → Tasks 4.2/4.3 (custom-field + transition + range checks); §13 performance → indexes + keyset (Phases 1,4); §14 scalability → schema design (Phase 1); §15 impl map → this plan; §16 decisions → applied. RBAC (`sales_order` pre-seeded; `inventory_item` added Task 1.3).

**Type consistency:** `ComputeLine`/`ComputeHeader`/`FormatNumber`/`CanTransition`/`ValidateTransition` names are used consistently across Phases 3–6. `Order`/`Line`/`CreateOrderInput`/`Page` defined in Phase 4 and consumed unchanged in Phases 5–6. `SortResolver.SortExpr(key)(expr,dt,ok)` and `SearchResolver.SearchPredicate(placeholder)` defined in Phase 0 and implemented in Task 4.4 with matching signatures.

**Placeholders:** none — pure-function tasks carry full code; DB/HTTP tasks carry integration tests + exact CRM mirror references (file:line) because reproducing unverified verbatim source would be less accurate than pointing at the real files.
