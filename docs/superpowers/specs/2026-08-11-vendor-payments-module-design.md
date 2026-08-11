# Vendor Payments Module — Backend Design Spec

**Date:** 2026-08-11
**Status:** Draft — approved by user in brainstorming session, proceeding to implementation plan.
**Scope:** New Vendor Payment module (header + bill-application ledger + refund ledger + AD-6 approval + scheduling) for the StoneSuite multi-tenant, database-per-tenant CRM/ERP backend, plus a **minimal** Vendor Bill header module built fresh in this branch as its settlement target.

---

## 1. Overview & Goals

Add a production-grade **Vendor Payment** module — money the company sends to a vendor, applied against vendor bills — as the AP mirror of the existing `payment`/`payment_application`/`invoice` triangle. Supports draft, scheduled, partial, full, advance, refund, reversal, cancellation, approvals, and payment history, per the original request.

**Branch-scope constraint (materially shapes this design):** this branch (`feat/vendor-payments-module`) is cut from `develop`, which does **not** contain the Vendor Bills module — that work exists only on the separate, unmerged `feat/vendor-bills` branch. Per explicit user direction, this branch does **not** merge `feat/vendor-bills`. Instead it builds a small, self-contained `vendor_bill` header table + `vendorbill` package — sized only for what Vendor Payments needs to apply against and keep balanced — using the same column names the real module will eventually use, so a future merge only needs `ALTER TABLE ADD COLUMN IF NOT EXISTS` (already this repo's mandated pattern for any schema change), not a rename or redefinition.

**Non-negotiable constraints (from CLAUDE.md, identical to every sibling module):**

- Database-per-tenant; no `tenant_id` column anywhere.
- v2 relational conventions: hybrid PK (`SERIAL` + `UUID`), `employee(employee_id)`-based audit columns, paired soft delete, `record_version` optimistic concurrency.
- Idempotent, append-only migrations (`CREATE TABLE IF NOT EXISTS`, `ON CONFLICT DO NOTHING`).
- Mandatory security chain on every `/api/tenant/` route.
- All list/search goes through `query/` (whitelisted `FieldResolver`, parameterized values, keyset pagination, filter × scope ANDed).

### What already exists on `develop` (reuse, do not recreate)

| Concern | Existing asset | Location |
|---|---|---|
| Vendor (counterparty) | `vendor` (relational, schema.org Person∩Organization) | `vendors/` package + `tenant/schema.sql:3425` |
| Actor / owner | `employee` | `tenant/schema.sql` |
| Record type "Vendor Bill" | `lkp_record_type` row `VBIL` (id 15) — **already seeded, unused until now** | `tenant/schema.sql:708` |
| Record type "Vendor Payment" | `lkp_record_type` row `VPAY` (id 16) — **already seeded, unused until now** | `tenant/schema.sql:709` |
| Vendor Bill status lifecycle | `lkp_record_status` rows for `record_type=15`: `DRFT, PAPV, APPV, PART, PAID, ODUE, VOID` — **already seeded** | `tenant/schema.sql:749-750` |
| Vendor Payment status lifecycle | `lkp_record_status` rows for `record_type=16`: `PEND, APPV, SENT, VOID` — **already seeded, missing DRFT/SCHD (added here, AD-7)** | `tenant/schema.sql:751` |
| RBAC pattern | `authz.ResourceVendorBill` / `authz.ResourceVendorPayment` — both **already fully seeded** (create/read/update/delete/transition, 5 rows each) | `authz/catalog.go:56-57`, catalog rows |
| Generic JSONB router mapping | `controllers/crm.go`'s `resourceForKey("vendor_bill"/"vendor_payment")` | `controllers/crm.go:81-84` |
| Payment method lookup | `lkp_payment_method` (Check/Cash/CC/ACH/Wire/Other) | `tenant/schema.sql:3290` |
| Header+application ledger pattern | `payment` / `payment_application` / `invoice.RecomputeBalance` | `payment/`, `invoice/balance.go` |
| AD-6 configurable approval gate | `purchase_order_approver` / `purchase_order_approval` + `purchaseorder/approval.go` | `purchaseorder/approval.go` |
| Per-tenant background worker pattern | `startRAGIndexing` / `runTenantIndexWorker` (ticker + `ctx.Done()` exit, one goroutine per tenant, started at boot) | `main.go:1058-1109` |
| Filter/sort/paginate/search | `query/` package | `query/` |
| Audit log | `audit_logs` via `workflow.LogAuditFull` | `controllers/payment_audit.go` (pattern) |
| Row-level IDOR guard | `recordInScope` | `controllers/scope.go` |
| Dual-permission mutation-with-side-effect pattern | `payment.go`'s `invoiceInScopeForUpdate` (Apply/Unapply need `invoice:update` too) | `controllers/payment.go:86-123` |

> **Key finding that shaped this design:** `VBIL`/`VPAY` and their status lifecycles are already seeded, and `ResourceVendorBill`/`ResourceVendorPayment` already have full RBAC catalog rows — confirming both were always intended as genuine sibling modules. No RBAC catalog changes are needed anywhere in this work.

### What is genuinely missing (new tables — justified in §3)

- `vendor_bill`, `vendor_bill_history` — minimal AP mirror of `invoice`, sized only as a payment target.
- `vendor_payment`, `vendor_payment_application`, `vendor_payment_refund`, `vendor_payment_history`, `vendor_payment_approver`, `vendor_payment_approval` — the full Vendor Payment module.

---

## 2. Architecture Decisions

**AD-1 — Minimal `vendor_bill`, built fresh, not merged from `feat/vendor-bills`.** Per explicit user direction. Header only: no line items, no PO linkage, no bill-owned approval workflow, no bill-owned payment ledger. Column names match the real module's naming convention exactly, so a future merge reconciles via `ALTER TABLE ADD COLUMN IF NOT EXISTS` only — never a `CREATE TABLE` redefinition, which would silently no-op on tenants already migrated here (the exact failure mode CLAUDE.md's migration rules warn against). This is a conscious, acknowledged risk, not an oversight.

**AD-2 — `vendor_payment` is a sibling of `vendor_bill`, connected only through `vendor_payment_application`; it is NOT a bill-owned ledger.** A vendor payment belongs to a vendor and may fund zero (advance), one, or several bills — the "one payment, many bills, with unapplied balance" allocation model, mirroring `payment`/`invoice`. A bill-owned ledger (one row per bill, `bill_id NOT NULL`) was considered and rejected: it cannot represent an advance payment (no bill to attach to yet) or one payment covering multiple bills. This is the exact reasoning `credit_memo_application` used when it could not reuse `payment_application` (whose `payment_id` is `NOT NULL`) — same shape of problem, same resolution.

**AD-3 — Hybrid PK everywhere.** Identical to every sibling: `SERIAL` internal, `UUID` external.

**AD-4 — `vendor_payment_application` is the ledger of record; `vendor_bill.amount_paid`/`balance_due` are stored rollups derived from it.** `vendorbill.RecomputeBalance` is the sole writer, invoked from `vendorpayment.Apply`/`Unapply`/`RecordRefund` — never duplicated, exactly mirroring `invoice.RecomputeBalance` being the sole writer of invoice AR balances today.

**AD-5 — Refund is a separate ledger table (`vendor_payment_refund`), netted against applications at recompute time — not a mutation of the application row, and not a full mirrored module like `refund/`.** A refund always references one live application (a payment→bill link) and cannot exceed `application_amount − already_refunded` for that link. Keeping applications immutable-once-created and refunds as their own visible trail (`"$500 applied, $200 later refunded"`) is more auditable than mutating the application down to `$300` and losing the fact a refund happened. `vendor_bill.amount_paid` and `vendor_payment.applied_total` are both computed as `SUM(live applications) − SUM(live refunds against those applications)`.

**AD-6 — Full AD-6-style configurable approval gate, reusing the exact `purchaseorder/approval.go` pattern.** `vendor_payment_approver` (who can approve at which status) + `vendor_payment_approval` (sign-offs, idempotent per approver) gate the `PAPV → APPV` move. `Approve()` is a distinct action (records one sign-off, flips status once the required count is met) — it is not reachable through the generic `Transition` endpoint, matching `purchaseorder`'s split between `/transition` and `/approve`.

**AD-7 — Two new `lkp_record_status` rows (`DRFT`, `SCHD`) appended for VPAY (record_type 16), joining the already-seeded `PEND/APPV/SENT/VOID`.** Uses the exact append-only `INSERT ... ON CONFLICT DO NOTHING` mechanism already used to add Vendor's `ONHD` status — not a new pattern, and it does not touch the original seed row-set.

**AD-8 — Cancellation and reversal are both expressed as `Transition(..., "VOID", ...)`, distinguished by which status the payment was in when voided — not two separate statuses or endpoints.** A pre-`SENT` void (from `DRFT/PAPV/APPV/SCHD`) reads as a cancellation (no cash has moved); a `SENT → VOID` reads as a reversal (cash moved and is being unwound). Both cascade: every live application on the payment is reversed in the same transaction, mirroring `payment.Transition`'s VOID cascade exactly. This keeps the API surface the same size as sibling modules (`payment` has one generic `/transition`, no `/cancel` or `/reverse`) rather than growing it for a distinction that's fully recoverable from the payment's own history.

**AD-9 — Scheduling automation via a new per-tenant background worker — the first date-triggered job in this repo.** No existing job runs tenant-scoped work on a schedule (`jobqueue/` is control-plane-scoped, for provisioning). Mirrors `startRAGIndexing`/`runTenantIndexWorker` (`main.go:1058-1109`) exactly: one `time.NewTicker`-driven goroutine per active, servable tenant, started once at boot, explicit `ctx.Done()` exit. Each tick transitions any `SCHD` payment whose `scheduled_date` has arrived to `SENT`, through the same `Transition` function the API uses (actor = system). Tenants provisioned after boot are picked up on the next restart — acceptable given the scale-to-zero deploy model restarts frequently (same trade-off `startRAGIndexing` already accepts).

**AD-10 — Dual permission check on `Apply`/`Unapply`/`RecordRefund`.** These mutate a *bill's* balance as a side effect of a *payment*-side action, so they require `vendor_payment:update` + IDOR on the payment **and** `vendor_bill:update` + IDOR on the target bill — copying `payment.go`'s `invoiceInScopeForUpdate` verbatim. A caller who can edit their own payment but can't see the target bill must not be able to move money onto it.

**AD-11 — Lock order `vendor_payment < vendor_bill`, a new hierarchy non-overlapping with the AR side's `credit_memo < payment < invoice`.** Prevents cross-module deadlocks by never taking these two locks in the opposite order anywhere in the codebase.

**AD-12 — Vendor payment amount is immutable after creation** (mirrors `payment` AD-10). `PATCH` may edit non-monetary fields but not `amount`. To fix a wrong amount: void and recreate.

**AD-13 — No notification system is built.** Confirmed with the user: this repo has no notifications table, dispatch mechanism, or precedent (grepped clean). Approval-needed/approved/rejected events surface only through `vendor_payment_history` + `GET .../audit`, same as every sibling module.

**AD-14 — Vendor is fixed at creation on both `vendor_bill` and `vendor_payment`, name snapshotted** (mirrors `vendor_bill_vendor_id`/`vendor_bill_vendor_name` in the real module, and `credit_memo_customer_id` generally). `Apply` rejects (400) a payment/bill vendor mismatch.

**AD-15 — Minimal `vendor_bill`'s own status transitions are intentionally thin.** Manual moves: `DRFT→PAPV`, `PAPV→{APPV,DRFT}`, and `{DRFT,PAPV,APPV}→VOID` — VOID is rejected (409) while the bill has any live `vendor_payment_application` row (mirrors `payment` AD-11's "blocked while live applications exist," applied here to bill-void instead of payment-delete, since a minimal module has no cross-payment cascade). `PART`/`PAID` are reached only by derivation from `RecomputeBalance` (mirrors the real module's documented behavior), never by direct user transition. **`ODUE` (overdue) derivation is explicitly out of scope** — no due-date-driven job in this build; the seeded status exists but nothing transitions into it here. There is no bill-level approval gate (no `vendor_bill_approver`/`_approval` tables) — approval lives entirely on `vendor_payment`, since that's what the user's "approvals" requirement targets.

---

## 3. New Tables — Per-Table Justification

### `vendor_bill`
- **(a)** No table on this branch models a bill owed to a vendor.
- **(b)** Minimal header: identity, vendor (fixed), dates, stored `grand_total`/`amount_paid`/`balance_due`.
- **(c)** New master (hybrid PK). FKs to `vendor`, `employee`, `lkp_record_type`(=VBIL), `lkp_record_status`.

### `vendor_bill_history`
- **(b)** One row per status change, mirrors `invoice_history`/`payment_history`.
- **(c)** New child of `vendor_bill`; FKs to `lkp_record_status`, `employee`.

### `vendor_payment`
- **(a)** No table models money sent to a vendor independent of any single bill.
- **(b)** The payment header: identity, classification, vendor, method/reference, stored applied/unapplied rollup, approval status, optional scheduled date.
- **(c)** New master (hybrid PK). FKs to `vendor`, `employee`, `lkp_record_type`(=VPAY), `lkp_record_status`, `lkp_payment_method`, `lkp_currency`.

### `vendor_payment_application`
- **(a)** The payment↔bill many-to-many ledger; no existing table fits (AD-2).
- **(b)** One row per live allocation of a payment's money to a bill's balance.
- **(c)** New junction; FKs to `vendor_payment` (CASCADE) and `vendor_bill` (RESTRICT).

### `vendor_payment_refund`
- **(a)** No table models a vendor returning money against a specific application (AD-5).
- **(b)** One row per refund event, referencing exactly one live application.
- **(c)** New child; FKs to `vendor_payment` (CASCADE) and `vendor_bill` (RESTRICT).

### `vendor_payment_history`
- **(b)** Typed status/action trail alongside `audit_logs`, mirrors `payment_history`.
- **(c)** New child of `vendor_payment`; FKs to `lkp_record_status`, `employee`.

### `vendor_payment_approver` / `vendor_payment_approval`
- **(a)** No config exists for who can approve a vendor payment (AD-6).
- **(b)** Exact structural copy of `purchase_order_approver`/`purchase_order_approval`.
- **(c)** New; FKs to `lkp_record_type`, `lkp_record_status`, `employee`, `vendor_payment`.

---

## 4. ER Diagram (text)

```
   lkp_currency      vendor           employee        lkp_record_type/status    lkp_payment_method
        │(0..1)          │(1)             │(1)                 │(1)                   │(1)
        │        ┌───────┼─────────────────┼─────────────────────┼───────────────────────┤
        ▼(N)     ▼(N)                                                                    ▼(N)
 ┌──────────────────────────────────────────────────────────────────────────────────────────┐
 │                              vendor_payment  (header, NEW)                                 │
 │  PK vendor_payment_id (serial) · UUID vendor_payment_uuid · "VPAY-000001"                  │
 │  FK vendor_id (fixed) · record_type=VPAY · vendor_payment_status · scheduled_date          │
 │  amount (immutable) · applied_total (rollup) · unapplied_amount (rollup) · approval_status │
 └───┬───────────────────────┬────────────────────────────┬──────────────────────┬───────────┘
     │(1)                    │(1)                          │(1)                  │(1)
 (N) ▼                   (N) ▼                          (N) ▼                (N) ▼
┌─────────────────┐ ┌────────────────────┐  ┌─────────────────────┐  ┌──────────────────────┐
│ vendor_payment_  │ │ vendor_payment_    │  │ vendor_payment_     │  │ vendor_payment_       │
│ application (NEW)│ │ refund (NEW)       │  │ history (NEW)       │  │ approval (NEW)        │
│ FK vendor_payment│ │ FK vendor_payment ─┘  │ from/to_status_id   │  │ record_status_id       │
│ FK vendor_bill ──┼─┼─► one live app        │ action · snapshot   │  │ approver_employee_id   │
│ application_amt  │ │ FK vendor_bill        └─────────────────────┘  └──────────────────────┘
└────────┬─────────┘ └─────────┬───────────┘
         │(N)                  │(N)
         └──────────┬──────────┘
                     ▼(1)
          ┌───────────────────────────────────────────────────────┐
          │                  vendor_bill  (header, NEW, minimal)    │
          │  PK vendor_bill_id · UUID vendor_bill_uuid · FK vendor  │
          │  grand_total · amount_paid (rollup) · balance_due (rollup)│
          └──────────────────────────┬────────────────────────────┘
                                      │(1)
                                  (N) ▼
                          ┌─────────────────────┐
                          │ vendor_bill_history  │
                          └─────────────────────┘

vendor_payment_approver (config, not tied to one payment): (record_type_id=VPAY, record_status_id=PAPV, approver_employee_id)
```

**Cardinality summary**
- `vendor` 1 ─── N `vendor_bill`, 1 ─── N `vendor_payment`.
- `vendor_payment` 1 ─── N `vendor_payment_application`, `vendor_payment_refund`, `vendor_payment_history`, `vendor_payment_approval`.
- `vendor_bill` 1 ─── N `vendor_payment_application`, `vendor_payment_refund` (a bill may be paid by several payments; a payment may fund several bills).
- `vendor_bill.amount_paid`/`balance_due` are derived from `vendor_payment_application` net `vendor_payment_refund` — written by no other code path.

---

## 5. SQL — CREATE TABLE Statements

> Appended to `database/migrations/tenant/schema.sql` via the **add-migration** skill, after the `vendor`/`vendor_history` block (both new blocks FK `vendor`). Money `DECIMAL(15,2)`, consistent with every sibling.

### 5.1 Two new `lkp_record_status` rows for VPAY (AD-7)

```sql
INSERT INTO lkp_record_status (record_status_code, record_status_name, record_status_record_type, record_status_is_active, record_status_is_system, record_status_created_by)
SELECT 'DRFT', 'Draft', record_type_id, TRUE, TRUE, 1 FROM lkp_record_type WHERE record_type_code = 'VPAY'
UNION ALL
SELECT 'SCHD', 'Scheduled', record_type_id, TRUE, TRUE, 1 FROM lkp_record_type WHERE record_type_code = 'VPAY'
ON CONFLICT (record_status_code, record_status_record_type) DO NOTHING;
```

### 5.2 `vendor_bill` (minimal header)

```sql
CREATE TABLE IF NOT EXISTS vendor_bill (
    vendor_bill_id                SERIAL        PRIMARY KEY,
    vendor_bill_uuid              UUID          NOT NULL DEFAULT gen_random_uuid(),
    vendor_bill_number            VARCHAR(20)       NULL,  -- 'VBIL-000001', generated post-insert in Go

    record_type                   INTEGER       NOT NULL REFERENCES lkp_record_type(record_type_id),   -- = VBIL
    vendor_bill_status             INTEGER       NOT NULL REFERENCES lkp_record_status(record_status_id),

    vendor_bill_vendor_id          INTEGER       NOT NULL REFERENCES vendor(vendor_id),   -- fixed at creation
    vendor_bill_vendor_name        VARCHAR(150)  NOT NULL DEFAULT '',                      -- snapshot

    vendor_bill_reference_number   VARCHAR(50)   NOT NULL DEFAULT '',
    vendor_bill_date               DATE          NOT NULL DEFAULT CURRENT_DATE,
    vendor_bill_due_date           DATE              NULL,
    vendor_bill_memo               TEXT          NOT NULL DEFAULT '',
    vendor_bill_internal_notes     TEXT          NOT NULL DEFAULT '',

    vendor_bill_owner_id           INTEGER           NULL REFERENCES employee(employee_id),

    vendor_bill_grand_total        DECIMAL(15,2) NOT NULL DEFAULT 0,
    vendor_bill_amount_paid        DECIMAL(15,2) NOT NULL DEFAULT 0,   -- rollup, sole writer vendorbill.RecomputeBalance
    vendor_bill_balance_due        DECIMAL(15,2) NOT NULL DEFAULT 0,   -- rollup

    vendor_bill_custom_fields      JSONB         NOT NULL DEFAULT '{}',
    vendor_bill_created_at         TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    vendor_bill_created_by         INTEGER           NULL REFERENCES employee(employee_id),
    vendor_bill_updated_at         TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    vendor_bill_updated_by         INTEGER           NULL REFERENCES employee(employee_id),
    vendor_bill_deleted_at         TIMESTAMP         NULL,
    vendor_bill_deleted_by         INTEGER           NULL REFERENCES employee(employee_id),
    vendor_bill_record_version     INTEGER       NOT NULL DEFAULT 1,

    CONSTRAINT uq_vendor_bill_uuid     UNIQUE (vendor_bill_uuid),
    CONSTRAINT uq_vendor_bill_number   UNIQUE (vendor_bill_number),
    CONSTRAINT chk_vbil_total_nonneg   CHECK (vendor_bill_grand_total >= 0),
    CONSTRAINT chk_vbil_paid_nonneg    CHECK (vendor_bill_amount_paid >= 0 AND vendor_bill_balance_due >= 0),
    CONSTRAINT chk_vbil_soft_delete    CHECK (
        (vendor_bill_deleted_at IS NULL AND vendor_bill_deleted_by IS NULL) OR
        (vendor_bill_deleted_at IS NOT NULL AND vendor_bill_deleted_by IS NOT NULL)
    )
);

CREATE TABLE IF NOT EXISTS vendor_bill_history (
    vendor_bill_history_id   SERIAL       PRIMARY KEY,
    vendor_bill_id            INTEGER      NOT NULL REFERENCES vendor_bill(vendor_bill_id) ON DELETE CASCADE,
    from_status_id             INTEGER          NULL REFERENCES lkp_record_status(record_status_id),
    to_status_id                INTEGER          NULL REFERENCES lkp_record_status(record_status_id),
    action                      VARCHAR(32)  NOT NULL DEFAULT 'transition',
    actor_employee_id            INTEGER          NULL REFERENCES employee(employee_id),
    snapshot                     JSONB        NOT NULL DEFAULT '{}',
    at                           TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

### 5.3 `vendor_payment` (header)

```sql
CREATE TABLE IF NOT EXISTS vendor_payment (
    vendor_payment_id               SERIAL        PRIMARY KEY,
    vendor_payment_uuid             UUID          NOT NULL DEFAULT gen_random_uuid(),
    vendor_payment_number           VARCHAR(20)       NULL,  -- 'VPAY-000001', generated post-insert in Go

    record_type                     INTEGER       NOT NULL REFERENCES lkp_record_type(record_type_id),   -- = VPAY
    vendor_payment_status            INTEGER       NOT NULL REFERENCES lkp_record_status(record_status_id),

    vendor_payment_vendor_id          INTEGER       NOT NULL REFERENCES vendor(vendor_id),  -- fixed at creation
    vendor_payment_vendor_name        VARCHAR(150)  NOT NULL DEFAULT '',                      -- snapshot

    vendor_payment_method               INTEGER       NOT NULL REFERENCES lkp_payment_method(payment_method_id),
    vendor_payment_reference_number     VARCHAR(50)   NOT NULL DEFAULT '',
    vendor_payment_date                 DATE          NOT NULL DEFAULT CURRENT_DATE,
    vendor_payment_scheduled_date       DATE              NULL,  -- only meaningful once status = SCHD
    vendor_payment_currency             INTEGER           NULL REFERENCES lkp_currency(currency_id),
    vendor_payment_memo                 TEXT          NOT NULL DEFAULT '',
    vendor_payment_internal_notes       TEXT          NOT NULL DEFAULT '',

    vendor_payment_amount                DECIMAL(15,2) NOT NULL,                              -- immutable post-create (AD-12)
    vendor_payment_applied_total          DECIMAL(15,2) NOT NULL DEFAULT 0,                    -- rollup
    vendor_payment_unapplied_amount        DECIMAL(15,2) NOT NULL DEFAULT 0,                   -- rollup

    vendor_payment_approval_status          VARCHAR(10)  NOT NULL DEFAULT 'none',              -- AD-6
    vendor_payment_approved_by               INTEGER          NULL REFERENCES employee(employee_id),

    vendor_payment_owner_id                   INTEGER           NULL REFERENCES employee(employee_id),

    vendor_payment_custom_fields               JSONB        NOT NULL DEFAULT '{}',
    vendor_payment_created_at                   TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    vendor_payment_created_by                    INTEGER          NULL REFERENCES employee(employee_id),
    vendor_payment_updated_at                     TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    vendor_payment_updated_by                      INTEGER          NULL REFERENCES employee(employee_id),
    vendor_payment_deleted_at                       TIMESTAMP        NULL,
    vendor_payment_deleted_by                        INTEGER          NULL REFERENCES employee(employee_id),
    vendor_payment_record_version                     INTEGER      NOT NULL DEFAULT 1,

    CONSTRAINT uq_vendor_payment_uuid       UNIQUE (vendor_payment_uuid),
    CONSTRAINT uq_vendor_payment_number     UNIQUE (vendor_payment_number),
    CONSTRAINT chk_vpay_approval_status     CHECK (vendor_payment_approval_status IN ('none','pending','approved')),
    CONSTRAINT chk_vpay_amount_pos          CHECK (vendor_payment_amount > 0),
    CONSTRAINT chk_vpay_applied_nonneg      CHECK (vendor_payment_applied_total >= 0 AND vendor_payment_unapplied_amount >= 0),
    CONSTRAINT chk_vpay_applied_le_amt      CHECK (vendor_payment_applied_total <= vendor_payment_amount),
    CONSTRAINT chk_vpay_soft_delete         CHECK (
        (vendor_payment_deleted_at IS NULL AND vendor_payment_deleted_by IS NULL) OR
        (vendor_payment_deleted_at IS NOT NULL AND vendor_payment_deleted_by IS NOT NULL)
    )
);
```

### 5.4 `vendor_payment_application` (bill-application ledger)

```sql
CREATE TABLE IF NOT EXISTS vendor_payment_application (
    application_id              SERIAL        PRIMARY KEY,
    application_uuid             UUID          NOT NULL DEFAULT gen_random_uuid(),
    vendor_payment_id             INTEGER       NOT NULL REFERENCES vendor_payment(vendor_payment_id) ON DELETE CASCADE,
    vendor_bill_id                 INTEGER       NOT NULL REFERENCES vendor_bill(vendor_bill_id),

    application_amount              DECIMAL(15,2) NOT NULL,

    application_created_at           TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    application_created_by            INTEGER          NULL REFERENCES employee(employee_id),
    application_deleted_at             TIMESTAMP        NULL,  -- set = "unapplied"
    application_deleted_by              INTEGER          NULL REFERENCES employee(employee_id),
    application_record_version           INTEGER      NOT NULL DEFAULT 1,

    CONSTRAINT uq_vendor_payment_application_uuid UNIQUE (application_uuid),
    CONSTRAINT chk_vpay_app_amount_pos            CHECK (application_amount > 0),
    CONSTRAINT chk_vpay_app_soft_delete           CHECK (
        (application_deleted_at IS NULL AND application_deleted_by IS NULL) OR
        (application_deleted_at IS NOT NULL AND application_deleted_by IS NOT NULL)
    )
);

-- At most one LIVE application per (vendor_payment, vendor_bill) pair -- re-applying
-- increases the existing row's amount instead of creating a duplicate.
CREATE UNIQUE INDEX IF NOT EXISTS uq_vpay_app_live_pair
    ON vendor_payment_application (vendor_payment_id, vendor_bill_id) WHERE application_deleted_at IS NULL;
```

### 5.5 `vendor_payment_refund` (AD-5)

```sql
CREATE TABLE IF NOT EXISTS vendor_payment_refund (
    refund_id                    SERIAL        PRIMARY KEY,
    refund_uuid                  UUID          NOT NULL DEFAULT gen_random_uuid(),
    vendor_payment_id             INTEGER       NOT NULL REFERENCES vendor_payment(vendor_payment_id) ON DELETE CASCADE,
    vendor_bill_id                 INTEGER       NOT NULL REFERENCES vendor_bill(vendor_bill_id),

    refund_amount                   DECIMAL(15,2) NOT NULL,
    refund_reason                    VARCHAR(150) NOT NULL DEFAULT '',
    refund_reference_number           VARCHAR(50)  NOT NULL DEFAULT '',
    refund_memo                        TEXT         NOT NULL DEFAULT '',
    refund_refunded_at                  DATE         NOT NULL DEFAULT CURRENT_DATE,

    refund_created_at                    TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    refund_created_by                     INTEGER          NULL REFERENCES employee(employee_id),
    refund_deleted_at                      TIMESTAMP        NULL,  -- set = "un-refund" (correction)
    refund_deleted_by                       INTEGER          NULL REFERENCES employee(employee_id),
    refund_record_version                    INTEGER      NOT NULL DEFAULT 1,

    CONSTRAINT uq_vendor_payment_refund_uuid UNIQUE (refund_uuid),
    CONSTRAINT chk_vpay_refund_amount_pos    CHECK (refund_amount > 0),
    CONSTRAINT chk_vpay_refund_soft_delete   CHECK (
        (refund_deleted_at IS NULL AND refund_deleted_by IS NULL) OR
        (refund_deleted_at IS NOT NULL AND refund_deleted_by IS NOT NULL)
    )
);
```

### 5.6 `vendor_payment_history`

```sql
CREATE TABLE IF NOT EXISTS vendor_payment_history (
    vendor_payment_history_id  SERIAL       PRIMARY KEY,
    vendor_payment_id           INTEGER      NOT NULL REFERENCES vendor_payment(vendor_payment_id) ON DELETE CASCADE,
    from_status_id                INTEGER          NULL REFERENCES lkp_record_status(record_status_id),
    to_status_id                   INTEGER          NULL REFERENCES lkp_record_status(record_status_id),
    action                          VARCHAR(32)  NOT NULL DEFAULT 'transition',  -- create|update|transition|approve|apply|unapply|refund|unrefund
    actor_employee_id                INTEGER          NULL REFERENCES employee(employee_id),
    snapshot                          JSONB        NOT NULL DEFAULT '{}',
    at                                 TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

### 5.7 `vendor_payment_approver` / `vendor_payment_approval` (AD-6)

```sql
CREATE TABLE IF NOT EXISTS vendor_payment_approver (
    vendor_payment_approver_id   SERIAL      PRIMARY KEY,
    record_type_id                 INTEGER     NOT NULL REFERENCES lkp_record_type(record_type_id),      -- = VPAY
    record_status_id               INTEGER     NOT NULL REFERENCES lkp_record_status(record_status_id),  -- = PAPV
    approver_employee_id           INTEGER     NOT NULL REFERENCES employee(employee_id),
    is_active                      BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at                     TIMESTAMP   NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_by                     INTEGER         NULL REFERENCES employee(employee_id),
    CONSTRAINT uq_vendor_payment_approver UNIQUE (record_type_id, record_status_id, approver_employee_id)
);

CREATE TABLE IF NOT EXISTS vendor_payment_approval (
    vendor_payment_approval_id   SERIAL     PRIMARY KEY,
    vendor_payment_id              INTEGER    NOT NULL REFERENCES vendor_payment(vendor_payment_id) ON DELETE CASCADE,
    record_status_id               INTEGER    NOT NULL REFERENCES lkp_record_status(record_status_id),
    approver_employee_id           INTEGER    NOT NULL REFERENCES employee(employee_id),
    approved_at                    TIMESTAMP  NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT uq_vendor_payment_approval UNIQUE (vendor_payment_id, record_status_id, approver_employee_id)
);
```

### 5.8 Indexes

```sql
-- vendor_bill
CREATE INDEX IF NOT EXISTS idx_vbil_vendor        ON vendor_bill (vendor_bill_vendor_id) WHERE vendor_bill_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vbil_status         ON vendor_bill (vendor_bill_status)    WHERE vendor_bill_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vbil_date           ON vendor_bill (vendor_bill_date)      WHERE vendor_bill_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vbil_due_date       ON vendor_bill (vendor_bill_due_date)  WHERE vendor_bill_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vbil_owner          ON vendor_bill (vendor_bill_owner_id)  WHERE vendor_bill_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vbil_created_id     ON vendor_bill (vendor_bill_created_at, vendor_bill_id) WHERE vendor_bill_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vbil_balance_id     ON vendor_bill (vendor_bill_balance_due, vendor_bill_id) WHERE vendor_bill_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vbil_custom_gin     ON vendor_bill USING GIN (vendor_bill_custom_fields);
CREATE INDEX IF NOT EXISTS idx_vbil_history_bill   ON vendor_bill_history (vendor_bill_id);

-- vendor_payment
CREATE INDEX IF NOT EXISTS idx_vpay_vendor         ON vendor_payment (vendor_payment_vendor_id) WHERE vendor_payment_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vpay_status         ON vendor_payment (vendor_payment_status)    WHERE vendor_payment_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vpay_date           ON vendor_payment (vendor_payment_date)      WHERE vendor_payment_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vpay_scheduled      ON vendor_payment (vendor_payment_scheduled_date) WHERE vendor_payment_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vpay_owner          ON vendor_payment (vendor_payment_owner_id)  WHERE vendor_payment_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vpay_created_id     ON vendor_payment (vendor_payment_created_at, vendor_payment_id) WHERE vendor_payment_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vpay_amount_id      ON vendor_payment (vendor_payment_amount, vendor_payment_id) WHERE vendor_payment_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vpay_unapplied_id   ON vendor_payment (vendor_payment_unapplied_amount, vendor_payment_id) WHERE vendor_payment_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vpay_custom_gin     ON vendor_payment USING GIN (vendor_payment_custom_fields);

-- children
CREATE INDEX IF NOT EXISTS idx_vpay_app_payment    ON vendor_payment_application (vendor_payment_id) WHERE application_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vpay_app_bill        ON vendor_payment_application (vendor_bill_id)     WHERE application_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vpay_refund_payment  ON vendor_payment_refund (vendor_payment_id) WHERE refund_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vpay_refund_bill      ON vendor_payment_refund (vendor_bill_id)     WHERE refund_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vpay_history_payment   ON vendor_payment_history (vendor_payment_id);
CREATE INDEX IF NOT EXISTS idx_vpay_approver_lookup    ON vendor_payment_approver (record_type_id, record_status_id) WHERE is_active;
CREATE INDEX IF NOT EXISTS idx_vpay_approval_payment    ON vendor_payment_approval (vendor_payment_id);
```

> **Migration ordering:** appended after the `vendor`/`vendor_history` block, since `vendor_bill` and `vendor_payment` both FK `vendor`.

---

## 6. Foreign Key Relationships (explained)

| Child column | → Parent | Meaning | On delete |
|---|---|---|---|
| `vendor_bill.vendor_bill_vendor_id` | `vendor.vendor_id` | Who's owed | RESTRICT |
| `vendor_bill.record_type` | `lkp_record_type.record_type_id` | Always `VBIL` | RESTRICT |
| `vendor_bill.vendor_bill_status` | `lkp_record_status.record_status_id` | Lifecycle (`record_type=15` set) | RESTRICT |
| `vendor_bill_history.vendor_bill_id` | `vendor_bill.vendor_bill_id` | Owning bill | **CASCADE** |
| `vendor_payment.vendor_payment_vendor_id` | `vendor.vendor_id` | Who's being paid | RESTRICT |
| `vendor_payment.record_type` | `lkp_record_type.record_type_id` | Always `VPAY` | RESTRICT |
| `vendor_payment.vendor_payment_status` | `lkp_record_status.record_status_id` | Lifecycle (`record_type=16` set) | RESTRICT |
| `vendor_payment.vendor_payment_method` | `lkp_payment_method.payment_method_id` | How it's paid | RESTRICT |
| `vendor_payment_application.vendor_payment_id` | `vendor_payment.vendor_payment_id` | Owning payment | **CASCADE** |
| `vendor_payment_application.vendor_bill_id` | `vendor_bill.vendor_bill_id` | Target bill | RESTRICT |
| `vendor_payment_refund.vendor_payment_id` | `vendor_payment.vendor_payment_id` | Owning payment | **CASCADE** |
| `vendor_payment_refund.vendor_bill_id` | `vendor_bill.vendor_bill_id` | Bill the refund restores balance on | RESTRICT |
| `vendor_payment_history.vendor_payment_id` | `vendor_payment.vendor_payment_id` | Owning payment | **CASCADE** |
| `vendor_payment_approval.vendor_payment_id` | `vendor_payment.vendor_payment_id` | Owning payment | **CASCADE** |

No cross-database FKs. No `tenant_id` anywhere.

---

## 7. Status Transition Rules

### Vendor Payment (`record_type=16`, VPAY): `DRFT, SCHD` new (AD-7); `PAPV, APPV, SENT, VOID` already seeded (`PEND` stays seeded but unused — `DRFT` is the new entry point instead)

```go
var allowedVendorPaymentTransitions = map[string]map[string]bool{
    "DRFT": {"PAPV": true, "VOID": true},
    "PAPV": {"APPV": true, "DRFT": true, "VOID": true},  // APPV only reachable in practice via Approve(), see below
    "APPV": {"SCHD": true, "SENT": true, "VOID": true},
    "SCHD": {"SENT": true, "VOID": true},
    "SENT": {"VOID": true},
    "VOID": {},
}
```

- New payments start at **`DRFT`**.
- `Transition` **rejects** a manual `PAPV → APPV` move (`ErrApprovalRequired`-shaped error) — that edge is only reachable through `Approve()` once the configured sign-off count is met (AD-6), mirroring `purchaseorder`.
- Applying/unapplying/refunding is allowed at any status except `VOID` (mirrors `payment`'s AD-7 looseness — recording money movement is decoupled from the payment's own lifecycle stage).
- `VOID` from any non-terminal status cascades: every live `vendor_payment_application` is reversed (and every live `vendor_payment_refund` against those applications, implicitly moot once the application itself is gone) in the same transaction — mirrors `payment.Transition`'s cascade (AD-8).
- `SCHD → SENT` is reached either manually (`Transition`) or automatically by the scheduler (AD-9), both through the same function.

### Vendor Bill (`record_type=15`, VBIL) — minimal, AD-15

```go
var allowedVendorBillTransitions = map[string]map[string]bool{
    "DRFT": {"PAPV": true, "VOID": true},
    "PAPV": {"APPV": true, "DRFT": true, "VOID": true},
    "APPV": {"VOID": true},   // PART/PAID reached only by DeriveStatus, never here
    "PART": {"VOID": true},
    "PAID": {"VOID": true},
    "VOID": {},
}
```

`Transition(..., "VOID", ...)` is rejected (409) while the bill has any live `vendor_payment_application` — unapply/reverse first (mirrors `payment` AD-11, applied to bill-void).

---

## 8. Money, Application & Rollup Rules

**Applying a payment to a bill (`Apply`, transactional, `FOR UPDATE` on `vendor_payment` then `vendor_bill` — AD-11 lock order):**
```
1. Lock vendor_payment row; reject (409) if status == VOID.
2. Lock vendor_bill row; reject if vendor mismatch (400), or bill not in {APPV,PART,PAID... no, only
   APPV/PART} PayableStatuses = {APPV, PART} (ODUE excluded per AD-15 -- not derived in this build).
3. cap = min(requestedAmount, vendor_payment.unapplied_amount, vendor_bill.BalanceDue())
   if requestedAmount > cap: reject 400 ("exceeds available balance") -- never clamped.
4. Upsert vendor_payment_application: live row for (payment_id, bill_id) exists -> increase amount;
   else insert new row.
5. Recompute vendor_payment.applied_total = SUM(live applications) - SUM(live refunds against them);
   unapplied_amount = amount - applied_total.
6. vendorbill.RecomputeBalance: bill.amount_paid = SUM(live applications for this bill across all
   payments) - SUM(live refunds against those applications); balance_due = grand_total - amount_paid;
   derive status (balance_due <= 0 -> PAID; 0 < amount_paid -> PART; else APPV).
7. Insert vendor_payment_history (action='apply') and vendor_bill_history rows.
```

**Unapplying (`Unapply`, transactional, no status gate — "must always be possible"):**
```
1. Lock the live application row for (payment_id, bill_id); 404 if none.
2. Soft-delete it. Any live refunds against it become orphaned-but-harmless history (refunds reference
   the application's (payment,bill) pair, not a live-row FK, so they survive as an audit trail).
3. Recompute vendor_payment rollup (step 5 above) and vendor_bill rollup (step 6 above).
4. Insert history rows (action='unapply') on both sides.
```

**Refunding (`RecordRefund`, transactional, AD-5):**
```
1. Lock vendor_payment row (no status gate) then vendor_bill row.
2. Find the live application for (payment_id, bill_id); 404 if none.
3. cap = application_amount - SUM(live refunds already against this application)
   if requestedAmount > cap: reject 400 -- never clamped.
4. Insert vendor_payment_refund row.
5. Recompute vendor_payment rollup and vendor_bill rollup (refunds subtract from both, same as an
   unapply would, but the application row itself is untouched -- the audit trail keeps both events
   visible).
6. Insert history rows (action='refund') on both sides.
```
`RemoveRefund` (soft-delete a refund = "un-refund"/correction) is the mirror operation, recomputing both rollups back upward. No status gate.

**Voiding (`Transition` to VOID, transactional, cascades — AD-8):**
```
1. Lock vendor_payment row.
2. For every live vendor_payment_application on this payment: run the Unapply steps (2-3 above)
   against its bill, inside the same transaction, ORDER BY vendor_bill_id (global lock-order
   tiebreaker, mirrors payment.Transition's cascade).
3. Set vendor_payment.status = VOID.
4. Insert vendor_payment_history (action='transition').
```

**Scheduler tick (AD-9, no user request in flight):**
```
For each SCHD payment where scheduled_date <= CURRENT_DATE:
    Transition(ctx, pool, uuid, "SENT", systemEmployeeID)
```

---

## 9. API Contracts

All under `/api/tenant/`, through `tenantChain`, RBAC-checked in-handler, IDOR-guarded (404 on scope denial), same envelope as every sibling (`{success, message?, ...}`).

### Vendor Bill (minimal)

| Method & path | Purpose | RBAC |
|---|---|---|
| `GET  /api/tenant/vendor-bills` | Simple in-scope list, cursor-paginated | `vendor_bill:read` + scope |
| `POST /api/tenant/vendor-bills/search` | Full filter + sort + search + pagination | `vendor_bill:read` + scope |
| `POST /api/tenant/vendor-bills` | Create | `vendor_bill:create` |
| `GET  /api/tenant/vendor-bills/{uuid}` | Get one | `vendor_bill:read` + IDOR |
| `PATCH /api/tenant/vendor-bills/{uuid}` | Edit (DRFT only) | `vendor_bill:update` + IDOR |
| `DELETE /api/tenant/vendor-bills/{uuid}` | Soft delete (DRFT/VOID only) | `vendor_bill:delete` + IDOR |
| `POST /api/tenant/vendor-bills/{uuid}/transition` | Status change (§7) | `vendor_bill:transition` + IDOR |
| `GET  /api/tenant/vendor-bills/{uuid}/audit` | Audit trail | `vendor_bill:read` + IDOR |
| `GET  /api/tenant/vendor-bills/{uuid}/payments` | Live applications + refunds against this bill (AP reconciliation view) | `vendor_bill:read` + IDOR |

### Vendor Payment

| Method & path | Purpose | RBAC |
|---|---|---|
| `GET  /api/tenant/vendor-payments` | Simple in-scope list, cursor-paginated | `vendor_payment:read` + scope |
| `POST /api/tenant/vendor-payments/search` | Full filter + sort + search + pagination | `vendor_payment:read` + scope |
| `POST /api/tenant/vendor-payments` | Create (header, optional inline `applications[]`) | `vendor_payment:create` (+ `vendor_bill:update` scope-check per inline application) |
| `GET  /api/tenant/vendor-payments/{uuid}` | Get one (+ live applications + refunds) | `vendor_payment:read` + IDOR |
| `PATCH /api/tenant/vendor-payments/{uuid}` | Edit non-monetary fields (DRFT/PAPV only) | `vendor_payment:update` + IDOR |
| `DELETE /api/tenant/vendor-payments/{uuid}` | Soft delete; 409 if live applications exist | `vendor_payment:delete` + IDOR |
| `POST /api/tenant/vendor-payments/{uuid}/transition` | Status change (§7); `VOID` cascades | `vendor_payment:transition` + IDOR |
| `POST /api/tenant/vendor-payments/{uuid}/approve` | AD-6 sign-off; flips `PAPV→APPV` once satisfied | `vendor_payment:transition` + IDOR |
| `POST /api/tenant/vendor-payments/{uuid}/apply` | `{vendorBillUuid, amount}` | `vendor_payment:update` + IDOR, **and** `vendor_bill:update` scope-check (AD-10) |
| `POST /api/tenant/vendor-payments/{uuid}/unapply` | `{vendorBillUuid}` | same dual-check |
| `POST /api/tenant/vendor-payments/{uuid}/refund` | `{vendorBillUuid, amount, reason}` | same dual-check |
| `DELETE /api/tenant/vendor-payments/{uuid}/refunds/{refundId}` | Un-refund | same dual-check |
| `GET  /api/tenant/vendor-payments/{uuid}/audit` | Audit / history trail | `vendor_payment:read` + IDOR |

**Create request**
```json
POST /api/tenant/vendor-payments
{
  "vendorUuid": "9d0f…c2",
  "methodId": 5,
  "referenceNumber": "Wire #77",
  "paymentDate": "2026-08-11",
  "amount": 2500.00,
  "memo": "August settlement",
  "customFields": {},
  "applications": [
    { "vendorBillUuid": "6f2c…9a", "amount": 1500.00 }
  ]
}
→ 201 { "success": true, "vendorPayment": { "id": "…", "vendorPaymentNumber": "VPAY-000001",
        "status": "Draft", "amount": 2500.00, "appliedTotal": 1500.00,
        "unappliedAmount": 1000.00, "applications": [...] } }
```

**Apply / Unapply / Refund** follow the identical `{vendorBillUuid[, amount[, reason]]}` shape as `payment`'s `/apply`, `/unapply`, returning the updated payment (with `400` on cap-exceeded, `404` on no-live-application for unapply/refund).

---

## 10. Listing & Query Architecture

Reuses `query/` unchanged. New: `vendorbill.resolver` and `vendorpayment.resolver` (`FieldResolver` + `SortResolver` + `SearchResolver`), each store's `Search` (keyset).

**Vendor Payment FieldResolver whitelist** (alias `vp`):

| Logical key | SQL expression | DataType | Ops |
|---|---|---|---|
| `id` | `vp.vendor_payment_uuid::text` | string | eq |
| `document_number` | `COALESCE(vp.vendor_payment_number,'')` | string | eq, contains, startswith |
| `vendor_id` | `vp.vendor_payment_vendor_id::text` | string | eq, in |
| `status` | `vp.vendor_payment_status::text` | string | eq, in |
| `method_id` | `vp.vendor_payment_method::text` | string | eq, in |
| `payment_date` | `vp.vendor_payment_date` | date | eq, gt, gte, lt, lte, between |
| `scheduled_date` | `vp.vendor_payment_scheduled_date` | date | eq, gt, gte, lt, lte, between, is_null |
| `amount` / `applied_total` / `unapplied_amount` | respective `vp.vendor_payment_*` | number | eq, gt, gte, lt, lte, between |
| `owner_id` | `vp.vendor_payment_owner_id::text` | string | eq, in, is_null |
| `created_at` / `updated_at` | `vp.vendor_payment_created_at` / `_updated_at` | date | gte, lte, between |
| `cf:<key>` | `vp.vendor_payment_custom_fields->>'<key>'` | per `workflow_field_definitions` | per type |

**SortResolver whitelist:** `document_number, payment_date, amount, unapplied_amount, status, vendor_id, created_at, updated_at` — each `NOT NULL`, paired with `vendor_payment_id` tiebreaker.

**SearchPredicate:**
```sql
(   vp.vendor_payment_number             ILIKE $n
 OR vp.vendor_payment_reference_number   ILIKE $n
 OR vp.vendor_payment_memo               ILIKE $n
 OR vp.vendor_payment_vendor_name        ILIKE $n)
```

`vendor_bill`'s resolver mirrors this shape 1:1 on its own columns (`document_number`, `vendor_id`, `status`, `grand_total`, `balance_due`, `due_date`, `owner_id`, `cf:<key>`).

**Response envelope:** identical to every sibling — `{success, scope, records, nextCursor, hasMore}`. Keyset only, no offset.

---

## 11. Validation Rules

**Vendor Payment header**
- `vendorUuid` required, must resolve to a live `vendor` in this tenant; caller must have scope on it.
- `methodId` required, must reference a live, active `lkp_payment_method` row.
- `amount` > 0, immutable after creation (AD-12).
- `scheduledDate`, if present, required to be set before a `Transition` to `SCHD` is accepted (400 otherwise).
- `customFields` validated against the `vendor_payment` workflow's field definitions if seeded (≤15, type/required/enum/regex) — no-ops if unseeded, mirrors `payment.validateCustom`.

**Applications / Refunds**
- `amount` > 0.
- Application capped at `min(payment.unapplied_amount, bill.BalanceDue())`; rejected (400) above that, never clamped.
- Refund capped at `application_amount − already-refunded`; rejected (400) above that.
- Rejected (400) on a payment/bill vendor mismatch.
- Rejected (409) if payment status is `VOID`, or (for Apply only) if the bill isn't in `{APPV, PART}`.
- Rejected (404, IDOR) if the caller's scope doesn't cover the target bill.

**Transitions** — only moves in the maps in §7; else 409. Manual `PAPV→APPV` always 409 (must use `/approve`). `VOID` cascades (AD-8).

**Approve** — 403 `ErrNotApprover` if the caller isn't a configured approver for the current status; 409 `ErrApprovalNotRequired` if no approvers are configured there.

**Delete** — rejected (409) if any live `vendor_payment_application` row references this payment.

**Tenant/RBAC/IDOR** — identical to every sibling: `authz.Check` before every mutation; every single-record op scope/IDOR-guarded, 404 on denial, `idor_denied`/`permission_denied` logged; scope composed into SQL, never filtered in Go.

---

## 12. Backend Implementation Map

| Concern | Action | Reference to mirror |
|---|---|---|
| Schema | Append to `database/migrations/tenant/schema.sql` via **add-migration** skill, after the `vendor` block | `vendor` block |
| RBAC | `ResourceVendorBill`/`ResourceVendorPayment` already in `authz/catalog.go` — no change needed | — |
| Route registration | `main.go`, new block alongside other Purchases/AP modules | `main.go` payment/purchaseorder blocks |
| Vendor Bill package | New `vendorbill/`: `types.go`, `balance.go` (RecomputeBalance/DeriveStatus/PayableStatuses), `transitions.go`, `numbering.go`, `resolver.go`, `store.go` | `invoice/balance.go`, `vendors/store.go` |
| Vendor Bill controller | New `controllers/vendorbill.go` + `controllers/vendorbill_audit.go` | `controllers/payment.go` skeleton (per `new-module` skill guidance) |
| Vendor Payment package | New `vendorpayment/`: `types.go`, `money.go`, `numbering.go`, `transitions.go`, `resolver.go`, `approval.go` (AD-6), `store.go`+`store_create.go`+`store_update.go`+`store_search.go`+`store_transition.go` (300-line cap), `apply.go` (Apply/Unapply), `refund.go` (RecordRefund/RemoveRefund), `scheduler.go` (AD-9) | `payment/apply.go`, `purchaseorder/approval.go`, `main.go` RAG-indexing block |
| Vendor Payment controller | New `controllers/vendorpayment.go` + `_audit.go` + `_transition.go` | `controllers/payment.go`, `payment_transition.go`, `payment_audit.go` |
| Scheduler wiring | `startVendorPaymentScheduler(ctx, cp, router)` called from `main.go` alongside `startRAGIndexing` | `main.go:1058-1109` |
| Tests | Table-driven for `transitions`/`numbering`/`resolver`/apply-cap math on both packages; `dbtest`-tagged integration tests for store/apply/unapply/refund/approval/scheduler; filter-invariant tests for both resolvers | `payment/*_test.go`, `purchaseorder/approval_test.go` (if present) |
| Review | **module-drift-checker**, **tenancy-security-reviewer**, **migration-auditor**, **filter-invariant-checker** before calling this done | — |

---

## 13. Open Decisions — Resolved During Brainstorming

1. **Don't merge `feat/vendor-bills`; build a minimal `vendor_bill` fresh in this branch instead.** Explicit user direction, twice-confirmed. Accepted trade-off: a future merge of the real module must extend via `ALTER TABLE ADD COLUMN IF NOT EXISTS`, not redefine `CREATE TABLE vendor_bill` (AD-1).
2. **No notification system.** Confirmed via `AskUserQuestion` — this repo has no notification infra to reuse; audit log + `GET .../audit` only (AD-13).
3. **Refund = a ledger entry on the payment, not a request/approval sub-workflow, not a vendor-credit alias.** Confirmed; drives AD-5's design (separate netted table, not a mutated application or a full mirrored `refund/`-style module).
4. **Scheduling = a real auto-transition background job**, not just a status+date field. Confirmed; this is the one genuinely new *kind* of infrastructure in the design (AD-9) — flagged explicitly since nothing else in this module introduces a new architectural pattern, everything else is copy-the-twins.
5. **Approval = the full AD-6 configurable-approver pattern**, not a single-level flag and not skipped entirely. Confirmed; reuses `purchaseorder/approval.go` verbatim (AD-6).
6. **Cancellation and reversal share one `VOID` status and one `/transition` call**, distinguished only by originating status, rather than two new statuses or two new endpoints. My own design call during presentation, not separately asked — flagged here in case it should be revisited (AD-8).
