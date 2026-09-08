# Global Search — Design

**Status:** implemented (this doc retro-documents PR #174 `da5226e` and the
coverage/wire-up work on branch `feat/global-search-coverage` +
StoneSuite-WebUI `feat/global-search-wireup`).

## Problem

A user should be able to type any term into the top-nav bar and see matches
across *everything* they can access — clients, every document module, inventory,
finance, people. Before this work: the backend `GET /api/tenant/search` endpoint
existed (PR #174, 26 modules) but the frontend `GlobalSearch.tsx` never called it
(it fanned out client-side over only lead/prospect/customer), and several entity
types plus the DesignV1 CRM path were missing backend-side.

## Architecture

### Backend — `globalsearch/` package

`GET /api/tenant/search?q=<term>&modules=<csv>&limit=<n>` → `controllers.GlobalSearchOps.Search`.

- **`q`** trimmed; `< 2` chars → 400 (`minSearchTermLen`).
- **`modules`** optional CSV allowlist of registry keys (convenience filter, not
  a security boundary).
- **`limit`** optional per-group cap, clamped to `[1, MaxPerGroupCap=50]`,
  default `PerGroupCap=6`. The dropdown omits it; the results page sends 50.

**Fan-out** (`globalsearch/search.go`): one goroutine per registered provider,
`fanoutTimeout = 5s` ceiling. Each goroutine:
1. `authz.Check(ctx, pool, identityID, provider.Resource, ActionRead)` — on deny
   or error the group is **silently omitted** (not a security event — denial is
   expected in a UI feature).
2. calls the provider's `SearchFunc`, which calls that module's own
   `Search(...)` with `query.Request{Search: term, Limit: cap}` — the fan-out
   runs **no SQL of its own**; every module's RBAC scope, field whitelist, and
   parameterized ILIKE stay the single source of truth.
3. the fan-out stamps `Result.Domain` / `Result.Module` (the frontend route
   segments) centrally from the `Provider`, mirroring
   `controllers/dashboard_recent.go`'s `fetchAllRecent`.

**Response:** `{ success, query, groups: { <type>: { results: Result[], hasMore } } }`
where `Result = { type, id, number?, displayName, subtitle?, updatedAt, domain, module }`.

**Registry** (`globalsearch/registry.go` + `providers_*.go`): `var _ =
addProvider(Provider{Key, Resource, Domain, Module, Search})` at package init.
`registry_test.go` pins the expected key list — a forgotten registration fails
loudly. `Module` is the **frontend** route segment and is not always `Key`
(`fabrication_job` → `sales/installation`, `inventory_item` → `inventory/item`,
`cash_transfer` → `finance/journal-entries`).

**Coverage (28 providers):** customer, lead, prospect, **crm_activity**,
**customer_note**, quote, estimate, sales_order, invoice, payment, credit_memo,
refund, fabrication_job, vendor, requisition, purchase_order, item_receipt,
vendor_bill, vendor_payment, vendor_credit, expense, inventory_item,
inventory_unit, inventory_adjustment, inventory_transfer, inventory_count,
chart_of_account, cash_transfer, **user**.

**Term handling** (`query/builder.go`): the term is split on whitespace; each
token is stripped of LIKE metacharacters (`query.StripLikeMeta` — the
`SearchPredicate` fragments carry no `ESCAPE` clause) and bound as its own `$n`;
every token must match (AND), the resolver's fragment ORs across its columns for
one token. "acme granite" matches a row containing both words in any searched
column. `filter ⨯ scope = AND` is preserved (each token is one more AND-ed
predicate).

**DesignV1 CRM fix:** `workflow.recordResolver` now implements
`query.SearchResolver` (`record_number` + fixed `core_fields` keys). Without it,
`ListRecordsFiltered` 400s on any free-text term, which made the CRM groups
silently vanish from global search on non-DesignV2 tenants.

**Hand-rolled searches** (not through a doc-module `Search`): `userstore.Search`
(name/email, `all`-scoped) and `crmstore.SearchActivity` / `SearchCustomerNotes`
(joined to `customer`, owner-scoped, route to the parent customer's detail page).
Both use `query.StripLikeMeta` + `$n` params.

**Rate limit:** `GET /api/tenant/search` runs on `searchChain` — a per-user
limiter (2 req/s, burst 6) inside the generic per-tenant one. The fan-out is ~28
leading-wildcard ILIKE scans in parallel, far heavier than CRUD.

### Frontend — `StoneSuite-WebUI`

- `src/services/globalSearchService.ts` — `GET /tenant/search`, typed response.
- `src/lib/searchEntity.ts` — `entityLabel` (curated plurals), `compareEntityTypes`
  (curated order), `hitRoute` (`recordRoute(domain, module, id)` + a list-page
  fallback for types with no detail route — currently just `user`).
- `src/components/GlobalSearch.tsx` — rewritten: one `globalSearchService.search`
  call, grouped dropdown (≤6/type), per-group "See all …" and a global "See all
  results" row, flat keyboard-nav index across groups, ⌘K, 300ms debounce, ARIA
  combobox. Mounted 3× unchanged in `MainLayout.tsx`.
- `src/pages/search/SearchResultsPage.tsx` at `/search?q=&type=` — type rail +
  the active group's results (up to 50), each row → `hitRoute`.

## Deliberately out of scope

- **Trigram / `tsvector` index on business tables.** Every `SearchPredicate` is a
  leading-wildcard ILIKE (un-indexable seq scan), bounded by the 5s timeout.
  Fine at current tenant sizes; add `pg_trgm` GIN via the `add-migration` skill
  if latency regresses. (The `ai/` RAG `tsvector` is unrelated — semantic Q&A.)
- **Cross-group relevance ranking / one merged list.** Groups stay newest-first.
- **True per-type pagination past 50** on the results page. The dropdown's
  per-group `hasMore` + a "narrow your search" hint is the current ceiling; a
  follow-up can page each type through its own `POST /api/tenant/<mod>/search`.
- **Generic custom-workflow `record` provider.** The `SearchResolver` groundwork
  is in place, but the frontend has no per-record detail page for admin-defined
  workflows (`WorkflowPlaceholderPage` only) — a hit would navigate nowhere.
- **Journal entries as a distinct entity.** The frontend "Journal Entries" page
  is backed by the `cash_transfer` module; `journal/` is internal GL plumbing
  with no RBAC resource or list endpoint.
- **Inventory master (warehouses, bins, bundles).** No existing search fn,
  list-only routes, small tables. SKUs + slabs (the valuable inventory search)
  are covered.
- **Users / customer notes / CRM activity in the results-page pagination** beyond
  the first page — the dropdown surfaces them; deep paging is a follow-up.
