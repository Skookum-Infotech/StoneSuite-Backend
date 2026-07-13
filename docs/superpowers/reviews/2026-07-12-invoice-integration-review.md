# Invoice Module — Integration, Architecture & Review

**Date:** 2026-07-12
**Branch:** `feat/invoice-module-integration` (off `develop` @ `ed5c388`, PR #46)
**Source:** ported from `origin/feature/invoice-module` (colleague's work)
**Scope of this pass:** integrate + review + document. No new feature code.

---

## 1. What was integrated and how

The invoice module was brought onto develop as an **additive port**, not a `git merge`.
Rationale: the invoice branch had diverged in parallel on `salesorder/` (it branched at
PR #40 and kept refactoring sales-order while develop landed #46's schema.org/Order work).
A raw merge would have collided on `salesorder/store.go`/`types.go`/`controllers/salesorder.go`
(full rewrites on both sides) and risked reverting #46. Since the invoice module has
**zero Go-level dependency** on the invoice branch's sales-order/inventory code (it only
touches `sales_order`/`sales_order_item`/`inventory_item` at the SQL-JOIN level, which
develop's schema already provides as a superset), we ported only the invoice-owned files.

**Result:** `git diff --stat develop` = **28 files, +3270, −0** (100% additive; #46 fully preserved).

Ported:
- `invoice/` — 18 files (types, calc, numbering, transitions, store + create/update/transition/line-resolve, resolver, search + tests).
- `controllers/invoice.go`, `invoice_transition.go`, `invoice_audit.go`, `invoice_test.go`.
- Design docs `docs/superpowers/{specs,plans}/2026-07-10-invoice-module*`.

Additive edits to existing develop files:
- `database/migrations/tenant/schema.sql` — appended `invoice`, `invoice_item`, `invoice_history` DDL + indexes (+179 lines). **Two integration fixes applied during the port** (below).
- `workflow/store.go` — added `EmployeeIDByIdentity` helper (invoice's `search.go` needs it; absent on develop).
- `authz/catalog_test.go` — added the invoice-permission assertion (`ResourceInvoice` was already registered on develop).
- `main.go` — registered the 9 `/api/tenant/invoices...` routes in `tenantChain`.

**Adaptation for develop:** the invoice controllers called a helper `employeeID(ctx, pool, id)`
that lived in the invoice branch's (unported) `controllers/salesorder.go`. Rather than
duplicate it, the 5 call sites now reuse develop's existing `resolveEmployeeID(r, identityID)`
(`controllers/crm_admin.go`), which does the identical query.

### Fixes already applied during integration (data-integrity, not new features)
1. **Restored the two lineage FKs** that the colleague's DDL had dropped with a false
   "missing table" comment. `sales_order`/`sales_order_item` exist on develop, so:
   - `invoice.invoice_sales_order_id → sales_order(sales_order_id) ON DELETE SET NULL`
   - `invoice_item.sales_order_item_id → sales_order_item(sales_order_item_id) ON DELETE SET NULL`
2. **Repaired an invalid UTF-8 byte** (`0x97`, a Windows-1252 em-dash) in a `schema.sql`
   comment that made the file invalid UTF-8. Replaced with a proper em-dash.

### Verification
- `go build ./...` clean, `go vet ./...` clean, **`go test ./...` all green** (full suite).
- Schema audited idempotent; all FKs resolve against develop; `INVC` type + 8 statuses seeded.

---

## 2. Architecture (invoice as a sibling of sales order)

The invoice module follows the same v2-relational pattern as `customer` and `sales_order`:

- **Hybrid PK**: `SERIAL` internal id + external `UUID` (`gen_random_uuid()`), unique.
- **Database-per-tenant**: no `tenant_id` column; the connection is the tenant scope.
- **Three tables**: `invoice` (header) → `invoice_item` (lines, `ON DELETE CASCADE`) +
  `invoice_history` (typed status/payment trail).
- **Stored snapshot pricing** (spec AD-4/AD-5): line + header money computed once at write
  and stored; never recomputed on read. Billing/shipping addresses and per-line SKU/price/tax
  are frozen snapshots, not live joins.
- **AR balance on the header** (spec §14 open-decision #2): `amount_paid` / `balance_due`
  live on `invoice` directly. **No separate Payment table yet** — `PYMT`/`CRDT` record types
  are reserved for future work.
- **Status lifecycle**: `DRFT → PAPV → APPV → SENT`; from `SENT` → `PART`/`PAID`/`ODUE`/`VOID`;
  `PAID` and `VOID` terminal. Enforced by a static transition map (`invoice/transitions.go`).
- **Security chain** (all `/api/tenant/invoices...`): `tenantChain` (auth + rate-limit +
  TenantResolver) → in-handler `authz.Check(ResourceInvoice, action)` → scope filter on list,
  `recordInScope` IDOR guard (404 on deny) on single-record ops → security-event logging.
- **Shared filter engine**: list/search route through `query/` (whitelisted fields, keyset
  pagination, parameterized values) via `invoice/resolver.go` + `invoice/search.go`.

### API surface (9 endpoints, resource `invoice`)
`GET /invoices` (read) · `POST /invoices/search` (read) · `POST /invoices` (create) ·
`GET|PATCH|DELETE /invoices/{uuid}` (read/update/delete) ·
`POST /invoices/{uuid}/transition` (transition) ·
`POST /invoices/{uuid}/payment` (update) · `GET /invoices/{uuid}/audit` (read).

---

## 3. System design — "invoice based on sales order" (the deferred piece)

**Status: designed in the spec, NOT implemented.** The headline capability — creating an
invoice *from* a sales order — has no code on the branch: no `invoice/convert.go`, no
`POST /sales-orders/{uuid}/convert-to-invoice` route, no handler. `CreateInvoiceInput`
deliberately omits `salesOrderUuid`; only standalone invoices can be created today. The
lineage columns exist (and now have real FKs) but nothing writes them.

**Recommended conversion design (next pass), per the colleague's spec §9 / AD-2 / AD-8:**
1. New route `POST /api/tenant/sales-orders/{uuid}/convert-to-invoice`, guarded by
   `invoice:create` (+ IDOR scope on the source SO).
2. New `invoice.ConvertFromSalesOrder(ctx, pool, soUUID, actorEmployeeID)` in `invoice/convert.go`:
   one transaction, `SELECT ... FOR UPDATE` the sales order, copy its already-frozen
   billing/shipping and per-line snapshots **verbatim** (no live re-read of customer/inventory),
   set `invoice_sales_order_id` / `invoice_item.sales_order_item_id`, compute totals,
   assign number, write a `convert` history row, start at `DRFT`.
3. Multiple invoices per SO allowed (progress billing); conversion is intentionally not
   idempotent. Direct `POST /invoices` continues to reject any SO linkage.

Invoice's read path already `LEFT JOIN`s `sales_order`/`sales_order_item` to expose lineage
UUIDs, so once the conversion writes those columns the existing Get/Search surfaces them
with no further change.

---

## 4. Consolidated review findings

Four local review agents ran against the integrated code: **migration-auditor**,
**tenancy-security-reviewer**, **filter-invariant-checker**, and **feature-dev:code-reviewer**.
Findings below are de-duplicated and re-graded with codebase context (karpathy lens: several
"issues" are faithful mirrors of the accepted `salesorder` pattern, not invoice regressions).

### Must-fix before this module is used for real invoicing

| # | Sev | Location | Finding | Recommended fix |
|---|-----|----------|---------|-----------------|
| C1 | High | `invoice/store_transition.go:114-134` | **Full payment on a DRFT/PAPV/APPV invoice** sets `balance_due=0` but the transition to `PAID` is gated by `CanTransition`, which has no edge from those states → invoice stuck "paid but DRFT", and the overpayment guard then blocks any recovery. The unresolved "thinking out loud" comments (L118-124) show this was never settled. | Reject `RecordPayment` on pre-`SENT` statuses with a `ClientError` (matches spec §12), or make full payment advance the workflow. |
| C2 | High | `invoice/store_update.go:131` | **`Update` hard-`DELETE`s all `invoice_item` rows** then re-inserts, even though the schema has `item_deleted_at` and `loadLines` filters on it — the sibling `salesorder/store.go:844` **soft-deletes**. Line history on a financial doc is destroyed on every edit. | Change to `UPDATE invoice_item SET item_deleted_at = NOW() WHERE invoice_id=$1 AND item_deleted_at IS NULL`, matching salesorder. |
| C3 | Med-High | `invoice/store_update.go` + `invoice/calc.go:29-43` | **`Update` can write negative `balance_due`** (ComputeHeader doesn't clamp, unlike RecordPayment). Reducing line total below `amount_paid` violates `chk_invoice_paid_nonneg` → opaque **500** instead of a clean 400. | Validate `GrandTotal >= existing.AmountPaid` before the UPDATE; return a `ClientError`. |
| F1 | High | `invoice/resolver.go:62` | **`due_date` is a sortable field but nullable** with no COALESCE → NULLs break keyset-cursor comparison / pagination. | Wrap sort expr in `COALESCE(i.invoice_due_date, DATE '1900-01-01')`, or drop it from sortable fields. |

### Should-fix / consistency

| # | Sev | Location | Finding | Recommended fix |
|---|-----|----------|---------|-----------------|
| S1 | Med | `schema.sql` `invoice_item` | Audit-column gap: has `item_updated_at`/`item_deleted_at` but **no `item_updated_by`/`item_deleted_by`** and **no soft-delete CHECK** (header has both). | Add the two `*_by` columns (FK `employee`) + `chk_ii_soft_delete` pairing constraint. Pairs naturally with C2. |
| D1 | Med | `main.go` / routes | Payment route is **singular `/payment`**; spec §10 says `/payments`. | Align route name with spec (decide before frontend integrates). |
| D2 | Low | `controllers/invoice_transition.go` | `RecordPayment` guarded by `invoice:update`, not `:transition`. Reviewers judged this **acceptable** (author's deliberate choice; a payment is a data update). | Confirm intent; document. |

### Pre-existing, repo-wide (NOT an invoice regression)

- **`team` scope == `own` scope** (`invoice/search.go:22-30`): team-scoped users only see their
  own invoices. This is a **faithful mirror of develop's `salesorder/store.go:982`** (`if scope
  == "own" || scope == "team"`), which was accepted in #40/#46. It **fails closed** (under-grants,
  no data leak). Fix, if wanted, belongs in **both** modules (resolve team members via
  `workflow.TeamIDsForUser` and filter `owner_id = ANY(...)`), or `team` should not be a valid
  grant for these resources. Tracking it here so it isn't mistaken for an invoice-specific bug.

### Verified NOT bugs (checked and cleared)
- `statusIDByCode(ctx, pool, ...)` called with `pool` mid-transaction: only reads static
  seed data (`lkp_record_*`), never mutated at runtime — no consistency concern.
- **float64 for money** (`invoice/calc.go`): identical pattern to `salesorder/calc.go`; inputs
  pre-rounded via `round2`; the `+0.001`/`<0.005` fuzz thresholds are safe.
- Row-level locking (`FOR UPDATE OF i`) in Transition/RecordPayment correctly serializes.

---

## 5. Recommended next steps
1. Apply C1–C3 + F1 (small, surgical correctness fixes) and S1 (schema pairing) — one focused PR.
2. Decide D1 (route name) and D2 (payment permission) with the frontend team.
3. Implement the SO→Invoice conversion (§3) as the follow-up feature PR.
4. Treat the team-scope gap as a separate repo-wide RBAC ticket covering salesorder + invoice.
