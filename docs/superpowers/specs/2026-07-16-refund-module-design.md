# Refund Module — Backend Design Spec

**Date:** 2026-07-16
**Status:** Draft — approved by user in planning session, proceeding to implementation.
**Scope:** New Refund module (header + dual-source application ledger + status workflow) for the StoneSuite multi-tenant, database-per-tenant CRM/ERP backend. Picks up the work the Credit Memo module (`docs/superpowers/specs/2026-07-15-credit-memo-module-design.md` §1) explicitly deferred: *"Refund (`RFND`, record_type=10) is out of scope... Cash refund / payment-method reversal is deferred, exactly as Payments deferred Credit Memo."*

---

## 1. Overview & Goals

Add a production-grade **Refund** module — money returned to a customer, drawn against an overpayment held on a `payment` or an unapplied `credit_memo` — as a sibling of `payment`/`credit_memo`, following the same v2 relational conventions: hybrid PK, employee-based audit, soft delete, `record_version`, RBAC/scope/IDOR, the `query/` filter engine, keyset pagination.

Today there is no way to return money. A `payment`'s unapplied balance and a `credit_memo`'s unapplied balance can only ever be *applied* (to an invoice); neither can be *paid back out*.

**Non-negotiable constraints (from CLAUDE.md, identical to Payment/Credit Memo):**

- Database-per-tenant; no `tenant_id` column anywhere.
- v2 relational conventions: hybrid PK (`SERIAL` + `UUID`), `employee(employee_id)`-based audit columns, paired soft delete, `record_version`.
- Idempotent, append-only migrations (`CREATE TABLE IF NOT EXISTS`, `ON CONFLICT DO NOTHING`).
- Mandatory security chain on every `/api/tenant/` route.
- All list/search goes through `query/` (whitelisted `FieldResolver`, parameterized values, keyset pagination, filter × scope ANDed).

### Correcting the original brief

Two premises in the original request did not survive contact with the codebase — recorded here because each was a deliberate finding during planning, not an oversight:

1. **There is no payment gateway anywhere in StoneSuite.** No Stripe/PayPal/Braintree/Square/Authorize.net dependency, no `charge`/`capture`/`transaction_id` column, and **no inbound webhook endpoint of any kind** exists in this codebase (verified by exhaustive grep). `payment` is strictly record-only bookkeeping: it records money collected out-of-band (check, cash, ACH, wire) and reconciles it against invoices. A "Pending Gateway" status, a `payment_gateway_logs` table, and a webhook-driven update endpoint have no foundation to build on here and are **not part of this module**. Refund is **record-only**, exactly like Payment and Credit Memo: it records money that was *already* returned out-of-band (check mailed, ACH sent, cash handed back) and the reference/method fields capture how.
2. **The lifecycle is the already-seeded `PEND/APPV/SENT/VOID`, not a bespoke `Initiated/Pending Gateway/Succeeded/Failed`.** See the key finding below — the seed is seven months old and append-only; inventing new codes would silently orphan it.

### What already exists (reuse, do not recreate)

| Concern | Existing asset | Location |
|---|---|---|
| Refund record type | `lkp_record_type` row `RFND` / `customerrefund` (**id 10**) — already seeded, unused until now | `tenant/schema.sql:700` |
| Refund status lifecycle | `lkp_record_status` rows for `record_type=10`: `PEND, APPV, SENT, VOID` — already seeded, unused until now | `tenant/schema.sql:740` |
| RBAC resource + 5 CRM actions | `authz.ResourceRefund` — already seeded, unused until now | `authz/catalog.go:43`, `:167-171` |
| Refund method (how money went back) | `lkp_payment_method` (Check/Cash/Credit Card/ACH/Wire/Other) — reused as-is | `tenant/schema.sql:3287-3308` |
| Source #1 — overpayment held | `payment.payment_unapplied_amount` | `payment/` + `tenant/schema.sql:3310` |
| Source #2 — unapplied credit | `credit_memo.credit_memo_unapplied_amount` | `creditmemo/` + `tenant/schema.sql:3546` |
| Lineage reference (optional, no money) | `invoice` | `tenant/schema.sql:2701` |
| Ledger + rollup pattern | `payment_application`, `credit_memo_application` | `tenant/schema.sql:3353`, `:3678` |
| Filter/sort/paginate/search | `query/` package | `query/` |
| Money calc pattern | `payment/apply.go`, `round2` | `payment/` |
| Audit log (one table, all resources) | `audit_logs` via `workflow.LogAuditFull` | `tenant/schema.sql:1278` |
| Row-level IDOR guard | `recordInScope` | `controllers/scope.go:26` |
| Auth skeleton (the correct one) | `controllers/payment.go:24-79` — only controller that logs `permission_denied` | `controllers/payment.go` |
| Cross-resource IDOR pattern for a side-effect mutation | `invoiceInScopeForUpdate` | `controllers/payment.go:86-123` |

> **Key finding that shaped this design:** Refund was already half-scaffolded across two layers — RBAC catalog and both lookup seeds (`lkp_record_type` id 10, `lkp_record_status` for type 10) — by the original catalog work, then deliberately left tableless by the Credit Memo spec, exactly as Credit Memo itself had been left tableless by Payments. **Nothing in those layers is recreated here.** The seed is append-only: `lkp_record_status` keys statuses to record types by hardcoded integer, relying on `SERIAL` assignment order, so it cannot be edited or reordered — it can only be adopted as-is.

### What is genuinely missing (new tables — justified in §3)

- `refund`, `refund_application`, `refund_history` — no existing table can represent money returned to a customer, drawn from a payment or a credit memo, in a state, with a running applied/unapplied balance.
- Two new columns: `payment.payment_refunded_total`, `credit_memo.credit_memo_refunded_total` — see AD-2.

---

## 2. Architecture Decisions

**AD-1 — `refund` is payment-shaped, not invoice-shaped.** Unlike Credit Memo (which needed itemized lines because a credit is issued *for* something and finance needs that itemization), a refund is scalar: an amount going back out. It has no line items — the itemization already lives on the source (`payment` has no lines either; `credit_memo_item` already itemizes what was credited). Confirmed with the user: **amount-level only, no `refund_items` table.**

**AD-2 — Dual source via one ledger, `refund_application`, with an XOR constraint; refund owns two new rollup columns, one bolted onto each source.**
```
payment.payment_refunded_total       -- sole writer: refund module
credit_memo.credit_memo_refunded_total -- sole writer: refund module
available_from_payment      = payment_unapplied_amount      - payment_refunded_total
available_from_credit_memo  = credit_memo_unapplied_amount  - credit_memo_refunded_total
```
This mirrors exactly how Credit Memo added `invoice_credit_total` to `invoice` (AD-4 there) without invoice's own code ever writing it. The reverse composition here is simpler than the invoice case: `RecomputeBalance` centralizes *invoice's* rollup because two other modules (`payment`, `credit_memo`) both write into it. `refunded_total` has exactly one writer — refund itself — so there is no shared-invariant risk, and no exported lock/recompute API needs to be added to `payment` or `creditmemo`. **Refund's own SQL reads/updates these two columns directly** (its own `SELECT ... FOR UPDATE` against `payment`/`credit_memo`, its own `UPDATE ... SET payment_refunded_total = ...`); it never imports the `payment` or `creditmemo` Go packages, and neither of those completed modules' Go code changes.

The rejected alternative was a single polymorphic `source_type/source_id` pair on `refund_application` instead of two nullable FKs. Rejected because Postgres cannot enforce a polymorphic FK's referential integrity — an orphaned or wrong-table reference would compile and silently corrupt the ledger. Two nullable, real FKs plus an XOR `CHECK` gets full referential integrity at the cost of one extra column.

**AD-3 — Status lifecycle reuses the seeded `PEND → APPV → SENT`, `VOID`, record_type 10 — no new seed rows.**
```go
var allowedTransitions = map[string]map[string]bool{
    "PEND": {"APPV": true, "VOID": true},
    "APPV": {"SENT": true, "VOID": true},
    "SENT": {},
    "VOID": {},
}
```
This is structurally identical to Payment's `PEND→APPV→DEPO, VOID` (`payment/transitions.go:10-15`) with `DEPO` (deposited) renamed `SENT` (sent back to the customer) — the seed already made this exact substitution. `PEND` = Initiated (a support/finance draft, not yet authorized to move money). `APPV` = Approved (finance sign-off; authorizes drawing down the source). `SENT` = Issued (money physically returned; terminal). `VOID` reachable only from `PEND`/`APPV`, never from `SENT` — once money has gone out, reversing it is a new refund's problem, not a void (mirrors Payment AD-3/Credit Memo AD-14 exactly: a consumed/settled instrument's terminal state stays terminal).

**AD-4 — The `PEND → APPV` gate is a distinct permission, `refund:approve`.** The 5 CRM actions already seeded for `ResourceRefund` (create/read/update/delete/transition) cannot separate "initiate a refund" from "authorize it to draw down real money" — both would be `transition`. This appends one row, `{ResourceRefund, ActionApprove}`, mirroring Credit Memo AD-8 exactly. Every other transition (`APPV→SENT`, `→VOID`) stays gated on `transition`.

**AD-5 — Applying money (drawing down a source) requires the refund be `APPV` or later, never `PEND`.** Nothing has been authorized yet at `PEND`; the refund *is* the authorization once approved. This mirrors Credit Memo AD-7 (credit's `Apply` requires `APPV`) rather than Payment AD-7 (payment allows applying while `PEND`, because payment's money has already physically arrived — the opposite situation from a refund, where approval is what authorizes money to leave).

**AD-6 — Each `refund_application` row caps at `min(refund.unapplied_amount, source.available)` and is rejected (400), never silently clamped, on overshoot.** Identical to Payment AD-8 / Credit Memo AD-9. Excess stays as `refund_unapplied_amount`, available for a second application to the other source type (e.g. $30 from an overpayment + $20 from a credit memo composing one $50 refund) or later.

**AD-7 — Lock order extends the existing total order to `refund < credit_memo < payment < invoice`.** The existing invariant is `credit_memo < payment < invoice` (Credit Memo AD-12, `invoice/balance.go:46-51`). `refund.Apply` locks `refund` first, then its source (`payment` or `credit_memo`) — never both in the same call, since one application targets exactly one source (AD-2's XOR). `refund` never locks `invoice`. Since a single call only ever holds at most two locks (`refund`, then one of `payment`/`credit_memo`) and never acquires them in the reverse order, and neither `payment` nor `creditmemo` code ever locks `refund`, no cycle is possible. A VOID cascade that reverses applications against both source types in one transaction locks `credit_memo`-before-`payment`, consistent with the total order.

**AD-8 — Money and identity are immutable once approved.** Mirrors Payment AD-10 / Credit Memo AD-15. `PATCH` may edit non-monetary fields (reference, memo, notes) at any pre-terminal status; `amount`, `payment_id`/`credit_memo_id` source selection are fixed at create. To correct an approved refund: void it and create a new one.

**AD-9 — Deleting a refund is blocked while it has live applications.** Mirrors Payment AD-11 / Credit Memo AD-16. Must unapply (or void, which cascades the unapply) first.

**AD-10 — No general ledger, no gateway, no webhooks — because none exist.** Same finding as Credit Memo AD-10, extended: grep for `ledger|general_ledger|gl_account|journal_entry|chart_of_account` returns one unrelated comment; grep for `stripe|paypal|braintree|square|authorize\.net|gateway|webhook|charge|capture|transaction_id` returns no payment-processing infrastructure anywhere (the only "stripe" hits are three inert VARCHAR reference columns on `prospects`, unrelated to `payment`). Building a gateway/webhook layer is a platform decision, not a Refund-module decision, and is explicitly out of scope (§14).

**AD-11 — No exchange rate conversion.** `refund_currency` is display-only, reused from `lkp_currency`; application amounts compare numerically against the source's available balance, same-currency assumed. Identical to Payment AD-12 / Credit Memo AD-17.

**AD-12 — `refund_invoice_id` is optional lineage only, no money semantics.** A refund often arises "because invoice #123 was overpaid" or "because credit memo X against invoice #123 is being cashed out instead of applied" — worth recording for reporting, exactly like Credit Memo's `credit_memo_invoice_id`/`credit_memo_sales_order_id` (AD-2 there). It is never read by `Apply`/`Unapply` and never gates anything.

---

## 3. New Tables — Per-Table Justification

### `refund`
The header. No existing table can hold "money returned to a customer, drawn from a payment or credit memo, in a state, with a running applied/unapplied balance". Modeled on `payment` (scalar amount + applied/unapplied rollup) with a dual, XOR-constrained source ledger instead of payment's single-target-type ledger.

### `refund_application`
The ledger of record (AD-2). Junction between refund and **exactly one of** `payment` or `credit_memo`, enforced by an XOR `CHECK`. Cannot reuse `payment_application` or `credit_memo_application`: both have a `NOT NULL` FK to their single target type and neither can represent "money flowing the other direction, against either source."

### `refund_history`
Typed status/action trail, written **inside** the mutation transaction (unlike `audit_logs`, written outside it from the controller). Mirrors `payment_history` / `credit_memo_history`.

### No new audit table
`audit_logs` is one table for all resources, discriminated by the `resource` column.

### No new lookup tables
`lkp_record_type` (row `RFND`, id 10) and `lkp_record_status` (four rows for type 10) already exist. `lkp_payment_method` is reused as-is for "how the refund went back."

---

## 4. ER Diagram (text)

```
customer ──1──*── refund ──1──*── refund_history
              │  │
              │  ├──1──*── refund_application ──*──1── payment       (XOR: exactly one
              │  │                          └───*──1── credit_memo    of these two is set)
              │  │
              │  └──*──1── invoice   (lineage only, nullable — AD-12)
              │
              ├──*── payment      ──1──*── payment_application ──*──1── invoice
              │        rollup: payment_refunded_total <- SUM(live refund_application by payment)  [AD-2]
              │
              └──*── credit_memo  ──1──*── credit_memo_application ──*──1── invoice
                       rollup: credit_memo_refunded_total <- SUM(live refund_application by credit_memo) [AD-2]
```

---

## 5. SQL

See `database/migrations/tenant/schema.sql`, block `-- -- 000032_refund_module`, appended after the credit-memo block. Summary of what it contains:

- `ALTER TABLE payment ADD COLUMN IF NOT EXISTS payment_refunded_total DECIMAL(15,2) NOT NULL DEFAULT 0;`
- `ALTER TABLE credit_memo ADD COLUMN IF NOT EXISTS credit_memo_refunded_total DECIMAL(15,2) NOT NULL DEFAULT 0;`
- `refund` — hybrid PK, `record_type`/`refund_status` FKs into the lookups (both reused, no seed rows added), customer FK (required), payment/credit_memo FKs (nullable, lineage of "typical source" plus what `Create` may pre-apply against), invoice FK (nullable, `ON DELETE SET NULL`, lineage only), refund method FK (reused `lkp_payment_method`), money summary (`refund_amount`, `refund_applied_total`, `refund_unapplied_amount`), `refund_custom_fields JSONB NOT NULL DEFAULT '{}'`, paired soft delete, `refund_record_version`.
- `refund_application` — `refund_id NOT NULL` + nullable `payment_id`/`credit_memo_id` with `chk_refund_app_xor_source`, `application_amount`, soft-delete pair, `uq_refund_app_live_pair` (partial unique, one live row per refund + source).
- `refund_history` — mirrors `payment_history`/`credit_memo_history`.

**Conventions enforced:** money `DECIMAL(15,2)`; bare `TIMESTAMP` (v2, **not** `TIMESTAMPTZ`); no triggers; `refund_owner_id INTEGER NULL REFERENCES employee(employee_id)` with **no team column** (v2 modules pass `""` for teamID to `recordInScope`).

**Idempotency:** every statement is `CREATE TABLE IF NOT EXISTS` / `CREATE INDEX IF NOT EXISTS` / `ADD COLUMN IF NOT EXISTS`. The whole file runs in **one transaction** on every tenant boot, so `CREATE INDEX CONCURRENTLY`, `VACUUM`, `DROP`, `TRUNCATE`, type-changing `ALTER COLUMN`, and renames are all forbidden. No down-migration — recovery is Neon PITR.

**Seeds:** none. `RFND` (id 10) and its four statuses already exist; nothing appended to `lkp_record_type` or `lkp_record_status`.

**Indexes:** partial on live rows (`WHERE refund_deleted_at IS NULL`), named `idx_rfnd_*`. Every column exposed as **sortable** in `resolver.go` gets a `(column, id)` composite for keyset pagination.

---

## 6. Foreign Key Relationships

| From | To | Nullability | On delete | Why |
|---|---|---|---|---|
| `refund.refund_customer_id` | `customer` | NOT NULL | restrict (default) | A refund always belongs to a customer. |
| `refund.refund_payment_id` | `payment` | NULL | `SET NULL` | The typical/primary source when refunding an overpayment (lineage; money moves only through `refund_application`). |
| `refund.refund_credit_memo_id` | `credit_memo` | NULL | `SET NULL` | The typical/primary source when cashing out a credit (lineage; money moves only through `refund_application`). |
| `refund.refund_invoice_id` | `invoice` | NULL | `SET NULL` | Lineage only (AD-12). |
| `refund_application.refund_id` | `refund` | NOT NULL | `CASCADE` | Ledger rows are owned by the header. |
| `refund_application.payment_id` | `payment` | NULL (XOR with credit_memo_id) | restrict | Not cascade — a payment must not be hard-deletable out from under a live refund application. |
| `refund_application.credit_memo_id` | `credit_memo` | NULL (XOR with payment_id) | restrict | Same reasoning, credit-memo side. |
| `refund_history.refund_id` | `refund` | NOT NULL | `CASCADE` | Trail dies with the record. |
| `*_created_by` / `_updated_by` / `_deleted_by` / `_owner_id` | `employee` | NULL | restrict | v2 audit convention. |

---

## 7. Status Transition Rules (service layer)

```
PEND ──approve──> APPV ──transition──> SENT
  │                 │
  └──> VOID <───────┘
```

`refund/transitions.go`:
```go
var allowedTransitions = map[string]map[string]bool{
    "PEND": {"APPV": true, "VOID": true},
    "APPV": {"SENT": true, "VOID": true},
    "SENT": {},
    "VOID": {},
}
```

- `PEND → APPV` requires **`refund:approve`** (AD-4); all others require `refund:transition`.
- `APPV → SENT` is user-directed (unlike Credit Memo's derived `APPV→APPL` — a refund's "money physically returned" is a real-world event the caller reports, not something inferable from the applied/unapplied split, since a refund can be fully applied to a source and still not yet be in the customer's hands).
- `SENT → VOID` is **absent by design** (AD-3) — once issued, terminal.
- Denied transition → **409**.

---

## 8. Money, Application & Rollup Rules

1. `round2` on every stored money value (`payment/money.go` convention; `float64` + explicit rounding).
2. **Apply** (`refund.Apply`), in one tx, locks **refund then its one source** (AD-7):
   - amount must be `> 0` → else 400.
   - refund must be `APPV` or `SENT`... actually must be **`APPV`** (not yet `SENT` — money isn't out until fully resolved) → else 409 (AD-5).
   - exactly one of `paymentUUID`/`creditMemoUUID` provided → else 400.
   - source customer must equal refund customer → else 400.
   - if source is `payment`: reject if payment status is `VOID` → 400.
   - if source is `credit_memo`: reject unless credit-memo status is `APPV` or `APPL` → 400 (a `DRFT` or `VOID` credit memo authorizes nothing).
   - amount must be `<= min(refund.unapplied_amount, source.available)` where `source.available = source.unapplied_amount - source.refunded_total` → else **400, not clamped** (AD-6).
   - upsert the live `(refund_id, payment_id|credit_memo_id)` row — **increment** if present, per `uq_refund_app_live_pair`.
   - `recomputeRefund` → `refund_applied_total`/`refund_unapplied_amount`.
   - direct `UPDATE` of the source's `refunded_total` column (AD-2) — no import of `payment`/`creditmemo` Go packages.
   - `refund_history` row, action `apply`.
3. **Unapply** — symmetric; soft-deletes the application row and recomputes both sides.
4. **Void cascade** (reachable only from `PEND`/`APPV`) — reverses every live application, credit-memo-before-payment (AD-7), recomputing each side.
5. Refund itself never writes `payment_status`, `credit_memo_status`, `payment_applied_total`, or `credit_memo_applied_total` — only its own two bolted-on `*_refunded_total` columns.

---

## 9. API Contracts

All under `/api/tenant/refunds`, all wrapped in `tenantChain` (`main.go:370`). All ids are UUIDs. All responses `{success: bool, ...}`.

| Method | Path | Permission | Notes |
|---|---|---|---|
| GET | `/refunds` | `refund:read` + scope | keyset paginated |
| POST | `/refunds/search` | `refund:read` + scope | `query/` filter body |
| POST | `/refunds` | `refund:create` | 201 |
| GET | `/refunds/{uuid}` | `refund:read` + IDOR | 404 on scope denial |
| PATCH | `/refunds/{uuid}` | `refund:update` + IDOR | amount/source immutable post-create (AD-8) |
| DELETE | `/refunds/{uuid}` | `refund:delete` + IDOR | soft; 409 if live applications (AD-9) |
| POST | `/refunds/{uuid}/transition` | `refund:approve` for `->APPV`, else `refund:transition` | 409 on denied transition |
| POST | `/refunds/{uuid}/apply` | `refund:update` + **`payment:update` or `credit_memo:update`** on target | AD-6 |
| POST | `/refunds/{uuid}/unapply` | `refund:update` + **`payment:update` or `credit_memo:update`** on target | |
| GET | `/refunds/{uuid}/audit` | `refund:read` + IDOR | last 200 `audit_logs` rows |
| GET | `/payments/{uuid}/refunds` | `payment:read` + IDOR | refunds applied against a payment (optional reconciliation view) |
| GET | `/credit-memos/{uuid}/refunds` | `credit_memo:read` + IDOR | refunds applied against a credit memo (optional reconciliation view) |

Status codes: 400 invalid input / over-apply / `*query.InvalidFilterError`; 401 unauthenticated; 403 permission denied; **404 scope denial** (never 403 — ids must not be enumerable); 409 invalid transition / delete with live applications; 500 otherwise.

---

## 10. Listing & Query Architecture

Identical pattern to Payment §10 / Credit Memo §10 — routed entirely through `query/` via `refund/resolver.go` (table alias **`rfnd`**). No hand-rolled filtering.

- **Filter × scope is ANDed, never OR.**
- Field keys are a **whitelist**; unresolved → 400, never raw SQL. All values `$n`-parameterized.
- **Keyset pagination only** (opaque base64 cursor). `MaxLimit` 100, default 25.
- `sortFields` is a **separate, narrower whitelist** than `systemFields`: nullable columns are filterable but never sortable.
- `cf:<key>` reaches into `refund_custom_fields`, regex-guarded.

Filterable per the brief: `refund_date`, `customer_id`, `status`, `refund_amount`, `unapplied_amount`, `payment_id`, `credit_memo_id`, `invoice_id`, `method_id`, `owner_id`, plus `cf:` keys.

---

## 11. Validation Rules

- `customer_uuid` required and resolvable → else 400.
- `amount > 0` → else 400 (DB CHECK backstops).
- `CreateRefundInput` carries no inline `Applications` list. Per AD-5, `Apply` requires the refund be `APPV`, and a new refund always starts `PEND` — an inline application at create time would always be rejected, so the field is omitted from the wire contract entirely rather than accepted and always failing. Approve, then call `POST .../apply` (once per source) to compose a refund from a payment, a credit memo, or both.
- `custom_fields` validated against `workflow_field_definitions` for the `refund` workflow before save; **max 15 keys**.
- **Nil-map guard**: `if custom == nil { custom = map[string]any{} }` on both create and update.
- `actorOrSystem()` — never `nullableInt` — for any `_deleted_by` paired with a CHECK.

---

## 12. Backend Implementation Map

**New — `refund/`:** `types.go`, `money.go`, `numbering.go` (`numberPrefix = "RFND"` → `RFND-000001`), `transitions.go`, `resolver.go`, `search.go`, `store.go`, `store_create.go`, `store_update.go`, `store_transition.go`, `apply.go` + `money_test.go`, `numbering_test.go`, `transitions_test.go`, `resolver_test.go`, `store_test.go` (dbtest).

**New — controllers:** `refund.go`, `refund_transition.go`, `refund_audit.go`.

**Modified:** `database/migrations/tenant/schema.sql` (append + two `ADD COLUMN`) · `main.go` (~10-12 routes) · `authz/catalog.go` (one row, AD-4) · `.claude/hooks/check-module-package.sh` (add `refund` to the guarded-directory list).

**Unchanged:** `payment/` and `creditmemo/` Go code (only their tables gain a column via `ALTER`, written exclusively by `refund/`) · `controllers/crm.go` (refund is not on the JSONB generic router, same as payment/credit_memo) · both lookup seeds · `query/` (stays dependency-free).

### RBAC — roles are tenant data, not code

There is no role seed in this repo; roles are per-tenant rows in `roles`. Only `super_admin` is a system role, holding wildcard `('*','*','all')`. The requested split (from the original brief) is delivered as a **grant recipe** via `POST /api/tenant/rbac/roles`:

| Role | Grants (`resource`, `action`, `scope`) |
|---|---|
| `customer_support` | `refund` × {create, read, update} @ **own** — can initiate/draft, cannot self-approve |
| `finance_manager` | `refund` × {create, read, update, delete, transition, **approve**} @ **all**, plus `payment` × {read, update} @ all and `credit_memo` × {read, update} @ all — required, because apply/unapply mutate those sources |
| `admin` | the existing `super_admin` system role — already match-all |

`customer_support` deliberately holds no `approve`, so an initiated refund cannot authorize its own money movement.

---

## 13. Open Decisions — Resolved During Planning

1. **Gateway/webhook infrastructure from the original brief** → does not exist in this codebase anywhere; refund is record-only, matching every sibling module. *Resolved: record-only.*
2. **What a refund draws against** → both `payment` (overpayment) and `credit_memo` (cash-out), via one XOR-constrained ledger. *Resolved: dual source.*
3. **Line-item tracking** → amount-level only; the source document already carries line detail. *Resolved: no `refund_items` table.*
4. **Lifecycle naming** → adopt the seeded `PEND/APPV/SENT/VOID` (record_type 10) rather than inventing new codes; the seed is append-only. *Resolved: seeded lifecycle reused verbatim.*
5. **How to split submit from approve** → new `refund:approve` catalog row (AD-4), mirroring Credit Memo AD-8. *Resolved: permission.*
6. **Where the `refunded_total` rollup lives** → bolted directly onto `payment`/`credit_memo` tables, written only by `refund/`, no shared-invariant extraction needed (single writer, unlike invoice's rollup). *Resolved: two columns, refund-owned.*

## 14. Deliberately Out of Scope

Payment-gateway integration and inbound webhooks (AD-10) · GL or double-entry ledger (AD-10) · multi-approver sign-off tables (mirrors Credit Memo's rejection of this for the same reason) · multi-currency conversion (AD-11) · frontend.
