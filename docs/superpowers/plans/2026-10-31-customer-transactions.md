# Customer Transactions Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans. Planned by Opus; implement with Sonnet in a separate session. ONE PR per repo, raised only after everything is implemented and verified.

**Goal:** Customer detail page gets a working Transactions tab listing every transaction for that customer (plus live AR summary), and customer names link to the customer page everywhere they appear.

**Repos:**
- Backend: `/Users/prasannakumarnagaboyina/pkumardev/StoneSuite-Backend` (Go; read its CLAUDE.md first)
- Frontend: `/Users/prasannakumarnagaboyina/pkumardev/StoneSuite-WebUI` (the live frontend; `StoneSuite/frontend` is the stale monorepo, ignore it)

**Decisions already made (planner defaults, user may override):**
1. There is no vendor-side or other per-party transactions screen to copy (vendor work is owned by another teammate and is not started). Do not look for one. The only reference is the existing My Transactions page (backend `mytransactions/`, WebUI `pages/transactions/*`), plus the AR summary tiles specified below. If a vendor equivalent lands later, it should be modelled on this customer tab, not the other way round.
2. Own-scoped users see summary figures only for records they can see, with a "Based on records you can see" note. Never widen a grant.
3. Numbered pages with a total (My Transactions / auditstore precedent), not keyset.
4. Leave the stale stored balance fields on the Overview tab untouched. Follow-up to retire them.
5. No parent/child account rollup. Direct customer only.

---

## 1. Findings

- Live frontend is `StoneSuite-WebUI` (remote `Skookum-Infotech/StoneSuite-WebUI`).
- Customer list `pages/crm/customer/CustomerListPage.tsx` is done. `CustomerDetailPage.tsx` (359 lines, already over the cap) has tabs overview, transactions, files, portal, audit, activity; tab is local state seeded from `location.state.initialTab`.
- Transactions tab is a stub: `components/crm/CrmSubTabsPanel.tsx:159` `TransactionsContent()` renders "No transactions yet."
- Closest pattern: My Transactions. Backend `mytransactions/` (UNION ALL registry, `grantFor`, `ownerPredicate`, page/offset with `COUNT(*) OVER()`), `controllers/mytransactions.go`, `main.go:766-768`. Frontend `pages/transactions/*`, `services/myTransactionsService.ts`, `types/myTransactions.ts`, `lib/myTransactions.ts`, `components/TransactionsPagination.tsx`.
- Customer sub-resource auth pattern: `controllers/customer_note.go` calls `authCRMByRecordID(..., ActionRead)`; routes use `/api/tenant/crm/customer/records/{id}/...` (`main.go:1130`). `authCRMByRecordID` is at `controllers/crm.go:183` (404 for missing permission AND out-of-scope; logs events).
- AR definitions to reuse: `invoice/aging.go` (outstanding = SENT/PART/ODUE and balance_due > 0); `automation/ar_source.go:131-150` (available credit: non-VOID payments `unapplied - refunded - credited` + APPV credit memos `unapplied - refunded`, clamped at 0); `invoice/revenue.go` (billed = SENT/PART/ODUE/PAID).
- Tenant "today": `automation.TenantToday(now, tz, fallback)` + `companyprofile.Get(...).Timezone`.
- URL tab precedent: `SalesOrderDetailPage.tsx:85` `tabFromParam(searchParams.get('tab'))`. Permission-aware customer link idea: `TopCustomers.tsx:82`.
- Keys: `customer.customer_id` SERIAL is the FK everywhere; `customer_uuid` is the API id. CRM `{id}` and each document's `customer.id` are the uuid. Document customer FKs (INTEGER NOT NULL, each with partial index `idx_*_customer`): quote, estimate, sales_order, fabrication_job, invoice, payment, credit_memo, refund.
- Resolver quirk: every resolver exposes `customer_id` as the serial; only `salesorder/resolver.go:31` also has `customer_uuid`. Not part of this feature.
- `customer_total_balance` etc. are hand-entered and stale; do not touch.
- No dark theme in WebUI; match the stone palette.

## 2. Backend design

### New package `customertransactions/` (read-only, sibling of `mytransactions`; does NOT use `query/`; one static UNION ALL from a registry; every client value bound as `$n`)

Registry, 8 sources (all Domain `"sales"`, base alias `t`, `s` = `lkp_record_status` on status column):

| key | Resource | Module | CustomerCol | Number | Date | DueDate | Amount | Balance | OpenWhere | Owner |
|---|---|---|---|---|---|---|---|---|---|---|
| quote | ResourceQuote | quote | quote_customer_id | quote_number | quote_date | quote_valid_until | quote_grand_total | — | code IN (DRFT,PAPV,APPV,SENT) | quote_owner_id |
| estimate | ResourceEstimate | estimate | estimate_customer_id | estimate_number | estimate_date | estimate_valid_until | estimate_grand_total | — | same as quote | estimate_owner_id |
| sales_order | ResourceSalesOrder | sales_order | sales_order_customer_id | sales_order_number | sales_order_date | — | sales_order_grand_total | — | code IN (DRFT,PAPV,APPV,OPEN,PART) | sales_order_owner_id |
| fabrication_job | ResourceInstallation | installation | fabrication_job_customer_id | fabrication_job_number | fabrication_job_created_at::date | job_promised_install_date | — | — | code NOT IN (COMP,CANC) | job_owner_id |
| invoice | ResourceInvoice | invoice | invoice_customer_id | invoice_number | invoice_date | invoice_due_date | invoice_grand_total | invoice_balance_due | code IN (SENT,PART,ODUE) AND balance_due > 0 | invoice_owner_id |
| payment | ResourcePayment | payment | payment_customer_id | payment_number | payment_date | — | payment_amount | GREATEST(unapplied − refunded − credited, 0) | code <> 'VOID' AND balance > 0 | payment_owner_id |
| credit_memo | ResourceCreditMemo | credit_memo | credit_memo_customer_id | credit_memo_number | credit_memo_date | — | credit_memo_grand_total | GREATEST(unapplied − refunded, 0) | code = 'APPV' AND balance > 0 | credit_memo_owner_id |
| refund | ResourceRefund | refund | refund_customer_id | refund_number | refund_date | — | refund_amount | — | code = 'PEND' | refund_owner_id |

Only invoices can be overdue: `OverdueWhere = open AND t.invoice_due_date < {today}`; others `FALSE`. Verify every column name against `database/migrations/tenant/schema.sql` before coding.

Branch SQL shape:
```sql
SELECT '<key>'::text type, <ID>::text id, COALESCE(<Number>,'') number,
       COALESCE(s.record_status_name,'') status_name, COALESCE(s.record_status_code,'') status_code,
       <Date>::date doc_date, <DueDate|NULL>::date due_date, (<Amount>|NULL)::float8 amount,
       (<Balance>|NULL)::float8 balance, (<OpenWhere>) is_open, (<OverdueWhere|FALSE>) is_overdue,
       t.<p>_created_at::timestamp created_at, t.<p>_updated_at::timestamp updated_at
FROM <table> t LEFT JOIN lkp_record_status s ON s.record_status_id = t.<p>_status
WHERE t.<p>_deleted_at IS NULL AND t.<CustomerCol> = $1 [AND t.<Owner> = {emp}]
```
Outer: `SELECT ..., COUNT(*) OVER () total FROM (<union>) x <where> ORDER BY doc_date DESC, created_at DESC, type, id LIMIT $n OFFSET $m`.

Binder: `$1` = customer internal id. `today` and `emp` bound lazily and memoized (Postgres rejects unreferenced bind params; see `mytransactions/sql.go:138`).

RBAC: parent via `authCRMByRecordID(ActionRead)`; if record key != `"customer"` → 404 "Record not found." Per source: `authz.EffectiveGrants` → `grantFor` (copy from `mytransactions/sql.go:218`, fail-closed: only explicit `all` lifts owner narrowing). Own scope adds `t.<Owner> = emp` (no employee row → emp 0 → matches nothing). No read grant → source dropped from union.

Paging: page/offset with `total`; `MaxLimit` 100, `DefaultLimit` 25, `MaxPage` 10000.
Filters (invalid → 400 via `InvalidParamError`): `type` (registry key), `status` (`^[A-Z_]{2,8}$`), `from`/`to` (`dateonly.Parse`, on doc_date; from>to → 400), `q` (≤100 chars, `query.StripLikeMeta`, number ILIKE), `open` (bool), `page`, `limit`.

Summary (AR fields NULL when caller can't read the backing module): `openBalance`, `openInvoiceCount`, `overdueBalance`, `overdueInvoiceCount`, `unappliedCredits` (payment + credit-memo balances; null only if neither readable), `billed12m` (invoice grand total, billed statuses, invoice_date >= today−365), `paid12m` (payments APPV/DEPO, payment_date >= today−365), `lastActivityAt` (MAX updated_at over readable union). `counts`: GROUP BY type,status folded in Go to `{count, openCount, statuses[]}`. `scopeLimited = true` when any of 8 sources is unreadable or own-scoped.

Today: controller computes `automation.TenantToday(time.Now(), companyprofile.Get(...).Timezone, fallback)`; fallback built in `main.go` from `config.AppConfig.AutomationDefaultTimezone` (as line ~358) and injected via constructor.

Endpoints (in `main.go` near line 1130, `tenantChain`):
- `GET /api/tenant/crm/customer/records/{id}/transactions` → `custTxOps.List`
- `GET /api/tenant/crm/customer/records/{id}/transactions/summary` → `custTxOps.Summary`
If no source readable: 200 with empty rows and `types: []`.
Logging: security events come from `authCRMByRecordID`; 500s `slog.Error("customer transactions: request failed", "error", err)`. No schema change. No globalsearch/approvalchain change; add a guard test against `mytransactions.Sources()`.

### API contract
```jsonc
// GET .../transactions?type=invoice&status=SENT&from=2026-01-01&to=2026-10-31&q=INV-1&open=true&page=1&limit=25
{ "success": true, "page": 1, "limit": 25, "total": 42,
  "rows": [{ "type": "invoice", "typeLabel": "Invoice", "domain": "sales", "module": "invoice",
             "id": "<uuid>", "number": "INV-001042", "status": "Sent", "statusCode": "SENT",
             "date": "2026-10-01", "dueDate": "2026-10-31",   // string|null
             "amount": 1250.00, "balance": 400.00,            // number|null
             "isOpen": true, "isOverdue": false,
             "createdAt": "2026-10-01T14:03:00Z", "updatedAt": "2026-10-02T09:00:00Z" }] }

// GET .../transactions/summary
{ "success": true, "scopeLimited": false,
  "summary": { "openBalance": 400.0, "openInvoiceCount": 1, "overdueBalance": 0, "overdueInvoiceCount": 0,
               "unappliedCredits": 50.0, "billed12m": 9800.0, "paid12m": 9400.0,
               "lastActivityAt": "2026-10-02T09:00:00Z" },     // any money field may be null
  "counts": [{ "type": "invoice", "count": 12, "openCount": 1,
               "statuses": [{ "code": "PAID", "label": "Paid", "count": 11 }] }],
  "types": [{ "type": "invoice", "label": "Invoice", "domain": "sales", "module": "invoice" }] }
// Errors: 400 {success:false,message:"Invalid <param>: <msg>."}; 401; 404 "Record not found."; 500.
```

## 3. Frontend design (StoneSuite-WebUI)

Routes: none new. `/crm/customer/:id?tab=transactions&type=&status=&from=&to=&q=&open=1&page=` reuses existing `PermissionGuard resource="customer" action="read"`. Tab read from `?tab=`, falling back to `location.state.initialTab` then `overview`; write with `setSearchParams(..., { replace: true })`. Filter change resets `page`.

New files:
- `src/types/customerTransactions.ts`: `CustomerTransactionRow`, `CustomerTransactionsPage`, `CustomerTransactionSummary`, `CustomerTransactionCount`, `CustomerTransactionsOverview`, `CustomerTransactionFilters`.
- `src/services/customerTransactionsService.ts`: `list(customerId, filters & {page, limit})`, `summary(customerId)`, `toParams()` drops defaults. URLs `/tenant/crm/customer/records/${id}/transactions` and `/transactions/summary`.
- `src/lib/customerTransactions.ts`: `parseFilters(sp)`, `writeFilters(sp, next)` (preserves `tab`, deletes defaults), `statusOptions(counts, type)`, `isFiltered(f)`, `balanceLabel(row)` (invoice → "Balance due"; payment/credit_memo → "Unapplied").
- `src/hooks/useCustomerTransactions.ts`: React Query keys `['customer-transactions', id, 'list', filters, page, limit]` and `[..., 'summary']`; `placeholderData: keepPreviousData`, `staleTime: 10_000`.
- `src/pages/crm/customer/components/transactions/`: `CustomerTransactionsTab.tsx` (container; owns URL filter state; states: loading `Spinner`, error `ErrorNote`+retry, `types.length===0` "You don't have access to any transaction types for this customer.", empty+unfiltered "No transactions for this customer yet", empty+filtered "No matching transactions"+Clear filters), `CustomerTransactionSummary.tsx` (tiles: Open balance, Overdue, Unapplied credits, Billed (12 mo), Paid (12 mo), Last activity; hide null tiles; "Based on records you can see." when scopeLimited), `CustomerTransactionFilters.tsx` (type select with counts; status select only when type chosen; from/to date inputs; "Open items only" toggle; debounced 300ms number search; Clear; wraps on mobile), `CustomerTransactionsTable.tsx` (lg+: Date | Type | Number | Status | Due | Amount | Balance; row click and Link go to `recordRoute(domain, module, id)`; red "Overdue" badge; `formatMoney`), `CustomerTransactionCards.tsx` (<lg).
- Reuse `TypeBadge`, `StatusBadge`, `TransactionsPagination` from `pages/transactions/components` (widen props to a `Pick<>` if TS complains).
- `src/components/crm/CustomerLink.tsx`: props `{ id?: string|null; name: string; className?: string }`; `<Link to=/crm/customer/:id>` with `stopPropagation` and `aria-label="View customer <name>"` when id set and `hasPermission('customer','read')`, else plain `<span>`.

Edits:
- `CustomerDetailPage.tsx`: extract `TABS` + tab bar into `pages/crm/customer/components/CustomerDetailTabs.tsx` (page must get shorter); URL-driven tab; replace `<TransactionsContent />` with `<CustomerTransactionsTab customerId={id} />`.
- Customer-name links (16 places): customer `<td>` in `pages/sales/components/{Quote,Estimate,SalesOrder,FabricationJob,Invoice,Payment,CreditMemo,Refund}Table.tsx`; sidebar "Customer" row in `pages/sales/{Quote,Estimate,SalesOrder,FabricationJob,Invoice,Payment,CreditMemo,Refund}DetailPage.tsx` (~lines 288–483).
- Global search already routes customer hits to `/crm/customer/:id`: verify only.
- `CrmSubTabsPanel.TransactionsContent` stays for lead/prospect.

## 4. Task breakdown

Model key: C = cheap/mechanical, S = Sonnet standard.

### Backend PR `feat(crm): customer transactions feed + summary` (branch from `develop`)

| # | Task | Files | Signatures / tests | Model | Deps |
|---|---|---|---|---|---|
| B1 | Registry | `customertransactions/registry.go` | `type Source struct{Key,Label string; Resource authz.Resource; Domain,Module,Table,CustomerCol,ID,Number,StatusCol,Date,DueDate,Amount,Balance,OpenWhere,DeletedAt,CreatedAt,UpdatedAt,Owner string; Overdue bool}`; `func Sources() []Source`; `func sourceByKey(string)(Source,bool)` | S | — |
| B2 | Registry tests | `registry_test.go` | `TestSources_WellFormed` (identifier regex, no `;`, 8 entries); `TestSources_MatchMyTransactionsSales` (key/resource/module set-equal to `mytransactions.Sources()` where Domain=="sales") | C | B1 |
| B3 | SQL builder | `sql.go` | `type granted struct{Source; own bool}`; `grantFor(Source, []authz.Grant)(granted,bool)`; `type binder` with `add(any) string`, `today() string`, `emp() string` (memoized); `(g granted) branchSQL(b *binder) string`; `buildPageSQL(src []granted, f pageFilter, customerID, employee int, today time.Time)(string, []any)`; `buildCountSQL(...)` | S | B1 |
| B4 | SQL unit tests | `sql_test.go` | table-driven `TestBuildPageSQL` (own adds owner predicate; all-scope never binds emp; filters add `$n`, never literals; `q` uses ILIKE on number); `TestGrantFor` (missing→false, own→own, all→not own, unknown scope→own) | C | B3 |
| B5 | Store: List | `store.go` | `Params{IdentityID, CustomerUUID string; Type, Status, Search string; From, To *dateonly.Date; OpenOnly bool; Page, Limit int; Today time.Time}`; `Row` (JSON per contract); `Page{Rows []Row; Total, Page, Limit int}`; `InvalidParamError`; `parse(Params)(pageFilter,int,error)`; `List(ctx, pool, p)(Page, error)`. Resolves `customer_id` from uuid (CUST + not deleted else `ErrCustomerNotFound`) and employee as `mytransactions.resolveActor` | S | B3 |
| B6 | Summary | `summary.go` (+`summary_sql.go` if >300 lines) | `Summary`, `Count`, `StatusCount`, `Overview{Summary; Counts []Count; Types []TypeInfo; ScopeLimited bool}`; `GetOverview(ctx, pool, identityID, customerUUID string, today time.Time)(Overview, error)` | S | B5 |
| B7 | Parse tests | `store_test.go` | `TestParse` table: bad type, status regex, from>to, q too long, page bounds, limit clamp | C | B5 |
| B8 | DB tests | `store_dbtest_test.go` (`//go:build dbtest`) | `TestBranchesExecuteAgainstSchema` (each source alone, own/all, full union, page/count/summary); `TestList_DB` (only this customer's docs; deleted excluded; own scope; no grant drops type; open/type/status/date/q filters; ordering; total past end); `TestOverview_DB` (open/overdue/unapplied/billed/paid match seeds; null when invoice unreadable; scopeLimited). Copy `seedActor`/`read` from `mytransactions/store_dbtest_test.go`; create docs via module `Create` (see `controllers/globalsearch_dbtest_test.go`) or minimal INSERT | S | B5, B6 |
| B9 | Controller | `controllers/customer_transactions.go` | `type CustomerTransactionsOps struct{crm *CRMOps; fallbackTZ *time.Location}`; `NewCustomerTransactionsOps(fallback *time.Location)`; `List`, `Summary` (`w http.ResponseWriter, r *http.Request`); `authCustomer(w,r,id)(pool *pgxpool.Pool, identityID string, ok bool)` (calls `authCRMByRecordID`, requires key `"customer"` else 404); `tenantToday(ctx,pool) time.Time`; `writeCustomerTxError` (400/404/500) | S | B5, B6 |
| B10 | Wiring | `main.go` ~line 1130 | `custTxOps := controllers.NewCustomerTransactionsOps(defaultTZ)` + 2 `mux.Handle("GET …", tenantChain(...))`; `defaultTZ` from `config.AppConfig.AutomationDefaultTimezone` as at ~line 358 | C | B9 |
| B11 | Controller DB test | `controllers/customer_transactions_dbtest_test.go` | `TestCustomerTransactions_IDOR_DB`: own-scoped caller on a customer they don't own → 404 + no rows; lead uuid → 404; unknown uuid → 404; no customer:read → 404; customer:read but no invoice:read → 200 with no invoice rows; bad `from` → 400. Harness: `withTestJWTSecret`, `testCustomerTenantPool`, `newSAMLTestControlPlane`, `seedServableCustomerTestTenant`, `tenancy.NewResolver` | S | B9, B10 |
| B12 | Docs | `docs/crm-concepts.md` (Customers: one paragraph on the Transactions tab, open items, overdue, summary figures; end-user wording only) then `go run ./cmd/gen-apidocs` | — | C | B10 |

Verification:
```bash
go build ./... && go vet ./...
go test ./customertransactions/... ./controllers/... .
TEST_DATABASE_URL=... go test -tags dbtest ./customertransactions/... ./controllers/ -run 'CustomerTransactions|TestList_DB|TestOverview_DB|TestBranches'
go run ./cmd/gen-apidocs -check
golangci-lint run
wc -l customertransactions/*.go controllers/customer_transactions*.go   # every file <= 300
```
Then run `module-drift-checker`-style review is N/A (not a module); run `tenancy-security-reviewer` and `migration-auditor` is N/A (no schema).

### Frontend PR `feat(crm): customer Transactions tab + customer links` (branch from `develop`; build against mocks, merge after backend is deployed)

| # | Task | Files | Tests | Model | Deps |
|---|---|---|---|---|---|
| F1 | Types + service | `types/customerTransactions.ts`, `services/customerTransactionsService.ts` | `toParams` drops defaults; `open`→`'true'`; encoded id in URL; null rows→[] | C | contract |
| F2 | Pure helpers | `lib/customerTransactions.ts` | table-driven: parse/write round-trip, `tab` preserved, filter change clears `page`, invalid page→1, `statusOptions` | C | F1 |
| F3 | Hook | `hooks/useCustomerTransactions.ts` | query keys; disabled with no id | C | F1 |
| F4 | `CustomerLink` | `components/crm/CustomerLink.tsx` | link with permission; span without permission or id; click doesn't bubble | C | — |
| F5 | Summary tiles | `.../transactions/CustomerTransactionSummary.tsx` | via F8 | S | F1 |
| F6 | Filters | `.../transactions/CustomerTransactionFilters.tsx` | via F8 | S | F2 |
| F7 | Table + cards | `.../CustomerTransactionsTable.tsx`, `CustomerTransactionCards.tsx` | via F8 | C | F1 |
| F8 | Container | `.../CustomerTransactionsTab.tsx` | mock service, MemoryRouter with query string: loading; error+retry; no-types message; empty vs filtered-empty; filter writes URL and resets page; overdue badge; row links to `/sales/invoice/<id>`; null tiles hidden; scopeLimited note | S | F3, F5–F7 |
| F9 | Detail page | `CustomerDetailPage.tsx`, new `components/CustomerDetailTabs.tsx` | new `CustomerDetailPage.test.tsx`: `?tab=transactions` opens tab; clicking a tab updates URL; `state.initialTab` still works | S | F8 |
| F10 | Links in tables | 8 `pages/sales/components/*Table.tsx` | update `InvoiceTable.test.tsx`/`SalesOrderTable.test.tsx`: name links to `/crm/customer/<id>` | C | F4 |
| F11 | Links in detail sidebars | 8 `pages/sales/*DetailPage.tsx` | existing tests pass; add one assertion in `InvoiceDetailPage.test.tsx` | C | F4 |

Verification: `npm run ci` (lint + vitest + build). Manual check in `npm run dev` against a local backend at 375px and desktop, as an own-scoped role and as a user without invoice:read.

## 5. Risks and non-goals

Risks: live summary will disagree with stale stored balances on Overview (decision 4); own-scoped users see partial AR (scopeLimited note); open-status definitions are hard-coded per type, so document in `registry.go` that a new status needs an open-logic update; guard test B2 catches forgotten customer-linked modules.

Non-goals (follow-ups): AR dunning/automation changes; statement PDF; CSV export; "New Invoice / Record Payment for this customer" quick actions; `customer_uuid` filter key on the 7 resolvers; dashboard AR widget customer links (needs `customerUuid` on `invoice.OutstandingInvoice`); CRM activities/notes/emails in the feed; lead/prospect transactions; dark mode; multi-currency conversion.

## 6. Definition of done

- Every customer name in sales lists, sales detail pages, global search opens `/crm/customer/:id`.
- Transactions tab lists every readable quote, estimate, sales order, installation job, invoice, payment, credit memo and refund for that customer; filters survive reload and sharing.
- Summary tiles match dashboard AR definitions.
- An own-scoped or ungranted user never sees or learns about a record they can't open (404 either way).
- `go test ./...`, dbtest run, `gen-apidocs -check`, `npm run ci` green.

Backend PR checklist: routes on `tenantChain`; parent auth via `authCRMByRecordID`, non-customer → 404; `grantFor` fail-closed; filters ANDed; all client values `$n`, registry fragments static, lazy binder; table-driven + dbtests incl. IDOR/cross-scope + guard test; `InvalidParamError`→400; no schema change, no `log.Printf`, files ≤300 lines, public funcs documented; docs + apidocs committed; conventional commit; PR body includes the JSON contract.

Frontend PR checklist: all calls via `customerTransactionsService`; React Query only; no `any`; tab and filters in URL; `initialTab` fallback kept; loading/error/no-access/empty/filtered-empty states; table lg+, cards below; 404 never reveals existence; `CustomerLink` used in 8 tables + 8 sidebars; `CustomerDetailPage.tsx` not longer than before; new files ≤300 lines; tests for service, helpers, hook, `CustomerLink`, tab container, detail page; `npm run ci` green; screenshots attached (desktop, 375px, own-scoped user, no-invoice-access user).
