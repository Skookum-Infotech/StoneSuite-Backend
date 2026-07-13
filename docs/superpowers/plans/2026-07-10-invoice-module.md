# Invoice Module Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development or superpowers:executing-plans. Steps use checkbox (`- [ ]`) syntax for tracking. This plan assumes the Sales Order module (`salesorder/`, `inventory/`, `controllers/salesorder.go`, `controllers/inventory.go`, `sales_order`/`sales_order_item`/`inventory_item` tables) is **already implemented and merged on this branch** — Invoice depends on it for conversion (§9 of the spec) and FKs into `sales_order_item`. Verify with `ls salesorder/ inventory/` and `grep -n "CREATE TABLE IF NOT EXISTS sales_order " database/migrations/tenant/schema.sql` before starting; if either is missing, stop and finish that module first.

**Goal:** Add a production-grade relational Invoice module (header + line items + status lifecycle + minimal payments + listing/search) plus Sales Order → Invoice conversion with transactional snapshot pricing.

**Spec (authoritative):** `docs/superpowers/specs/2026-07-10-invoice-module-design.md` — cite section numbers.

**Tech Stack:** Go (net/http, pgx/pgxpool), PostgreSQL (per-tenant DB), `testify`, Cloudflare R2 for attachments.

## Global Constraints (identical to the Sales Order plan — see CLAUDE.md)

- No `tenant_id`/`ss_tenant_id` column on any tenant-DB table.
- Migrations idempotent + append-only (`CREATE TABLE IF NOT EXISTS`, `ON CONFLICT DO NOTHING`, `ADD COLUMN IF NOT EXISTS` + `DEFAULT`). Never DROP/rename.
- Every `/api/tenant/` route: `tenantChain` (RequireAuth → rate limit → TenantResolver) + RBAC `authz.Check` before any write + scope filtering on lists + single-record IDOR guard returning **404** (not 403), logged via `logSecurityEvent(r, "idor_denied", ...)`.
- Filter × scope ANDed, never OR; field keys resolved via whitelist `FieldResolver` only; all values parameterized; keyset pagination only.
- Money `DECIMAL(15,2)`, quantity `DECIMAL(14,3)`, percent `DECIMAL(6,4)`, exchange rate `DECIMAL(18,6)`.
- Custom fields ≤15/workflow, validated via `workflow.ValidateCustomFields`/`ValidateCustomFieldsPartial`.
- Response envelope `{ success, message?, ... }` via `controllers.writeJSON`/`fail`.
- Conventional Commits; `go build ./... && go vet ./... && go test ./...` green before each commit.
- Integration tests: `//go:build dbtest` + `TEST_DATABASE_URL`, `t.Skip` when unset (see `ai/store_test.go`). Pure-function/resolver/handler-auth tests carry no build tag.
- Files over 300 lines: split them.

## File Structure

**Created:**
- `invoice/types.go` — DTOs / row structs (Invoice, InvoiceItem, CreateInvoiceInput, etc.)
- `invoice/calc.go` — line + header money math (spec §8)
- `invoice/transitions.go` — status transition map (spec §7)
- `invoice/numbering.go` — `INVC-000001` formatter (spec AD-7)
- `invoice/store.go` — relational store: create/update/get/list/delete/search/transition/recordPayment, transactional
- `invoice/resolver.go` — `invoiceResolver` implementing `query.FieldResolver` + `query.SortResolver` + `query.SearchResolver` (spec §11)
- `invoice/convert.go` — `ConvertSalesOrder(ctx, pool, soUUID, actorEmployeeID uuid/int) (*Invoice, error)` (spec §9)
- `invoice/*_test.go` — unit + integration tests
- `controllers/invoice.go` — `InvoiceOps` HTTP handlers
- `authz/catalog.go` — add `ResourceInvoice` (+ actions: read, create, update, delete, transition)

**Modified:**
- `database/migrations/tenant/schema.sql` — append `invoice`, `invoice_item`, `invoice_history` + indexes (spec §5)
- `main.go` — register invoice routes + the SO `convert-to-invoice` route
- `controllers/salesorder.go` — add `ConvertToInvoice` handler (calls `invoice.ConvertSalesOrder`)
- `workflow/attachments.go` — ensure `RecordKeyForAttachment` resolves an `invoice` UUID (mirror the `sales_order` branch added in the SO module)

---

## Phase 1 — Database schema + RBAC

### Task 1.1: Invoice tables + indexes
- [ ] **Step 1:** Read spec §5.1–§5.4 and §6. Use the **add-migration** skill to append `invoice`, `invoice_item`, `invoice_history` + all indexes to `database/migrations/tenant/schema.sql`, placed **after** the `sales_order`/`sales_order_item` block (FK ordering: `sales_order_item` → `invoice_item`; `sales_order` → `invoice`).
- [ ] **Step 2:** `go build ./...` to catch obvious SQL/Go mismatches at the app layer (schema.sql is plain SQL, but confirm nothing else references these table names yet that would break).
- [ ] **Step 3:** If a scratch/test DB is reachable, apply the schema twice to verify idempotency (`psql $TEST_DATABASE_URL -f database/migrations/tenant/schema.sql` twice, or however this repo's dbtest convention invokes it — check `database/migrations.go` / `ai/store_test.go` for the actual apply mechanism). Otherwise, visually confirm every statement is `IF NOT EXISTS`/`ON CONFLICT DO NOTHING`.
- [ ] **Step 4:** Dispatch **migration-auditor** agent on the diff. Fix findings.
- [ ] **Step 5:** Commit — `git commit -m "feat(invoice): add invoice, invoice_item, invoice_history tables"`.

### Task 1.2: RBAC — `invoice` resource
- [ ] **Step 1:** Write a failing test in `authz/catalog_test.go` (mirror the `inventory_item` resource test added for Sales Order) asserting an `invoice` resource exists with actions `read, create, update, delete, transition`.
- [ ] **Step 2:** Run to verify it fails.
- [ ] **Step 3:** Add `ResourceInvoice` + catalog entries to `authz/catalog.go`, and a `resourceForKey` mapping in `controllers/crm.go` if that's where Sales Order's was added (check how `sales_order` was wired — mirror exactly). Also check `controllers/rbac_catalog_drift_test.go` — it likely needs the new resource added to its expected set.
- [ ] **Step 4:** Run to verify it passes; run `go test ./controllers/ -run Drift -v` to confirm the drift test still passes.
- [ ] **Step 5:** Commit — `git commit -m "feat(authz): add invoice RBAC resource"`.

---

## Phase 2 — Invoice pure domain logic (TDD-first)

### Task 2.1: Line + header money math
- [ ] **Steps:** Write failing table-driven tests in `invoice/calc_test.go` per spec §8 formulas (mirror `salesorder/calc_test.go` cases: zero discount, full discount, tax on discounted subtotal, rounding at each step). Implement `invoice/calc.go`. Run to pass. Commit — `feat(invoice): add line + header money calculation`.

### Task 2.2: Status transition map
- [ ] **Steps:** Write failing tests in `invoice/transitions_test.go` covering every edge in spec §7's map (including `PART`/`ODUE` cross-moves and both terminal states rejecting all further moves). Implement `invoice/transitions.go`. Run to pass. Commit — `feat(invoice): add status transition validation`.

### Task 2.3: Invoice-number formatting
- [ ] **Steps:** Write failing test in `invoice/numbering_test.go` (mirror `salesorder/numbering_test.go`: `FormatNumber(7) == "INVC-000007"`, zero-padding, no upper bound truncation for 7-digit ids). Implement `invoice/numbering.go`. Run to pass. Commit — `feat(invoice): add invoice-number formatting`.

### Task 2.4: Types
- [ ] **Steps:** Write a JSON-key test (mirror `salesorder/types_test.go` if one exists, else `inventory/types_test.go` pattern) for `Invoice`, `InvoiceItem`, `CreateInvoiceInput`, `InvoiceLineInput`. Implement `invoice/types.go`. Run, commit — `feat(invoice): add invoice/line/input types`.

---

## Phase 3 — Invoice store + resolver

### Task 3.1: Store — create (transactional, snapshots, totals, numbering)
- [ ] **Step 1:** Read `salesorder/store.go`'s `Create` (the just-built Sales Order equivalent) before writing this — match its transaction shape, snapshot-copy pattern, and error wrapping exactly.
- [ ] **Step 2:** Write failing `dbtest`-tagged integration tests: create with items computes correct totals; create snapshots billing/shipping from customer when no `salesOrderUuid` given; create rejects a `salesOrderUuid` in the body (spec §12 — direct create must not accept SO linkage, only the conversion endpoint sets it); starts at `DRFT`; assigns `invoice_number` post-insert.
- [ ] **Step 3:** Implement `Create` in `invoice/store.go`.
- [ ] **Step 4:** Run to pass (or SKIP without `TEST_DATABASE_URL`, but code must compile and untagged tests must pass).
- [ ] **Step 5:** Commit — `feat(invoice): transactional create with snapshots, totals, numbering`.

### Task 3.2: Store — get / update / soft-delete
- [ ] **Steps:** Integration tests: get returns items; update recomputes `grand_total`/`balance_due`; update rejected (409) once status is `PAID`/`VOID`; soft-deleted invoice not returned by Get; `record_version` increments on update (optimistic concurrency — mirror `sales_order`/`customer`). Implement (mirror `salesorder/store.go` get/update/delete). Run, commit — `feat(invoice): add get/update/soft-delete`.

### Task 3.3: Store — record payment
- [ ] **Steps:** Integration tests per spec §8/§12: payment within balance succeeds, updates `amount_paid`/`balance_due`, writes `invoice_history` action='payment'; payment exceeding remaining balance → 400/error, no partial write; payment on `DRFT`/`PAPV`/`APPV`/`VOID` → rejected; payment that exactly zeroes balance transitions to `PAID`; partial payment from `SENT` transitions to `PART`. All inside one transaction (`SELECT ... FOR UPDATE` on the invoice row to serialize concurrent payments). Implement `RecordPayment` in `invoice/store.go`. Run, commit — `feat(invoice): add payment recording with auto-transition`.

### Task 3.4: Resolver (FieldResolver + SortResolver + SearchResolver)
- [ ] **Step 1:** Write failing resolver tests (mirror `salesorder/resolver_test.go`) covering every key in spec §11's table, plus the `cf:<key>` namespace and the unknown-key → `ok=false` case.
- [ ] **Step 2:** Implement `invoice/resolver.go` using spec §11's mapping table exactly (`Resolve`, `SortExpr`, `SearchPredicate`).
- [ ] **Step 3:** Run to pass. Commit — `feat(invoice): add field/sort/search resolver`.

### Task 3.5: Store — `Search` (scope + filter + keyset)
- [ ] **Steps:** Integration tests: filter `status in [SENT,PART]`; sort by `balance_due`; `search:"INVC-0004"` matches by number; unknown filter field → `*query.InvalidFilterError`; scope `own` returns only caller-owned; filter × scope stays ANDed. Implement mirroring `salesorder/store.go`'s `Search`. Dispatch **filter-invariant-checker** on the diff before committing. Commit — `feat(invoice): add keyset search with scope + filter + sort + global search`.

---

## Phase 4 — Invoice HTTP layer (CRUD + listing + payments + transitions)

### Task 4.1: `InvoiceOps` CRUD + list handlers
- [ ] **Step 1:** Write failing handler tests (mirror `controllers/salesorder_test.go`): `Create` 403 without `invoice:create`, 201 with; `Get` on another owner's invoice with `own` scope → **404**, logs `idor_denied`; `Search` returns `{success, records, nextCursor, hasMore}`; `Create` with a `salesOrderUuid` in the body is rejected (spec §12).
- [ ] **Step 2:** Implement `controllers/invoice.go` (`InvoiceOps`) mirroring `SalesOrderOps` exactly (auth helper, employee-id lookup, `ErrRecordNotFound` → 404).
- [ ] **Step 3:** Register routes in `main.go` per spec §10's table.
- [ ] **Step 4:** Run `go build ./... && go test ./controllers/ -run Invoice -v` → PASS.
- [ ] **Step 5:** Commit — `feat(invoice): add CRUD + list/search HTTP handlers and routes`.

### Task 4.2: Transition + payment endpoints
- [ ] **Steps:** Handler tests: `POST .../transition` DRFT→PAPV ok, PAID→anything 409; `POST .../payments` happy path + overpayment 400 + wrong-status 409. Implement handlers calling the Phase 3.3 store methods, map `ErrInvalidTransition`→409, `ErrPaymentExceedsBalance`→400. Run, commit — `feat(invoice): add transition and payment HTTP endpoints`.

### Task 4.3: Sales Order → Invoice conversion endpoint
- [ ] **Step 1:** Write failing integration test per spec §9: converting an `OPEN` SO creates a `DRFT` invoice with items whose snapshot fields exactly match the SO lines (not re-read from `inventory_item`); converting a `DRFT`/`CANC` SO → 409; converting an SO with no lines → 409; two conversions of the same SO create two separate invoices (no dedup, per AD-8).
- [ ] **Step 2:** Implement `invoice.ConvertSalesOrder` in `invoice/convert.go` (transactional: `SELECT ... FOR UPDATE` the SO, copy snapshots, insert items + header, compute totals, generate number, write history) per spec §9's exact steps.
- [ ] **Step 3:** Add `ConvertToInvoice` handler to `controllers/salesorder.go` (checks `sales_order:read`+IDOR on the SO **and** `invoice:create`), register `POST /api/tenant/sales-orders/{uuid}/convert-to-invoice` in `main.go`.
- [ ] **Step 4:** Run to pass. Commit — `feat(invoice): add sales-order-to-invoice conversion with snapshot pricing`.

### Task 4.4: Attachments wiring
- [ ] **Steps:** Test asserting `RecordKeyForAttachment` resolves an `invoice` UUID to a usable workflow key, mirroring whatever branch the Sales Order module added for `sales_order`. Implement if a branch is needed. Commit — `feat(invoice): wire attachments to invoice records`.

---

## Phase 5 — Verification & security review

### Task 5.1: Full build, vet, test, and agent review
- [ ] **Step 1:** `go build ./... && go vet ./... && go test ./...` → all PASS.
- [ ] **Step 2:** Dispatch **filter-invariant-checker** on `query/`, `invoice/resolver.go`, `invoice/store.go` diffs.
- [ ] **Step 3:** Dispatch **tenancy-security-reviewer** on `controllers/invoice.go`, `controllers/salesorder.go` (conversion handler), `invoice/store.go`, `invoice/convert.go`.
- [ ] **Step 4:** Dispatch **migration-auditor** on the final `schema.sql` diff (in case anything changed since Task 1.1).
- [ ] **Step 5:** Fix any CRITICAL/HIGH findings, commit fixes — `fix(invoice): address security/invariant review findings` (only if needed).
- [ ] **Step 6:** Leave the branch in a state ready for PR review — do not push, do not open a PR.

---

## Notes for the implementing agent

- **Snapshot pricing is the load-bearing requirement** (spec §8, AD-4): conversion must copy `sales_order_item`'s frozen columns, never re-join `inventory_item` for current price/tax. Get an integration test for this specific behavior before considering Phase 4.3 done.
- **Don't build a Payment/Credit-Memo module.** Spec §14 Open Decision #2 deliberately scopes payments down to two stored columns + one endpoint. Resist the urge to add a `payment` table — it's out of scope and would duplicate the reserved `lkp_record_type` rows (`PYMT`/`CRDT`) meant for a future dedicated module.
- **Don't invent a shared numbering package.** Spec §14 Open Decision #1: follow `salesorder/numbering.go`'s exact pattern in `invoice/numbering.go`. A generic extraction is explicitly deferred.
- If any spec section is ambiguous once you're in the code, resolve it the same direction the Sales Order module resolved the analogous question (cite the SO spec section you're mirroring) rather than inventing a new convention.
