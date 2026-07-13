# Payments Module — Backend Design Spec

**Date:** 2026-07-13
**Status:** Draft — approved by user in brainstorming session, proceeding to implementation plan.
**Scope:** New Payment module (header + invoice-application ledger + status workflow) for the StoneSuite multi-tenant, database-per-tenant CRM/ERP backend. Formalizes the AR-balance design the Invoice module (`docs/superpowers/specs/2026-07-10-invoice-module-design.md`) deliberately deferred (its AD-5 / Open Decision #2).

---

## 1. Overview & Goals

Add a production-grade **Payment** module — customer payments received and applied against invoices — as a sibling of `invoice`/`sales_order`, following the same v2 relational conventions: hybrid PK, employee-based audit, soft delete, `record_version`, RBAC/scope/IDOR, the `query/` filter engine, keyset pagination.

**Phased scope (explicitly confirmed):** this pass builds **Payments only** — customer payments received and applied against invoices. Credit Memo (`CRDT`, record_type=9) and Refund (`RFND`, record_type=10) are out of scope; their `lkp_record_type` rows and status lifecycles are already seeded (from the original catalog work) but no tables/code are added for them here. This mirrors the phasing discipline the Invoice module itself used for Payment.

**Non-negotiable constraints (from CLAUDE.md, identical to Invoice/Sales Order):**

- Database-per-tenant; no `tenant_id` column anywhere.
- v2 relational conventions: hybrid PK (`SERIAL` + `UUID`), `employee(employee_id)`-based audit columns, paired soft delete, `record_version` optimistic concurrency.
- Idempotent, append-only migrations (`CREATE TABLE IF NOT EXISTS`, `ON CONFLICT DO NOTHING`).
- Mandatory security chain on every `/api/tenant/` route.
- All list/search goes through `query/` (whitelisted `FieldResolver`, parameterized values, keyset pagination, filter × scope ANDed).

### What already exists (reuse, do not recreate)

| Concern | Existing asset | Location |
|---|---|---|
| Billing customer | `customer` (v2 relational master) | `tenant/schema.sql:1087` |
| Actor / owner | `employee` | `tenant/schema.sql:511` |
| Invoice (application target) | `invoice`, `invoice_item` | `invoice/` package + `tenant/schema.sql` |
| Record type "Payment" | `lkp_record_type` row `PYMT` / `payment` (id 8) — **already seeded, unused until now** | `tenant/schema.sql:698` |
| Payment status lifecycle | `lkp_record_status` rows for `record_type=8`: `PEND, APPV, DEPO, VOID` — **already seeded, unused until now** | `tenant/schema.sql:738` |
| RBAC pattern | `authz/catalog.go` — `ResourcePayment` (5 actions) **already seeded, unused until now** | `authz/catalog.go:41,150-154` |
| Filter/sort/paginate/search | `query/` package | `query/` |
| Money/line calc pattern | `invoice/calc.go` (round-to-2dp helper, stored-not-recomputed convention) | `invoice/calc.go` |
| Status transition pattern | `invoice/transitions.go` (static Go map) | `invoice/transitions.go` |
| Document numbering pattern | `invoice/numbering.go` (Go post-insert format) | `invoice/numbering.go` |
| Audit log | `audit_logs` (v2 path) via `workflow.LogAuditFull` | `controllers/invoice_audit.go` (pattern) |
| Row-level IDOR guard | `recordInScope` | `controllers/scope.go` |

> **Key finding that shaped this design:** `lkp_record_type` and `lkp_record_status` already carry fully-formed lifecycles for `PYMT` (`PEND → APPV → DEPO`, or `VOID`), `CRDT`, and `RFND` — seeded ahead of any code using them. This confirms Payment, Credit Memo, and Refund were always intended as distinct entities (not sub-states of Invoice), and validates building Payment now as a genuine sibling module rather than continuing to bolt AR fields onto the invoice header.

### What is genuinely missing (new tables — justified in §3)

- `payment`, `payment_application`, `payment_history`, `lkp_payment_method` — no existing table can represent money received from a customer and its (possibly split, possibly partial, possibly deferred) application against one or more invoices.

---

## 2. Architecture Decisions

**AD-1 — Dedicated relational tables, not the JSONB engine.** Same reasoning as Invoice/Sales Order: a money ledger with cross-invoice application doesn't fit the 15-field JSONB `workflow_records` model.

**AD-2 — `payment` is a sibling of `invoice`, connected only through the junction table `payment_application`.** A payment is not owned by any single invoice — it belongs to a customer and may fund zero, one, or several invoices over its lifetime. This is the "one payment, many invoices, with unapplied balance" allocation model.

**AD-3 — Hybrid PK everywhere.** Identical to `customer`/`invoice`: `SERIAL` internal, `UUID` external.

**AD-4 — `payment_application` is the ledger of record; `payment` and `invoice` header money columns are stored rollups derived from it.** This reverses Invoice's AD-5 (which made the invoice header authoritative because no Payment table existed yet). Now:
- `payment.payment_applied_total` / `payment_unapplied_amount` = derived from `SUM(payment_application.application_amount)` for that payment's live applications.
- `invoice.invoice_amount_paid` / `invoice_balance_due` = derived from `SUM(payment_application.application_amount)` for that invoice's live applications (across all payments that touch it).

Both are **stored, not recomputed on read** — every apply/unapply/void recomputes and writes both sides transactionally under row locks (`FOR UPDATE`), matching the project's "store computed money on rows" convention (invoice AD-5, salesorder AD-5).

**AD-5 — `invoice.RecordPayment` is retired; `POST /api/tenant/invoices/{uuid}/payment` becomes a thin wrapper over the Payment module.** The endpoint path and request/response shape are unchanged for API compatibility, but internally it now calls `payment.QuickPay` (creates a `payment` row at status `APPV` + one `payment_application` row against that invoice) instead of writing `invoice_amount_paid` directly. There is exactly one code path that ever mutates AR balances after this change.

**AD-6 — Status transitions enforced in the Go service layer over the shared lookup**, identical pattern to `invoice/transitions.go`. Reuses `lkp_record_status` (`record_type=8`, already seeded). Denied transitions → HTTP 409.

**AD-7 — Applying/unapplying invoices is decoupled from the payment's own approval lifecycle.** A payment can be applied to invoices while `PEND` (money received but not yet bookkeeper-approved) — approval (`PEND→APPV`) and deposit tracking (`APPV→DEPO`) are separate concerns from whether the payment currently offsets AR. Only `VOID` blocks application (a voided payment holds no money). This is deliberately looser than Invoice's `payableStatuses` gate (which restricts *receiving* payment to `SENT/PART/ODUE`) because here the direction is reversed: this module is the one recording money in, not the one being paid.

**AD-8 — Each `payment_application` row caps at the invoice's live `balance_due`; it can never push balance negative.** Rejected (400) at the amount that would overpay, not silently clamped. Excess stays as `payment_unapplied_amount` on the payment, available to apply elsewhere or later.

**AD-9 — Voiding a payment cascades: every live application on it is reversed in the same transaction**, each affected invoice's rollup + status recomputed (mirrors and reuses the same recompute path `apply`/`unapply` use). Symmetric with how Invoice already auto-transitions on payment recording.

**AD-10 — Payment amount is immutable after creation.** `PATCH` may edit non-monetary fields (method, reference number, date, memo) but not `payment_amount` — changing the total once applications may exist against it would require re-deriving every downstream invoice balance under a much larger lock scope for a rare correction. To fix a wrong amount: void the payment (cascades cleanly) and create a new one.

**AD-11 — Deleting a payment is blocked while it has live applications.** Must `unapply` (or `void`, which unapplies everything) first. This keeps every visible `payment_application` row's parent payment always resolvable — no orphaned ledger entries pointing at a hidden header.

**AD-12 — No exchange rate on Payment.** `payment_currency` (nullable FK to `lkp_currency`) is carried for display only. Application amounts are compared numerically against invoice `balance_due` with no currency conversion — same currency is assumed. Multi-currency AR reconciliation is out of scope (YAGNI; nothing today requires it, and invoice's own `exchange_rate` is unused by any conversion logic yet either).

---

## 3. New Tables — Per-Table Justification

### `lkp_payment_method`
- **(a)** No existing lookup models how a payment was received.
- **(b)** Small system-seeded lookup: Check, Cash, Credit Card, ACH, Wire, Other.
- **(c)** New lookup, same shape as `lkp_payment_terms`/`lkp_price_level`.

### `payment`
- **(a)** No existing table models money received from a customer independent of any single invoice.
- **(b)** The payment header: identity, classification, customer, method/reference, stored applied/unapplied rollup.
- **(c)** New master (hybrid PK). FKs to `customer`, `employee`, `lkp_record_type`(=PYMT), `lkp_record_status`, `lkp_payment_method`, `lkp_currency`.

### `payment_application`
- **(a)** The payment↔invoice many-to-many ledger; no existing table fits.
- **(b)** One row per live allocation of a payment's money to an invoice's balance.
- **(c)** New junction; FKs to `payment` (CASCADE) and `invoice` (RESTRICT).

### `payment_history`
- **(a)** Generic `audit_logs` covers CRUD; the status lifecycle + apply/unapply events deserve a typed trail like `invoice_history`.
- **(b)** One row per status change / apply / unapply, with `from_status_id`/`to_status_id` + JSONB snapshot.
- **(c)** New child of `payment`; FKs to `lkp_record_status`, `employee`.

---

## 4. ER Diagram (text)

```
   lkp_currency      customer         employee        lkp_record_type/status    lkp_payment_method
        │(0..1)          │(1)             │(1)                 │(1)                   │(1)
        │        ┌───────┼─────────────────┼─────────────────────┼───────────────────────┤
        ▼(N)     ▼(N)                                                                    ▼(N)
 ┌──────────────────────────────────────────────────────────────────────────────────────────┐
 │                                    payment  (header, NEW)                                  │
 │  PK payment_id (serial) · UUID payment_uuid · payment_number "PYMT-000001"                 │
 │  FK customer_id · record_type=PYMT · payment_status                                        │
 │  payment_amount · payment_applied_total (rollup) · payment_unapplied_amount (rollup)        │
 └───────────┬───────────────────────────────────────────────┬───────────────────────────────┘
             │(1)                                             │(1)
     (N)     ▼                                         (N)    ▼
 ┌────────────────────────────────┐                ┌─────────────────────────────┐
 │  payment_application  (NEW)    │                ┌►│  payment_history  (NEW)    │
 │  PK application_id             │                │ │  from_status_id/to_status_id│
 │  FK payment_id (CASCADE)       │                │ │  action (create|transition │
 │  FK invoice_id (RESTRICT)  ────┼──► invoice     ┘ │   |apply|unapply)          │
 │  application_amount            │                  │  actor_employee_id·snapshot│
 │  paired soft delete = "unapplied"│                └─────────────────────────────┘
 └────────────────────────────────┘
```

**Cardinality summary**
- `customer` 1 ─── N `payment`.
- `payment` 1 ─── N `payment_application`, 1 ─── N `payment_history`.
- `invoice` 1 ─── N `payment_application` (an invoice may be paid by several payments; a payment may fund several invoices).
- `invoice.invoice_amount_paid`/`invoice_balance_due` are now derived from `payment_application`, not written directly by any invoice-side code.

---

## 5. SQL — CREATE TABLE Statements

> Appended to `database/migrations/tenant/schema.sql` via the **add-migration** skill, after the invoice/invoice_item/invoice_history block (payment_application FKs `invoice`). Same numeric standards: money `DECIMAL(15,2)`.

### 5.1 `lkp_payment_method`

```sql
CREATE TABLE IF NOT EXISTS lkp_payment_method (
    payment_method_id          SERIAL       PRIMARY KEY,
    payment_method_name        VARCHAR(50)  NOT NULL,
    payment_method_code        VARCHAR(10)  NOT NULL,
    payment_method_is_active   BOOLEAN      NOT NULL DEFAULT TRUE,
    payment_method_is_system   BOOLEAN      NOT NULL DEFAULT FALSE,
    payment_method_created_at  TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    payment_method_created_by  INTEGER      NOT NULL REFERENCES employee(employee_id),
    payment_method_deleted_at  TIMESTAMP        NULL,
    payment_method_deleted_by  INTEGER          NULL REFERENCES employee(employee_id),
    payment_method_record_version INTEGER   NOT NULL DEFAULT 1,
    CONSTRAINT uq_payment_method_code UNIQUE (payment_method_code)
);

INSERT INTO lkp_payment_method (payment_method_name, payment_method_code, payment_method_is_active, payment_method_is_system, payment_method_created_by) VALUES
    ('Check',       'CHK_', TRUE, TRUE, 1),
    ('Cash',        'CASH', TRUE, TRUE, 1),
    ('Credit Card', 'CC__', TRUE, TRUE, 1),
    ('ACH',         'ACH_', TRUE, TRUE, 1),
    ('Wire',        'WIRE', TRUE, TRUE, 1),
    ('Other',       'OTHR', TRUE, TRUE, 1)
ON CONFLICT (payment_method_code) DO NOTHING;
```

### 5.2 `payment` (header)

```sql
CREATE TABLE IF NOT EXISTS payment (
    payment_id                  SERIAL        PRIMARY KEY,
    payment_uuid                 UUID          NOT NULL DEFAULT gen_random_uuid(),
    payment_number                VARCHAR(20)      NULL,  -- 'PYMT-000001', generated post-insert in Go

    -- Classification
    record_type                   INTEGER       NOT NULL REFERENCES lkp_record_type(record_type_id),   -- = PYMT
    payment_status                 INTEGER       NOT NULL REFERENCES lkp_record_status(record_status_id),

    -- Source
    payment_customer_id            INTEGER       NOT NULL REFERENCES customer(customer_id),

    -- Primary info
    payment_method                  INTEGER       NOT NULL REFERENCES lkp_payment_method(payment_method_id),
    payment_reference_number        VARCHAR(50)   NOT NULL DEFAULT '',
    payment_date                     DATE          NOT NULL DEFAULT CURRENT_DATE,
    payment_currency                 INTEGER           NULL REFERENCES lkp_currency(currency_id),
    payment_memo                      TEXT          NOT NULL DEFAULT '',
    payment_internal_notes            TEXT          NOT NULL DEFAULT '',

    -- Money (stored)
    payment_amount                     DECIMAL(15,2) NOT NULL,
    payment_applied_total               DECIMAL(15,2) NOT NULL DEFAULT 0,  -- rollup, recomputed on apply/unapply
    payment_unapplied_amount             DECIMAL(15,2) NOT NULL DEFAULT 0, -- rollup, = amount - applied_total

    -- Assignment (drives own/team scope filtering, same role as invoice_owner_id)
    payment_owner_id                      INTEGER           NULL REFERENCES employee(employee_id),

    -- Dynamic + audit
    payment_custom_fields                  JSONB        NOT NULL DEFAULT '{}',
    payment_created_at                      TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    payment_created_by                       INTEGER          NULL REFERENCES employee(employee_id),
    payment_updated_at                        TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    payment_updated_by                         INTEGER          NULL REFERENCES employee(employee_id),
    payment_deleted_at                          TIMESTAMP        NULL,
    payment_deleted_by                           INTEGER          NULL REFERENCES employee(employee_id),
    payment_record_version                        INTEGER      NOT NULL DEFAULT 1,

    CONSTRAINT uq_payment_uuid       UNIQUE (payment_uuid),
    CONSTRAINT uq_payment_number     UNIQUE (payment_number),
    CONSTRAINT chk_payment_amount_pos      CHECK (payment_amount > 0),
    CONSTRAINT chk_payment_applied_nonneg  CHECK (payment_applied_total >= 0 AND payment_unapplied_amount >= 0),
    CONSTRAINT chk_payment_applied_le_amt  CHECK (payment_applied_total <= payment_amount),
    CONSTRAINT chk_payment_unapplied_eq    CHECK (payment_unapplied_amount = payment_amount - payment_applied_total),
    CONSTRAINT chk_payment_soft_delete     CHECK (
        (payment_deleted_at IS NULL AND payment_deleted_by IS NULL) OR
        (payment_deleted_at IS NOT NULL AND payment_deleted_by IS NOT NULL)
    )
);
```

### 5.3 `payment_application` (invoice-application ledger)

```sql
CREATE TABLE IF NOT EXISTS payment_application (
    application_id             SERIAL        PRIMARY KEY,
    application_uuid            UUID          NOT NULL DEFAULT gen_random_uuid(),
    payment_id                   INTEGER       NOT NULL REFERENCES payment(payment_id) ON DELETE CASCADE,
    invoice_id                    INTEGER       NOT NULL REFERENCES invoice(invoice_id),

    application_amount             DECIMAL(15,2) NOT NULL,

    application_created_at          TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    application_created_by           INTEGER          NULL REFERENCES employee(employee_id),
    application_deleted_at            TIMESTAMP        NULL,  -- set = "unapplied"
    application_deleted_by             INTEGER          NULL REFERENCES employee(employee_id),
    application_record_version          INTEGER      NOT NULL DEFAULT 1,

    CONSTRAINT uq_payment_application_uuid UNIQUE (application_uuid),
    CONSTRAINT chk_pay_app_amount_pos      CHECK (application_amount > 0),
    CONSTRAINT chk_pay_app_soft_delete     CHECK (
        (application_deleted_at IS NULL AND application_deleted_by IS NULL) OR
        (application_deleted_at IS NOT NULL AND application_deleted_by IS NOT NULL)
    )
);

-- At most one LIVE application per (payment, invoice) pair -- re-applying
-- increases the existing row's amount instead of creating a duplicate.
CREATE UNIQUE INDEX IF NOT EXISTS uq_pay_app_live_pair
    ON payment_application (payment_id, invoice_id) WHERE application_deleted_at IS NULL;
```

### 5.4 `payment_history`

```sql
CREATE TABLE IF NOT EXISTS payment_history (
    payment_history_id        SERIAL       PRIMARY KEY,
    payment_id                 INTEGER      NOT NULL REFERENCES payment(payment_id) ON DELETE CASCADE,
    from_status_id               INTEGER          NULL REFERENCES lkp_record_status(record_status_id),
    to_status_id                  INTEGER          NULL REFERENCES lkp_record_status(record_status_id),
    action                          VARCHAR(32)  NOT NULL DEFAULT 'transition', -- create | transition | apply | unapply | update
    actor_employee_id                INTEGER          NULL REFERENCES employee(employee_id),
    snapshot                          JSONB        NOT NULL DEFAULT '{}',
    at                                 TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

### 5.5 Indexes

```sql
-- payment (listing/filtering — all partial on live rows)
CREATE INDEX IF NOT EXISTS idx_pay_customer      ON payment (payment_customer_id) WHERE payment_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_pay_status         ON payment (payment_status)      WHERE payment_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_pay_date            ON payment (payment_date)        WHERE payment_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_pay_owner            ON payment (payment_owner_id)    WHERE payment_deleted_at IS NULL;
-- Keyset pagination tiebreakers (per sortable column + id)
CREATE INDEX IF NOT EXISTS idx_pay_created_id      ON payment (payment_created_at, payment_id)  WHERE payment_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_pay_updated_id      ON payment (payment_updated_at, payment_id)  WHERE payment_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_pay_date_id          ON payment (payment_date, payment_id)         WHERE payment_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_pay_amount_id         ON payment (payment_amount, payment_id)       WHERE payment_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_pay_unapplied_id       ON payment (payment_unapplied_amount, payment_id) WHERE payment_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_pay_status_created       ON payment (payment_status, payment_created_at, payment_id) WHERE payment_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_pay_custom_gin            ON payment USING GIN (payment_custom_fields);

-- payment_application
CREATE INDEX IF NOT EXISTS idx_pay_app_payment  ON payment_application (payment_id) WHERE application_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_pay_app_invoice  ON payment_application (invoice_id) WHERE application_deleted_at IS NULL;

-- payment_history
CREATE INDEX IF NOT EXISTS idx_pay_history_payment ON payment_history (payment_id);
```

> **Migration ordering:** this whole block is appended after the invoice/invoice_item/invoice_history block, since `payment_application` FKs `invoice`.

---

## 6. Foreign Key Relationships (explained)

| Child column | → Parent | Meaning | On delete |
|---|---|---|---|
| `payment.payment_customer_id` | `customer.customer_id` | Who paid | RESTRICT |
| `payment.record_type` | `lkp_record_type.record_type_id` | Always `PYMT` | RESTRICT |
| `payment.payment_status` | `lkp_record_status.record_status_id` | Lifecycle status (`record_type=8` set) | RESTRICT |
| `payment.payment_method` | `lkp_payment_method.payment_method_id` | How they paid | RESTRICT |
| `payment.payment_currency` | `lkp_currency.currency_id` | Display only, nullable | RESTRICT |
| `payment.payment_owner_id` | `employee.employee_id` | Scope assignment | RESTRICT |
| `payment_application.payment_id` | `payment.payment_id` | Owning payment | **CASCADE** |
| `payment_application.invoice_id` | `invoice.invoice_id` | Target invoice | RESTRICT |
| `payment_history.payment_id` | `payment.payment_id` | Owning payment | **CASCADE** |
| `payment_history.from_status_id`/`to_status_id` | `lkp_record_status.record_status_id` | Transition endpoints | RESTRICT |

No cross-database FKs. No `tenant_id` anywhere.

---

## 7. Status Transition Rules (service layer)

Statuses come from `lkp_record_status` where `record_status_record_type = 8` (PYMT): `PEND, APPV, DEPO, VOID` — already seeded.

```
PEND (Pending) ──▶ APPV (Approved) ──▶ DEPO (Deposited)  [terminal]
   │                    │
   └──────────▶ VOID ◀──┘
              [terminal]
```

```go
var allowedPaymentTransitions = map[string]map[string]bool{
    "PEND": {"APPV": true, "VOID": true},
    "APPV": {"DEPO": true, "VOID": true},
    "DEPO": {},   // terminal
    "VOID": {},   // terminal
}
```

- New payments start at **`PEND`**.
- Applying/unapplying to invoices is allowed at `PEND`, `APPV`, or `DEPO` (AD-7) — blocked only at `VOID`.
- Transitioning to `VOID` from any non-terminal status cascades: every live `payment_application` on it is reversed in the same transaction (AD-9).

---

## 8. Money, Application & Rollup Rules

**Applying a payment to an invoice (`Apply`, transactional, `FOR UPDATE` on both `payment` and `invoice` rows):**
```
1. Lock payment row; reject if status == VOID.
2. Lock invoice row; reject if invoice soft-deleted or customer_id != payment.customer_id.
3. cap = min(requestedAmount, payment.unapplied_amount, invoice.balance_due)
   if requestedAmount > cap: reject 400 ("exceeds available balance")
4. Upsert payment_application: if a live row exists for (payment_id, invoice_id), increase its
   amount by requestedAmount; else insert a new row with amount = requestedAmount.
5. Recompute payment.applied_total = SUM(live applications for this payment); unapplied_amount = amount - applied_total.
6. Recompute invoice.amount_paid = SUM(live applications for this invoice, across all payments); balance_due = grand_total - amount_paid.
7. Auto-transition invoice status exactly as invoice.RecordPayment did (balance_due == 0 -> PAID;
   0 < balance_due < grand_total -> PART, only from SENT/ODUE/PART).
8. Insert payment_history (action='apply') and invoice_history (action='payment') rows.
```

**Unapplying (`Unapply`, transactional):**
```
1. Lock the live payment_application row for (payment_id, invoice_id); 404 if none.
2. Soft-delete it (application_deleted_at/by).
3. Recompute payment.applied_total/unapplied_amount (step 5 above).
4. Recompute invoice.amount_paid/balance_due (step 6 above); re-derive invoice status
   (balance restored -> may move PAID back to PART, or PART back to SENT/ODUE as appropriate).
5. Insert history rows (action='unapply') on both sides.
```

**Voiding (`Transition` to VOID, transactional):**
```
1. Lock payment row.
2. For every live payment_application row on this payment: run the Unapply steps (2-4 above)
   against its invoice, inside the same transaction.
3. Set payment.status = VOID (applied_total/unapplied_amount will be 0/amount after step 2).
4. Insert payment_history (action='transition').
```

**QuickPay (legacy-endpoint wrapper, `POST /invoices/{uuid}/payment`):**
```
1. Create a payment: customer = invoice.customer, amount = requested amount, method = 'Other',
   status = APPV (skips PEND — this endpoint implies money already confirmed, matching the old
   endpoint's one-step semantics).
2. Apply it to the given invoice for min(amount, invoice.balance_due) — reject 400 if requested
   amount exceeds balance_due (same "no silent clamp" rule invoice.RecordPayment used).
3. Return the invoice (same response shape as before: amountPaid/balanceDue/status).
```

---

## 9. API Contracts

All under `/api/tenant/`, through `tenantChain`, RBAC-checked in-handler, IDOR-guarded (404 on scope denial), same envelope as Invoice (`{success, message?, ...}`).

| Method & path | Purpose | RBAC |
|---|---|---|
| `GET  /api/tenant/payments` | Simple in-scope list, cursor-paginated | `payment:read` + scope |
| `POST /api/tenant/payments/search` | Full filter + sort + search + pagination | `payment:read` + scope |
| `GET  /api/tenant/payments/{uuid}` | Get one (+ live applications) | `payment:read` + IDOR |
| `POST /api/tenant/payments` | Create (header, optional inline `applications[]` applied in the same tx) | `payment:create` (+ `invoice:update` scope-check per inline application) |
| `PATCH /api/tenant/payments/{uuid}` | Edit non-monetary fields (method, reference, date, memo, notes) | `payment:update` + IDOR |
| `DELETE /api/tenant/payments/{uuid}` | Soft delete; 409 if live applications exist | `payment:delete` + IDOR |
| `POST /api/tenant/payments/{uuid}/transition` | Status change (validated map, §7); `VOID` cascades unapply | `payment:transition` + IDOR |
| `POST /api/tenant/payments/{uuid}/apply` | `{invoiceUuid, amount}` | `payment:update` + IDOR on payment, `invoice:update` scope-check on invoice |
| `POST /api/tenant/payments/{uuid}/unapply` | `{invoiceUuid}` | same as apply |
| `GET  /api/tenant/payments/{uuid}/audit` | Audit / history trail | `payment:read` + IDOR |
| `GET  /api/tenant/invoices/{uuid}/payments` | List live applications against one invoice (AR reconciliation view) | `invoice:read` + IDOR |
| `POST /api/tenant/invoices/{uuid}/payment` | **Unchanged path**, now a QuickPay wrapper (§8) | `invoice:update` + IDOR |

**Create request**
```json
POST /api/tenant/payments
{
  "customerUuid": "9d0f…c2",
  "methodId": 1,
  "referenceNumber": "Check #1042",
  "paymentDate": "2026-07-13",
  "currencyId": 1,
  "amount": 1500.00,
  "memo": "July payment",
  "customFields": {},
  "applications": [
    { "invoiceUuid": "6f2c…9a", "amount": 1000.00 }
  ]
}
→ 201 { "success": true, "payment": { "id": "…", "paymentNumber": "PYMT-000001",
        "status": "Pending", "amount": 1500.00, "appliedTotal": 1000.00,
        "unappliedAmount": 500.00, "applications": [...] } }
```

**Apply request**
```json
POST /api/tenant/payments/{uuid}/apply
{ "invoiceUuid": "6f2c…9a", "amount": 500.00 }
→ 200 { "success": true, "payment": { "appliedTotal": 1500.00, "unappliedAmount": 0.00 } }
→ 400 { "success": false, "message": "Amount exceeds available balance." }
```

**Unapply request**
```json
POST /api/tenant/payments/{uuid}/unapply
{ "invoiceUuid": "6f2c…9a" }
→ 200 { "success": true, "payment": { "appliedTotal": 1000.00, "unappliedAmount": 500.00 } }
```

---

## 10. Listing & Query Architecture (identical pattern to Invoice §11)

Reuses `query/` unchanged. New code: `paymentResolver` (`query.FieldResolver` + `SortResolver` + `SearchResolver`) and `Store.Search` (keyset).

**FieldResolver whitelist** (table alias `p` = `payment`):

| Logical key | SQL expression | DataType | Ops |
|---|---|---|---|
| `id` | `p.payment_uuid::text` | string | eq |
| `document_number` / `record_number` | `COALESCE(p.payment_number,'')` | string | eq, contains, startswith |
| `customer_id` | `p.payment_customer_id::text` | string | eq, in |
| `status` | `p.payment_status::text` | string | eq, in |
| `method_id` | `p.payment_method::text` | string | eq, in |
| `reference_number` | `p.payment_reference_number` | string | eq, contains, startswith |
| `payment_date` | `p.payment_date` | date | eq, gt, gte, lt, lte, between |
| `currency_id` | `p.payment_currency::text` | string | eq, in, is_null |
| `amount` / `applied_total` / `unapplied_amount` | respective `p.payment_*` | number | eq, gt, gte, lt, lte, between |
| `owner_id` | `p.payment_owner_id::text` | string | eq, in, is_null |
| `created_by` / `updated_by` | `p.payment_created_by::text` / `_updated_by::text` | string | eq, in, is_null |
| `created_at` / `updated_at` | `p.payment_created_at` / `_updated_at` | date | gte, lte, between |
| `cf:<key>` | `p.payment_custom_fields->>'<key>'` | per `workflow_field_definitions` | per type |

**SortResolver whitelist:** `document_number, payment_date, amount, unapplied_amount, status, customer_id, created_at, updated_at` — each `NOT NULL`, paired with `payment_id` tiebreaker.

**SearchPredicate:**
```sql
(   p.payment_number             ILIKE $n
 OR p.payment_reference_number   ILIKE $n
 OR p.payment_memo               ILIKE $n
 OR EXISTS (SELECT 1 FROM customer c
             WHERE c.customer_id = p.payment_customer_id
               AND (c.customer_name ILIKE $n OR c.customer_doc_num ILIKE $n)))
```

**Response envelope:** identical to Invoice — `{success, scope, records, nextCursor, hasMore}`. Keyset only, no offset.

---

## 11. Validation Rules

**Header**
- `customerUuid` required, must resolve to a live `customer` in this tenant; caller must have scope on it.
- `methodId` required, must reference a live, active `lkp_payment_method` row.
- `amount` > 0.
- `customFields` validated against a `payment` workflow's `workflow_field_definitions` if one exists (≤15, type/required/enum/regex) via `workflow.ValidateCustomFieldsPartial` — no-ops gracefully if no such workflow is seeded (mirrors `invoice.validateCustom`).

**Applications**
- `amount` > 0.
- Capped at `min(payment.unapplied_amount, invoice.balance_due)`; rejected (400) above that, never clamped.
- Rejected (400) if the invoice's customer doesn't match the payment's customer.
- Rejected (409) if payment status is `VOID`.
- Rejected (404, IDOR) if the caller's scope doesn't cover the target invoice.

**Transitions** — only moves in `allowedPaymentTransitions`; else 409. `VOID` cascades unapply (§8).

**Delete** — rejected (409) if any live `payment_application` row references this payment.

**Tenant/RBAC/IDOR** — identical to Invoice §12: `authz.Check(payment, action)` before every mutation; every single-record op scope/IDOR-guarded, 404 on denial, `idor_denied` logged; scope composed into SQL, never filtered in Go.

---

## 12. Backend Implementation Map

| Concern | Action | Reference to mirror |
|---|---|---|
| Schema | Append to `database/migrations/tenant/schema.sql` via **add-migration** skill, after invoice block | `invoice` block |
| RBAC | `ResourcePayment` already in `authz/catalog.go` — no change needed | — |
| Route registration | `main.go`, alongside Invoice routes | `main.go` invoice block |
| Controller | New `controllers/payment.go` (`PaymentOps`) mirroring `InvoiceOps` | `controllers/invoice.go` |
| Store | New `payment/store.go`, transactional create/update/get/list/delete/search | `invoice/store.go`, `invoice/store_create.go` |
| Apply/unapply/void service | `payment/apply.go` — `Apply`, `Unapply`, transactional recompute of both sides (§8) | `invoice/store_transition.go` `RecordPayment` (transaction shape) |
| QuickPay wrapper | `payment/quickpay.go` — `QuickPay(ctx, pool, invoiceUUID, amount, actorEmployeeID)`, called from the existing `controllers/invoice_transition.go` `RecordPayment` handler (kept, reimplemented) | — |
| Resolver | New `payment/resolver.go` | `invoice/resolver.go` |
| Transitions | `payment/transitions.go` | `invoice/transitions.go` |
| Numbering | `payment/numbering.go` — `PYMT-%06d` | `invoice/numbering.go` |
| Audit | `payment_history` write helper, same shape as `invoice_history` | `controllers/invoice_audit.go` |
| Tests | Table-driven for `transitions`/`numbering`/apply-cap math; `dbtest`-tagged integration tests for store/apply/unapply/void; filter-invariant tests for resolver | `invoice/*_test.go` |
| Security review | **tenancy-security-reviewer**, **migration-auditor**, **filter-invariant-checker**, **feature-dev:code-reviewer**, **code-simplifier** before merge | — |

---

## 13. Open Decisions — Resolved During Brainstorming (flagging per practice)

1. **Scope: Payments only vs. + Credit Memo/Refund.** Confirmed Payments-only this pass, despite all three lifecycles being pre-seeded. Credit Memo and Refund are real future work but would roughly double-to-triple the surface area (cross-entity application rules between credit memos and invoices, refund-to-payment-method reversal) — deferred to their own spec/plan cycles.
2. **AR source of truth flip (AD-4/AD-5).** Confirmed: `payment_application` becomes the ledger of record; both `payment` and `invoice` header money columns become stored rollups recomputed from it. This directly resolves Invoice's Open Decision #2.
3. **Legacy endpoint (AD-5).** Confirmed: `POST /api/tenant/invoices/{uuid}/payment` keeps its path/shape but is reimplemented as a `QuickPay` wrapper over the new Payment module rather than removed, so nothing calling it today breaks.
4. **No exchange rate on Payment (AD-12).** Deliberately narrower than Invoice's `invoice_exchange_rate` column — nothing today converts currency during application, so carrying an unused rate would be premature. If cross-currency AR is needed later, it can be added additively without touching the application-cap math (which would need real conversion logic at that point, not just a column).
