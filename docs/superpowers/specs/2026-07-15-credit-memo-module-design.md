# Credit Memo Module — Backend Design Spec

**Date:** 2026-07-15
**Status:** Draft — approved by user in planning session, proceeding to implementation.
**Scope:** New Credit Memo module (header + line items + invoice-application ledger + status workflow) for the StoneSuite multi-tenant, database-per-tenant CRM/ERP backend. Picks up the work the Payments module (`docs/superpowers/specs/2026-07-13-payments-module-design.md` §1, §13) explicitly deferred to "their own spec/plan cycles".

---

## 1. Overview & Goals

Add a production-grade **Credit Memo** module — credit issued to a customer against an invoice (returned goods, overbilling, negotiated adjustment) and applied to reduce what they owe — as a sibling of `invoice`/`payment`, following the same v2 relational conventions: hybrid PK, employee-based audit, soft delete, `record_version`, RBAC/scope/IDOR, the `query/` filter engine, keyset pagination.

Today there is no way to record a credit. The only mechanism that reduces `invoice_balance_due` is `payment_application` — i.e. taking cash. An invoice raised in error can only be zeroed by voiding it or by recording money that never changed hands.

**Phased scope (explicitly confirmed with the user):** this pass builds **Credit Memo only**.

- **Refund (`RFND`, record_type=10) is out of scope.** Its `lkp_record_type` row and lifecycle (`PEND → APPV → SENT`, `VOID`) are already seeded but get no tables or code here. Cash refund / payment-method reversal is deferred, exactly as Payments deferred Credit Memo.
- **Inventory restocking is out of scope**, and not merely deferred — it is not currently buildable. See AD-11.
- **No GL / double-entry ledger.** None exists in this codebase (AD-10).

**Non-negotiable constraints (from CLAUDE.md, identical to Invoice/Payment):**

- Database-per-tenant; no `tenant_id` column anywhere.
- v2 relational conventions: hybrid PK (`SERIAL` + `UUID`), `employee(employee_id)`-based audit columns, paired soft delete, `record_version`.
- Idempotent, append-only migrations (`CREATE TABLE IF NOT EXISTS`, `ON CONFLICT DO NOTHING`).
- Mandatory security chain on every `/api/tenant/` route.
- All list/search goes through `query/` (whitelisted `FieldResolver`, parameterized values, keyset pagination, filter × scope ANDed).

### What already exists (reuse, do not recreate)

| Concern | Existing asset | Location |
|---|---|---|
| Credit memo record type | `lkp_record_type` row `CRDT` / `creditmemo` (**id 9**) — already seeded, unused until now | `tenant/schema.sql:699` |
| Credit memo status lifecycle | `lkp_record_status` rows for `record_type=9`: `DRFT, APPV, APPL, VOID` — already seeded, unused until now | `tenant/schema.sql:739` |
| RBAC resource + 5 CRM actions | `authz.ResourceCreditMemo` — already seeded, unused until now | `authz/catalog.go:42`, `:156-160` |
| Generic CRM router mapping | `resourceForKey` case `"credit_memo"` — already present | `controllers/crm.go:67` |
| `approve` action | `authz.ActionApprove` — already defined | `authz/catalog.go:66` |
| Credit customer | `customer` (v2 relational master) | `tenant/schema.sql:1087` |
| Actor / owner | `employee` | `tenant/schema.sql:511` |
| Application target | `invoice`, `invoice_item` | `invoice/` + `tenant/schema.sql:2701` |
| Lineage source | `sales_order` | `tenant/schema.sql:2435` |
| Ledger + rollup pattern | `payment_application`, `payment/apply.go` | `tenant/schema.sql:3353` |
| Line-item pattern | `invoice_item` | `tenant/schema.sql:2799` |
| Filter/sort/paginate/search | `query/` package | `query/` |
| Money/line calc pattern | `invoice/calc.go` | `invoice/calc.go` |
| Audit log (one table, all resources) | `audit_logs` via `workflow.LogAuditFull` | `tenant/schema.sql:1278` |
| Row-level IDOR guard | `recordInScope` | `controllers/scope.go:26` |
| Auth skeleton (the correct one) | `controllers/payment.go:24-79` — only controller that logs `permission_denied` | `controllers/payment.go` |
| Lookups | `lkp_unit`, `lkp_currency`, `lkp_tax_rate`, `lkp_price_level`, `lkp_state`, `lkp_country` | `tenant/schema.sql` |

> **Key finding that shaped this design:** Credit Memo was already half-scaffolded across three layers — RBAC catalog, CRM router mapping, and both lookup seeds — by the original catalog work, then deliberately left tableless by the Payments spec. **Nothing in those layers is recreated here.** The seeds in particular are effectively immutable: `lkp_record_status` keys statuses to record types by *hardcoded integer*, relying on `SERIAL` assignment order, so editing or reordering them silently mis-assigns every downstream status.

### What is genuinely missing (new tables — justified in §3)

- `credit_memo`, `credit_memo_item`, `credit_memo_application`, `credit_memo_history` — no existing table can represent credit issued to a customer, what it was issued *for* (lines), and its possibly-partial, possibly-split application against one or more invoices.
- One new column, `invoice.invoice_credit_total` — see AD-4.

### Correcting the original brief

Four premises in the original request did not survive contact with the codebase. Recorded here because each was a deliberate decision, not an oversight:

1. **Lifecycle is `DRFT → APPV → APPL` + `VOID`**, not Draft/Approved/**Refunded**/Voided. `APPL` = Applied. Refund is a separate reserved record type. The seed is append-only (see Key finding above).
2. **"Refunded" is not a credit-memo status.** Refunding a credit to cash is the `RFND` module's job.
3. **Inventory restocking is not implementable as scoped** (AD-11).
4. **There is no ledger to adjust** (AD-10). "Ledger" in this spec means `credit_memo_application` and nothing more.

---

## 2. Architecture Decisions

**AD-1 — Dedicated relational tables, not the JSONB engine.** Same reasoning as Invoice/Payment. The seeded v1 JSONB `credit_memo` workflow (`schema.sql:1779-1812`, states `cm_draft`/`cm_issued`/`cm_applied`/`cm_void`) is a legacy placeholder from migration 010 and is **left in place, unused** — exactly the precedent `sales_order` set (`schema.sql:2426-2429`). Note its state keys deliberately differ from the `lkp_record_status` codes this module uses; they are unrelated namespaces.

**AD-2 — `credit_memo` is a sibling of `invoice`, connected through the junction table `credit_memo_application`.** A memo belongs to a customer, not to an invoice. It may be issued with no invoice in mind (a goodwill credit), then spread across several invoices later. `credit_memo_invoice_id` on the header is **lineage only** ("this memo arose from that invoice") and carries no money semantics — the ledger is the only thing that moves balance.

**AD-3 — Invoice-shaped, not payment-shaped.** The memo carries `credit_memo_item` lines mirroring `invoice_item`. A credit is issued *for* something — returned slabs, an overcharged line — and finance needs that itemization for audit. This is the one place the module follows `invoice/` rather than `payment/`.

**AD-4 — `invoice_credit_total` is a third rollup column; `invoice_amount_paid` keeps meaning cash.**
```
invoice_balance_due = invoice_grand_total − invoice_amount_paid − invoice_credit_total
```
The rejected alternative was folding credits into `invoice_amount_paid` (no new column, no change to status derivation). It was rejected because it conflates cash with credit: AR aging, collections, and the audit question "how much did we actually collect?" all become unanswerable from the invoice row. A credit memo is not a payment, and the schema should not say it is.

**AD-5 — The AR rollup invariant lives in `invoice/balance.go`, and both writers route through it.** AD-4 means two packages now write the invoice rollup (`payment.Apply` and `creditmemo.Apply`). Duplicating that arithmetic would put a financial invariant in two places — precisely the copy-paste drift this repo already suffers from (see the `new-module` skill's account of `quote/`). So `invoice.RecomputeBalance` is the single owner, and `payment/apply.go`'s `recomputeInvoice` becomes a thin delegation to it.

Verified no import cycle: production direction is `payment → invoice` (`payment/quickpay.go:11`); `invoice → payment` exists *only* in the external `invoice_test` package (`invoice/store_delete_guard_test.go`). `creditmemo → invoice` adds none.

**AD-6 — `credit_memo_application` is the ledger of record; header money columns on both sides are stored rollups derived from it.** Identical to Payments AD-4. `credit_memo_applied_total` / `credit_memo_unapplied_amount` derive from that memo's live applications; `invoice_credit_total` derives from that invoice's live credit applications across all memos. Stored, not recomputed on read; every apply/unapply/void recomputes and writes both sides transactionally under `FOR UPDATE`.

**AD-7 — Application requires `APPV`; this is stricter than Payments AD-7.** Payments deliberately allows applying while `PEND` (money physically arrived; approval is bookkeeping). Credit is the opposite: nothing has arrived, and the memo *is* the authorization. Unapproved credit must never offset AR. So applications are permitted only from `APPV`, and `DRFT` cannot move money.

**AD-8 — The `DRFT → APPV` gate is a distinct permission, `credit_memo:approve`.** The 5 CRM actions cannot separate "submit a draft" from "approve it" — both would be `transition`. `authz.ActionApprove` already exists in the catalog; this appends one row, `{ResourceCreditMemo, ActionApprove}`. Every other transition stays gated on `transition`.

Rejected alternative: mirroring `estimate/approval.go`'s data-driven `estimate_approver` / `estimate_approval` tables (AD-8 there). That models *multi-person sign-off on a specific record*, which is a different axis and heavier surface area than the requirement ("finance approves, sales cannot"). YAGNI — it can layer on later without a schema change to this module.

**AD-9 — Each `credit_memo_application` row caps at the invoice's live `balance_due` and the memo's live `unapplied_amount`.** Rejected (400) at the amount that would overshoot, **never silently clamped** — identical to Payments AD-8. Excess stays as `credit_memo_unapplied_amount`, available elsewhere or later.

**AD-10 — No general ledger, because none exists.** Grep for `ledger|general_ledger|gl_account|journal_entry|chart_of_account` across the repo returns exactly one hit, and it is a comment (`main.go:567`). Balances are denormalized rollups. Introducing double-entry accounting for one module would be a platform decision, not a Credit Memo decision.

**AD-11 — No inventory restocking, because there is no inventory write path.** `quantity_on_hand` is `SELECT`ed in exactly two places (`salesorder/allocation.go:32,75`) and **written nowhere in the entire codebase**. There is no stock-movement, receiving, or adjustment table; `inventory_stock` is a static, externally-seeded figure. Restocking on credit would require building this repo's first inventory write path — a separate module with its own spec. `credit_memo_item.inventory_item_id` is recorded so that module can later find what was returned, but nothing is decremented or incremented.

**AD-12 — Lock order extends to a total order: `credit_memo < payment < invoice`.** The existing documented invariant is *payment before invoice* (`payment/apply.go:40-42`). `creditmemo.Apply` locks `credit_memo` then `invoice`, and never locks `payment`. Since `invoice` is always locked last on every path, no cycle is possible. The VOID cascade must `ORDER BY invoice_id`, mirroring `payment/store_transition.go:50`.

**AD-13 — `APPL` is derived, not user-directed.** It is set by the apply path when the memo becomes fully applied (`unapplied_amount ≈ 0`), and reversed to `APPV` by unapply. Users transition `DRFT→APPV` and `→VOID`; they do not hand-set `APPL`. This mirrors how `deriveInvoiceStatus` moves an invoice to `PAID` without a user-directed transition.

**AD-14 — `VOID` is not reachable from `APPL`.** A consumed credit must be unapplied first, which returns it to `APPV`. This keeps void's cascade bounded and makes "this credit was spent" a real terminal state. `APPL` and `VOID` are both terminal in the user-directed map.

**AD-15 — Money and lines are immutable once approved.** Mirrors Payments AD-10. `PATCH` may edit lines and money **only while `DRFT`** — once `APPV`, the memo is an authorized instrument and applications may exist against it. To correct an approved memo: void it and issue a new one. Non-monetary fields (memo, notes, reference) stay editable.

**AD-16 — Deleting a memo is blocked while it has live applications.** Mirrors Payments AD-11. Must unapply (or void) first — no orphaned ledger rows pointing at a hidden header.

**AD-17 — No exchange rate conversion.** `credit_memo_currency` is display-only; application amounts compare numerically against invoice `balance_due`, same-currency assumed. Identical to Payments AD-12.

---

## 3. New Tables — Per-Table Justification

### `credit_memo`
The header. No existing table can hold "credit issued to a customer, for a reason, in a state, with a running applied/unapplied balance". Modeled on `invoice` (money summary + billing snapshot + custom fields + lineage) with payment's applied/unapplied rollup grafted on.

### `credit_memo_item`
Per AD-3, a credit is itemized. Mirrors `invoice_item` exactly — including its asymmetry with the header (**no `item_deleted_by`, no `item_updated_by`**), because deviating there would be gratuitous drift.

### `credit_memo_application`
The ledger of record (AD-6). Junction between memo and invoice with an amount. Cannot reuse `payment_application`: its `payment_id` is `NOT NULL REFERENCES payment(payment_id)`, so a credit would need a fabricated payment row — which would corrupt `invoice_amount_paid`'s meaning (AD-4) and every payment report.

### `credit_memo_history`
Typed status trail, written **inside** the mutation transaction (unlike `audit_logs`, which is written outside it from the controller). Mirrors `invoice_history` / `payment_history`.

### No new audit table
`audit_logs` is one table for all resources, discriminated by the `resource` column.

### No new lookup tables
`lkp_record_type`, `lkp_record_status`, `lkp_unit`, `lkp_currency`, `lkp_tax_rate` all exist and already carry `CRDT`.

---

## 4. ER Diagram (text)

```
customer ──1──┬──*── credit_memo ──1──*── credit_memo_item ──*──1── inventory_item
              │           │  │                                (recorded, not decremented — AD-11)
              │           │  ├──*──1── invoice        (lineage only, nullable — AD-2)
              │           │  └──*──1── sales_order    (lineage only, nullable)
              │           │
              │           ├──1──*── credit_memo_history
              │           │
              │           └──1──*── credit_memo_application ──*──1── invoice
              │                          (the ledger — AD-6)              │
              └──*── invoice ──1──*── payment_application ──*──1── payment
                        │
                        └── rollups: amount_paid  <- SUM(payment_application)
                                     credit_total <- SUM(credit_memo_application)   [AD-4]
                                     balance_due  =  grand_total - amount_paid - credit_total
                                     (single writer: invoice.RecomputeBalance — AD-5)
```

---

## 5. SQL

See `database/migrations/tenant/schema.sql`, block `-- -- 000031_credit_memo_module ---`,
appended after the payment block. Summary of what it contains:

- `credit_memo` — hybrid PK, `record_type`/`credit_memo_status` FKs into the lookups, customer FK (required), invoice + sales_order FKs (nullable, `ON DELETE SET NULL`), money summary, `credit_memo_applied_total`/`credit_memo_unapplied_amount`, billing snapshot, `credit_memo_custom_fields JSONB NOT NULL DEFAULT '{}'`, `credit_memo_owner_id`, paired soft delete, `credit_memo_record_version`.
- `credit_memo_item` — mirrors `invoice_item`.
- `credit_memo_application` — mirrors `payment_application`, incl. `uq_cm_app_live_pair`.
- `credit_memo_history` — mirrors `invoice_history`.
- `ALTER TABLE invoice ADD COLUMN IF NOT EXISTS invoice_credit_total DECIMAL(15,2) NOT NULL DEFAULT 0;`

**Conventions enforced:** money `DECIMAL(15,2)`; percent `DECIMAL(6,4)` + `CHECK 0..100`; qty `DECIMAL(14,3)`; bare `TIMESTAMP` (v2, **not** `TIMESTAMPTZ`); no triggers; `credit_memo_owner_id INTEGER NULL REFERENCES employee(employee_id)` with **no team column** (v2 modules pass `""` for teamID to `recordInScope`).

**Idempotency:** every statement is `CREATE TABLE IF NOT EXISTS` / `CREATE INDEX IF NOT EXISTS` / `ADD COLUMN IF NOT EXISTS`. The whole file runs in **one transaction** on every tenant boot, so `CREATE INDEX CONCURRENTLY`, `VACUUM`, `DROP`, `TRUNCATE`, type-changing `ALTER COLUMN`, and renames are all forbidden. No down-migration — recovery is Neon PITR.

**Seeds:** none. `CRDT` and its four statuses already exist.

**Indexes:** partial on live rows (`WHERE credit_memo_deleted_at IS NULL`), named `idx_cm_*`. Every column exposed as **sortable** in `resolver.go` gets a `(column, id)` composite for keyset pagination. Plus `uq_cmi_line_active` (unique `(credit_memo_id, line_number)` among live rows, so Update can soft-delete and re-insert the same line number) and `idx_inv_credit_id` on the new invoice column.

---

## 6. Foreign Key Relationships

| From | To | Nullability | On delete | Why |
|---|---|---|---|---|
| `credit_memo.credit_memo_customer_id` | `customer` | NOT NULL | restrict (default) | A credit always belongs to a customer. |
| `credit_memo.credit_memo_invoice_id` | `invoice` | NULL | `SET NULL` | Lineage only (AD-2). Goodwill credits have no source invoice. |
| `credit_memo.credit_memo_sales_order_id` | `sales_order` | NULL | `SET NULL` | Lineage, mirrors `invoice.invoice_sales_order_id`. |
| `credit_memo.credit_memo_parent_id` | `credit_memo` | NULL | restrict | Self-FK lineage, mirrors `invoice_parent_id`. |
| `credit_memo_item.credit_memo_id` | `credit_memo` | NOT NULL | `CASCADE` | Lines are owned by the header. |
| `credit_memo_item.inventory_item_id` | `inventory_item` | NULL | restrict | Records *what* was credited (AD-11). Free-text lines have none. |
| `credit_memo_application.credit_memo_id` | `credit_memo` | NOT NULL | `CASCADE` | Mirrors `payment_application`. |
| `credit_memo_application.invoice_id` | `invoice` | NOT NULL | restrict | **Not** cascade — an invoice must not be hard-deletable out from under a live credit application. Mirrors `payment_application.invoice_id`. |
| `credit_memo_history.credit_memo_id` | `credit_memo` | NOT NULL | `CASCADE` | Trail dies with the record. |
| `*_created_by` / `_updated_by` / `_deleted_by` / `_owner_id` | `employee` | NULL | restrict | v2 audit convention. |

---

## 7. Status Transition Rules (service layer)

```
DRFT ──approve──> APPV ──(derived)──> APPL
  │                 │  <──(derived)──┘
  └──> VOID <───────┘
```

`creditmemo/transitions.go`:
```go
var allowedTransitions = map[string]map[string]bool{
    "DRFT": {"APPV": true, "VOID": true},
    "APPV": {"APPL": true, "VOID": true},
    "APPL": {},
    "VOID": {},
}
```

- `DRFT → APPV` requires **`credit_memo:approve`** (AD-8); all others require `credit_memo:transition`.
- `APPV → APPL` is derived by the apply path (AD-13); unapply reverses it. It is in the map because the apply path validates through it.
- `APPL → VOID` is **absent by design** (AD-14) — unapply first.
- Denied transition → **409**.

---

## 8. Money, Application & Rollup Rules

1. `round2` on every stored money value (`invoice/calc.go` convention; `float64` + explicit rounding).
2. **Apply** (`creditmemo.Apply`), in one tx, locks **credit_memo then invoice** (AD-12):
   - amount must be `> 0` → else 400.
   - memo must be `APPV` (AD-7) → else 409.
   - memo customer must equal invoice customer → else 400.
   - invoice must be in `SENT`/`PART`/`ODUE` (reuses invoice's payable gate) → else 409.
   - amount must be `<= min(memo.unapplied_amount, invoice.balance_due)` → else **400, not clamped** (AD-9).
   - upsert the live `(credit_memo_id, invoice_id)` row — **increment** if present, per `uq_cm_app_live_pair`.
   - `recomputeCreditMemo` → applied_total/unapplied_amount, and `APPV<->APPL` per AD-13.
   - `invoice.RecomputeBalance` → amount_paid, credit_total, balance_due, derived status, `invoice_history` row (AD-5).
   - `credit_memo_history` row, action `apply`.
3. **Unapply** — symmetric; soft-deletes the application row and recomputes both sides.
4. **Void cascade** (reachable only from `DRFT`/`APPV`, AD-14) — reverses every live application, `ORDER BY invoice_id`, recomputing each invoice (AD-12).
5. **`invoice.RecomputeBalance`** (AD-5) is the sole writer of the invoice rollup:
   ```
   amount_paid  = SUM(live payment_application, live parent payment)
   credit_total = SUM(live credit_memo_application, live parent credit_memo)
   balance_due  = round2(grand_total - amount_paid - credit_total), floored at 0
   status       = DeriveStatus(current, settled = amount_paid + credit_total, grand_total)
   ```
   `DeriveStatus` (moved from `payment/apply.go:111`) keeps its rationale: it deliberately bypasses `invoice.CanTransition`, because that map is for user-directed moves and has no path out of `PAID`, nor from `PART` back to `SENT` — moves an unapply legitimately needs.

---

## 9. API Contracts

All under `/api/tenant/credit-memos`, all wrapped in `tenantChain` (`main.go:370`). All ids are UUIDs. All responses `{success: bool, ...}`.

| Method | Path | Permission | Notes |
|---|---|---|---|
| GET | `/credit-memos` | `credit_memo:read` + scope | keyset paginated |
| POST | `/credit-memos/search` | `credit_memo:read` + scope | `query/` filter body |
| POST | `/credit-memos` | `credit_memo:create` | 201; + `invoice:update` per applied invoice if input carries applications |
| GET | `/credit-memos/{uuid}` | `credit_memo:read` + IDOR | 404 on scope denial |
| PATCH | `/credit-memos/{uuid}` | `credit_memo:update` + IDOR | money/lines editable only while `DRFT` (AD-15) |
| DELETE | `/credit-memos/{uuid}` | `credit_memo:delete` + IDOR | soft; 409 if live applications (AD-16) |
| POST | `/credit-memos/{uuid}/transition` | `credit_memo:approve` for `->APPV`, else `credit_memo:transition` | 409 on denied transition |
| POST | `/credit-memos/{uuid}/apply` | `credit_memo:update` + **`invoice:update`** on target | AD-9 |
| POST | `/credit-memos/{uuid}/unapply` | `credit_memo:update` + **`invoice:update`** on target | |
| GET | `/credit-memos/{uuid}/audit` | `credit_memo:read` + IDOR | last 200 `audit_logs` rows |
| GET | `/invoices/{uuid}/credit-memos` | `invoice:read` + IDOR | memos applied to an invoice |

Status codes: 400 invalid input / over-apply / `*query.InvalidFilterError`; 401 unauthenticated; 403 permission denied; **404 scope denial** (never 403 — ids must not be enumerable); 409 invalid transition / delete with live applications; 500 otherwise.

---

## 10. Listing & Query Architecture

Identical pattern to Payment §10 / Invoice §11 — routed entirely through `query/` via `creditmemo/resolver.go` (table alias **`cm`**). No hand-rolled filtering.

- **Filter × scope is ANDed, never OR.** Scope filter is applied **before** `query.Build`, which receives `nextIdx` so placeholders don't collide (`payment/search.go:20-43`). A filter can only narrow the caller's permitted set.
- Field keys are a **whitelist**; unresolved → 400, never raw SQL. All values `$n`-parameterized.
- **Keyset pagination only** (opaque base64 cursor). `MaxLimit` 100, default 25.
- `sortFields` is a **separate, narrower whitelist** than `systemFields`: nullable columns are filterable but **never sortable** — NULLs break cursor comparison (`invoice/resolver.go:58-60`).
- `cf:<key>` reaches into `credit_memo_custom_fields`, regex-guarded.

Filterable per the brief: `credit_memo_date`, `customer_id`, `status`, `grand_total`, `unapplied_amount`, `invoice_id`, `owner_id`, plus `cf:` keys.

---

## 11. Validation Rules

- `customer_uuid` required and resolvable → else 400.
- At least one line required; each line: `quantity >= 0`, `unit_price >= 0`, `discount_percent` 0..100, `tax_percent` 0..100 (DB CHECKs backstop).
- Free-text lines must carry a non-empty item name (per the fix in commit `e19d412`).
- `custom_fields` validated against `workflow_field_definitions` for the `credit_memo` workflow before save; **max 15 keys**.
- **Nil-map guard** — the bug `quote`/`estimate` both inherited: the column is `NOT NULL DEFAULT '{}'`, and a nil Go map encodes as SQL NULL, so every PATCH omitting `custom_fields` 500s.
  ```go
  if custom == nil { custom = map[string]any{} }
  ```
- `actorOrSystem()` — never `nullableInt` — for any `_deleted_by` paired with a CHECK.

---

## 12. Backend Implementation Map

**New — `creditmemo/`:** `types.go`, `numbering.go` (`numberPrefix = "CRDT"` → `CRDT-000001`, formatted post-insert from the serial PK, not a sequence), `transitions.go`, `calc.go`, `resolver.go`, `search.go`, `store.go`, `store_create.go`, `store_update.go`, `store_transition.go`, `store_line_resolve.go`, `apply.go` + `calc_test.go`, `numbering_test.go`, `transitions_test.go`, `resolver_test.go`, `store_test.go` (dbtest).

**New — controllers:** `creditmemo.go`, `creditmemo_transition.go`, `creditmemo_audit.go`, `invoice_creditmemos.go`.

**New — shared:** `invoice/balance.go` (AD-5) — `Locked`, `LockForUpdate`, `LockForUpdateByID`, `PayableStatuses`, `DeriveStatus`, `RecomputeBalance`.

**Modified:** `database/migrations/tenant/schema.sql` (append + one `ADD COLUMN`) · `main.go` (11 routes) · `authz/catalog.go` (one row, AD-8) · `payment/apply.go` + `payment/store_transition.go` (delegate to `invoice.RecomputeBalance`; their local `lockedInvoice`/`deriveInvoiceStatus`/`recomputeInvoice` are removed) · `controllers/payment.go` (`paymentFail` gains an `invoice.ClientError` arm — without it a bad invoice on Apply would 500 instead of 400) · `invoice/store_update.go` (`SoftDelete` now also guards live `credit_memo_application`).

**Unchanged:** `controllers/crm.go` · both lookup seeds · `query/` (stays dependency-free).

### RBAC — roles are tenant data, not code

There is no role seed in this repo; roles are per-tenant rows in `roles`. Only `super_admin` is a system role, holding wildcard `('*','*','all')` which the enforcer treats as match-all. The requested split is delivered as a **grant recipe** via `POST /api/tenant/rbac/roles`:

| Role | Grants (`resource`, `action`, `scope`) |
|---|---|
| `sales_agent` | `credit_memo` × {create, read, update} @ **own** |
| `finance_manager` | `credit_memo` × {create, read, update, delete, transition, **approve**} @ **all**, plus `invoice` × {read, update} @ all — required, because apply mutates the invoice |
| `admin` | the existing `super_admin` system role — already match-all |

`sales_agent` deliberately holds no `transition` and no `approve`, so a draft cannot be self-approved.

---

## 13. Open Decisions — Resolved During Planning

1. **Lifecycle vs. the brief's "Refunded"** → use the seeded `DRFT/APPV/APPL/VOID`; Refund is the `RFND` module. *Resolved: seeded lifecycle.*
2. **How credits reduce AR** → new `invoice_credit_total` column; `amount_paid` keeps meaning cash (AD-4). *Resolved: third column.*
3. **How to split submit from approve** → new `credit_memo:approve` catalog row (AD-8), not estimate's approver tables. *Resolved: permission.*
4. **Lines or scalar amount** → invoice-shaped with lines (AD-3). *Resolved: lines.*
5. **Inventory restocking** → out of scope; not buildable without a first inventory write path (AD-11). *Resolved: excluded.*
6. **Where the rollup invariant lives** → `invoice/balance.go`, single writer (AD-5). *Resolved: extracted.*

## 14. Deliberately Out of Scope

Inventory restocking (AD-11) · cash refund / `RFND` module · GL or double-entry ledger (AD-10) · multi-approver sign-off tables (AD-8) · multi-currency conversion (AD-17) · frontend.
