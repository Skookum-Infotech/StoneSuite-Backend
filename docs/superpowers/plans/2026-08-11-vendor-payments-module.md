# Vendor Payments Module Implementation Plan

> **For agentic workers:** Each task below is dispatched to a cold-start `go-implementer`. The step text plus the named reference file is all the context you get — read the reference file first, every time. Steps use checkbox (`- [ ]`) syntax.

**Goal:** Build the Vendor Payment module (header + bill-application ledger + refund ledger + configurable approval + scheduling worker) plus a minimal Vendor Bill header module as its settlement target — the AP mirror of the existing `payment`/`payment_application`/`invoice` triangle.

**Spec (authoritative):** `docs/superpowers/specs/2026-08-11-vendor-payments-module-design.md` — cite section numbers ("spec §8", "spec AD-5").

**Architecture:** Two new packages, `vendorbill/` and `vendorpayment/`, cloned structurally from `payment/` + `invoice/balance.go` + `purchaseorder/approval.go`. `vendor_payment_application` is the ledger of record; `vendorbill.RecomputeBalance` is the sole writer of bill rollups; `vendor_payment_refund` is a separate netted ledger. Lock order is `vendor_payment < vendor_bill` (spec AD-11), never the reverse.

**Tech Stack:** Go (net/http, pgx/v5, pgxpool), PostgreSQL per-tenant DB, `testify` where the sibling uses it (most sibling tests use plain `testing`).

## Global Constraints

- No `tenant_id` column on any tenant-DB table. Database-per-tenant is the isolation boundary.
- Migrations idempotent + append-only: `CREATE TABLE IF NOT EXISTS`, `CREATE INDEX IF NOT EXISTS`, `INSERT ... ON CONFLICT DO NOTHING`. Never `DROP`, never rename, never edit an existing `CREATE TABLE` body.
- Every `/api/tenant/` route goes through `tenantChain` + `authz.Check` before any write + scope filtering on lists + single-record IDOR guard returning **404** (not 403), logged via `logSecurityEvent(r, "idor_denied", ...)`.
- Filter × scope ANDed, never OR. Field keys resolved through a whitelist `FieldResolver` only. All values parameterized. Keyset pagination only, `MaxLimit` 100.
- Money is `DECIMAL(15,2)`; Go-side rounding through a local `round2`.
- Response envelope `{success, message?, ...}` via `controllers.writeJSON` / `controllers.fail`.
- `ResourceVendorBill` and `ResourceVendorPayment` are **already seeded** in `authz/catalog.go:315-325`. **Do not touch `authz/catalog.go`.**
- `VBIL` (record type 15) and `VPAY` (16) are **already seeded** in `schema.sql:708-709`, as are their status rows. Only the two new VPAY statuses (`DRFT`, `SCHD`) get appended.
- Files over 300 lines: split them.
- Integration tests: `//go:build dbtest` + `TEST_DATABASE_URL`, `t.Skip` when unset. Pure-function tests carry no build tag.
- Package names lowercase single word: `vendorbill`, `vendorpayment`.
- Every exported function gets a single-line doc comment. Errors wrapped with `fmt.Errorf("context: %w", err)`.

## File Structure

**Created — `vendorbill/`** (minimal AP mirror of `invoice/`):
- `types.go` — `VendorBill`, `VendorRef`, `CreateVendorBillInput`, `UpdateVendorBillInput`, `Page`
- `balance.go` — `PayableStatuses`, `Locked`, `BalanceDue()`, `LockForUpdate`, `LockForUpdateByID`, `DeriveStatus`, `RecomputeBalance`, `round2` (clone of `invoice/balance.go`)
- `transitions.go` — `CanTransition`, `ValidateTransition`, `ErrInvalidTransition` (spec §7 bill map)
- `numbering.go` — `FormatNumber` → `VBIL-000001`
- `store.go` — `Get`, `headerSelect`, `scanVendorBill`, `typeIDByCode`, `statusIDByCode`, `ErrNotFound`, `ClientError`, `nullableInt`, `actorOrSystem`, `validateCustom`
- `store_create.go` — `Create`
- `store_update.go` — `Update`, `SoftDelete`
- `store_transition.go` — `Transition` (VOID blocked on live applications)
- `resolver.go` — `resolver` (FieldResolver + SortResolver + SearchResolver)
- `search.go` — `Search` (keyset)
- `*_test.go`

**Created — `vendorpayment/`**:
- `types.go` — `VendorPayment`, `VendorRef`, `Application`, `Refund`, `CreateVendorPaymentInput`, `ApplicationInput`, `UpdateVendorPaymentInput`, `Page`
- `money.go` — `round2`
- `transitions.go` — transition map (spec §7 payment map), `ErrInvalidTransition`
- `numbering.go` — `FormatNumber` → `VPAY-000001`
- `approval.go` — `Approve`, `activeApproverCount`, `signOffCount`, `isConfiguredApprover`, `ErrNotApprover`/`ErrApprovalRequired`/`ErrApprovalNotRequired`
- `store.go` — `Get`, scanning, lookups, `ErrNotFound`, `ClientError`, `actorOrSystem`, `validateCustom`
- `store_create.go` — `Create`
- `store_update.go` — `Update`, `SoftDelete`
- `store_transition.go` — `Transition` (VOID cascade, approval gate, SCHD date guard)
- `apply.go` — `Apply`, `Unapply`, `lockPaymentForUpdate`, `recomputePayment`
- `refund.go` — `RecordRefund`, `RemoveRefund`
- `resolver.go`, `search.go`
- `scheduler.go` — `DueScheduled`, `RunSchedulerTick`
- `*_test.go`

**Created — controllers:**
- `controllers/vendorbill.go`, `controllers/vendorbill_transition.go`, `controllers/vendorbill_audit.go`, `controllers/vendorbill_payments.go`
- `controllers/vendorpayment.go`, `controllers/vendorpayment_transition.go`, `controllers/vendorpayment_audit.go`

**Modified:**
- `database/migrations/tenant/schema.sql` — append the whole block after the `vendor` indexes (spec §5)
- `main.go` — register routes + `startVendorPaymentScheduler`

---

## Phase 1 — Database schema

### Task 1.1: Append vendor_bill / vendor_payment tables, statuses and indexes

**Files:**
- Modify: `database/migrations/tenant/schema.sql` (append at EOF, after the existing `vendor` block/indexes at ~line 3512)

**Interfaces:**
- Produces: tables `vendor_bill`, `vendor_bill_history`, `vendor_payment`, `vendor_payment_application`, `vendor_payment_refund`, `vendor_payment_history`, `vendor_payment_approver`, `vendor_payment_approval`; `lkp_record_status` rows `DRFT`/`SCHD` for record type `VPAY`.

- [ ] **Step 1:** Read spec §5.1 through §5.8 in full. Copy every SQL statement from those sections **verbatim** and append them to the very end of `database/migrations/tenant/schema.sql`, preceded by a section banner comment:

```sql
-- ── Vendor Bills + Vendor Payments module ───────────────────────────
```

Append in this exact order: §5.1 (the two `lkp_record_status` rows), §5.2 (`vendor_bill`, `vendor_bill_history`), §5.3 (`vendor_payment`), §5.4 (`vendor_payment_application` + its partial unique index), §5.5 (`vendor_payment_refund`), §5.6 (`vendor_payment_history`), §5.7 (`vendor_payment_approver`, `vendor_payment_approval`), §5.8 (all indexes).

- [ ] **Step 2:** Verify the `ON CONFLICT` target in §5.1 matches a real unique constraint. Run:

```bash
grep -n "uq_record_status\|UNIQUE (record_status_code" database/migrations/tenant/schema.sql
```

If the existing unique constraint on `lkp_record_status` is named/shaped differently than `(record_status_code, record_status_record_type)`, adjust the `ON CONFLICT` clause to match the **actual** constraint columns. Do not add a new constraint.

- [ ] **Step 3:** Confirm every appended statement is idempotent. Every `CREATE TABLE` has `IF NOT EXISTS`; every `CREATE INDEX`/`CREATE UNIQUE INDEX` has `IF NOT EXISTS`; every `INSERT` has `ON CONFLICT ... DO NOTHING`. There must be zero `DROP`, `TRUNCATE`, `ALTER COLUMN ... TYPE`, or `ADD CONSTRAINT` statements in the diff.

- [ ] **Step 4:** Run `go build ./...` — must still compile (pure SQL append, nothing references these tables yet).

- [ ] **Step 5:** Report the appended line range. Do not commit.

---

## Phase 2 — `vendorbill` package (sequential; everything else depends on it)

### Task 2.1: Whole `vendorbill/` package
**Clone from:** `invoice/balance.go` (balance.go), `payment/store.go` + `payment/store_create.go` + `payment/store_update.go` + `payment/store_transition.go` (store files), `payment/resolver.go` + `payment/search.go` (query files), `payment/numbering.go`, `payment/transitions.go`, `purchaseorder/store.go:164-177` (`vendorSnapshot`).

Deltas from the `payment` clone: entity `vendor_bill`, prefix `VBIL`, counterparty is `vendor` (not `customer`), snapshot the vendor display name into `vendor_bill_vendor_name`, transition map is spec §7's bill map, `RecomputeBalance` sums `vendor_payment_application` minus `vendor_payment_refund` (no credit-memo leg), `DeriveStatus` returns `PAID`/`PART`/`APPV`, `Transition` to `VOID` returns `ClientError` while any live `vendor_payment_application` exists.

### Task 2.2: `vendorbill` unit tests
Table-driven, no build tag: `transitions_test.go`, `numbering_test.go`, `resolver_test.go` (every whitelisted key resolves, `cf:` keys validate, unknown key → false), `balance_test.go` (`DeriveStatus` table + `BalanceDue` floors at zero).

## Phase 3 — `vendorpayment` package (sequential, after Phase 2)

### Task 3.1: Core (`types.go`, `money.go`, `numbering.go`, `transitions.go`, `store.go`, `store_create.go`, `store_update.go`)
**Clone from:** the matching `payment/` files. Deltas: `VPAY` prefix, vendor counterparty + name snapshot, new entry status `DRFT`, extra columns `vendor_payment_scheduled_date` / `vendor_payment_approval_status` / `vendor_payment_approved_by`, `SoftDelete` blocked on live applications.

### Task 3.2: `apply.go` + `refund.go`
**Clone from:** `payment/apply.go`. Lock order `vendor_payment` → `vendor_bill` (spec AD-11). `Apply` caps at `min(unapplied, bill.BalanceDue())`, rejects above cap (never clamps), rejects vendor mismatch (400), rejects `VOID` payment (409), rejects a bill outside `{APPV, PART}`. `refund.go` adds `RecordRefund` (capped at `application_amount − live refunds on that application`) and `RemoveRefund`; both recompute payment + bill rollups.

### Task 3.3: `approval.go` + `store_transition.go`
**Clone from:** `purchaseorder/approval.go` and `purchaseorder/store_transition.go` verbatim, plus `payment/store_transition.go`'s VOID cascade. `Transition` rejects a manual `PAPV→APPV` (must use `Approve`), requires `scheduled_date` set before `→SCHD`, and cascades every live application on `→VOID`.

### Task 3.4: `resolver.go` + `search.go` + `scheduler.go`
Resolver whitelist exactly as spec §10. `scheduler.go` exposes `DueScheduled(ctx, pool) ([]string, error)` (uuids of `SCHD` payments with `scheduled_date <= CURRENT_DATE`) and `RunSchedulerTick(ctx, pool) (int, error)` which transitions each to `SENT` through `Transition` with actor `1`.

### Task 3.5: `vendorpayment` unit tests
Same shape as Task 2.2, plus an apply-cap math table test.

## Phase 4 — Controllers (parallelizable: bill and payment controllers touch disjoint files)

### Task 4.1: `controllers/vendorbill*.go`
**Clone from:** `controllers/payment.go` (auth skeleton), `payment_transition.go`, `payment_audit.go`, `invoice_payments.go`. Handlers: `List`, `Search`, `Create`, `Get`, `Update`, `Delete`, `Transition`, `Audit`, `Payments`.

### Task 4.2: `controllers/vendorpayment*.go`
Same clone. Handlers: `List`, `Search`, `Create`, `Get`, `Update`, `Delete`, `Transition`, `Approve`, `Apply`, `Unapply`, `Refund`, `RemoveRefund`, `Audit`. `Apply`/`Unapply`/`Refund`/`RemoveRefund` additionally call `vendorBillInScopeForUpdate` (clone of `PaymentOps.invoiceInScopeForUpdate`, spec AD-10).

## Phase 5 — Wiring

### Task 5.1: `main.go` routes + scheduler
Register all routes from spec §9 alongside the existing payment block, and add `startVendorPaymentScheduler(ctx, cp, router)` modelled on `startRAGIndexing` (`main.go:1062-1112`) — one ticker goroutine per servable tenant, `ctx.Done()` exit.

