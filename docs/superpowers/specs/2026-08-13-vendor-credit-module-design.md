# Vendor Credit Module — Backend Design Spec

**Date:** 2026-08-13
**Status:** Draft — proceeding to implementation plan.
**Scope:** New Vendor Credit module (header + bill-application ledger) for the
StoneSuite multi-tenant, database-per-tenant CRM/ERP backend — the accounts-payable
mirror of the existing `credit_memo` module, applying against `vendor_bill` instead of
`invoice`.

---

## 1. Overview & Goals

A Vendor Credit is an amount a vendor owes the company back (return, overbilling,
rebate) that reduces the outstanding balance of the company's future Vendor Bills to
that vendor. Supports Create, Update (Draft only), Approve, Apply, Reverse, Cancel, Get,
Search, soft delete, and full audit history, per the request.

**Non-negotiable constraints (from CLAUDE.md, identical to every sibling module):**

- Database-per-tenant; no `tenant_id` column anywhere.
- v2 relational conventions: hybrid PK (`SERIAL` + `UUID`), `employee(employee_id)`-based
  audit columns, paired soft delete, `record_version` optimistic concurrency.
- Idempotent, append-only migrations (`CREATE TABLE IF NOT EXISTS`,
  `ADD COLUMN IF NOT EXISTS`, `ON CONFLICT DO NOTHING`).
- Mandatory security chain on every `/api/tenant/` route.
- All list/search goes through `query/` (whitelisted `FieldResolver`, parameterized
  values, keyset pagination, filter × scope ANDed).

### Key finding that shaped this design

`lkp_record_type` already carries `('VCRD', 'vendorcredit', 'Vendor Credit', TRUE, TRUE,
1)` (`tenant/schema.sql:710`, record_type id 17), with statuses `DRFT/APPV/APPL/VOID`
already seeded for it (`tenant/schema.sql:752`) — the **exact same status set** as
`CRDT`/`credit_memo` (record_type 9). `authz.catalog.go:327-331` already carries
`ResourceVendorCredit` with `Create/Read/Update/Delete/Transition`. This is the same
"pre-seeded, unused until now" pattern that `VBIL`/`VPAY` showed before the Vendor Bill
and Vendor Payment modules were built — confirming Vendor Credit was always intended as
a genuine sibling of `credit_memo`, on the AP side. No new `lkp_record_type` or
`lkp_record_status` rows are needed. Only `ActionApprove` is missing from the catalog
(added here, mirroring `ResourceCreditMemo`/`ResourceRefund`/`ResourceItemReceipt`, each
of which treats "approve" as a capability distinct from "move the record around").

There is also a legacy v1 JSONB `workflows` seed row `key='vendor_credit'`
(`tenant/schema.sql:2091-2125`) and a `resourceForKey("vendor_credit")` mapping in
`controllers/crm.go:85-86`. Both predate this module (same situation `vendor_bill`/
`vendor_payment` were in) and are for the generic JSONB engine — left untouched, no
change needed.

### What already exists (reuse, do not recreate)

| Concern | Existing asset | Location |
|---|---|---|
| Vendor (counterparty) | `vendor` (relational) | `vendors/` package |
| Vendor Bill (settlement target) | `vendor_bill` (relational) | `vendorbill/` package |
| Record type "Vendor Credit" | `lkp_record_type` row `VCRD` (id 17) — already seeded | `tenant/schema.sql:710` |
| Vendor Credit status lifecycle | `DRFT, APPV, APPL, VOID` for record_type=17 — already seeded | `tenant/schema.sql:752` |
| RBAC resource | `authz.ResourceVendorCredit` — CRUD+Transition already seeded | `authz/catalog.go:58,327-331` |
| Direct architectural precedent | `credit_memo` / `credit_memo_application` / `invoice.RecomputeBalance` | `creditmemo/`, `invoice/balance.go` |
| AP balance identity to extend | `vendorbill.RecomputeBalance` (currently sums bill-ledger + vendor-payment applications only) | `vendorbill/balance.go` |
| Filter/sort/paginate/search | `query/` package | `query/` |
| Audit log | `audit_logs` via `workflow.LogAuditFull` | `controllers/vendorbill_audit.go` (pattern) |
| Row-level IDOR guard | `recordInScope` | `controllers/scope.go` |
| Dual-permission mutation-with-side-effect pattern | `creditmemo.go`'s `invoiceInScopeForUpdate` | `controllers/creditmemo.go:87-124` |

### What is genuinely missing (new — justified in §3)

- `vendor_credit`, `vendor_credit_history`, `vendor_credit_application` — the full
  Vendor Credit module, structurally identical to `credit_memo`/`credit_memo_history`/
  `credit_memo_application`.
- `vendor_bill.vendor_bill_credit_total` — one new rollup column (`ALTER TABLE ADD
  COLUMN IF NOT EXISTS`), mirroring `invoice.invoice_credit_total`, so a vendor credit's
  effect on a bill's balance is tracked separately from cash (`vendor_bill_amount_paid`)
  — exactly how `credit_memo`'s effect on an invoice is separate from `payment`'s.
- `{ResourceVendorCredit, ActionApprove}` catalog row.

---

## 2. Architecture Decisions

**AD-1 — Vendor Credit is a direct structural mirror of `credit_memo`, header-only (no
line items).** Table shape, transitions, apply/reverse semantics, and file layout copy
`creditmemo/` verbatim with `vendor`↔`customer` and `vendor_bill`↔`invoice` swapped.
Unlike `credit_memo` (which typically arises from itemized returns and carries lines +
tax + discount calc), a Vendor Credit is a single amount — the user's request and the
pre-existing v1 JSONB workflow seed (`credit_amount`, `reason` fields only, no lines)
both confirm this. This drops `calc.go`/`store_line_resolve.go`-equivalent machinery
entirely: no `ComputeHeader`, no per-line resolution, no `≥1 line` validation.

**AD-2 — `Approve` is the generic `/transition` endpoint gated by `ActionApprove` when
the target is `APPV`, not a dedicated endpoint or an AD-6 multi-approver gate.** VCRD's
seeded status set (`DRFT/APPV/APPL/VOID`) matches `CRDT`'s simple set, not `VPAY`'s
`PAPV`/`APPV` split with dedicated approver-config tables — so this mirrors
`creditmemo`'s `actionForTransition` pattern (`controllers/creditmemo_transition.go`)
exactly, not `vendorpayment`'s AD-6 pattern. No new approver-config tables.

**AD-3 — `Apply` allocates unapplied credit to a vendor bill; caps at
`min(credit.unapplied_amount, bill.BalanceDue())`, rejects (400) rather than clamping
above that.** Mirrors `creditmemo.Apply` verbatim. Rejects (409) unless the credit is in
`{APPV, APPL}` (mirrors `appliableStatuses`), rejects (400) on a vendor mismatch, rejects
(409) unless the bill is in `vendorbill.PayableStatuses` (`{APPV, PART, ODUE}` — the
concrete status set behind the request's informal "Open, Approved, Partially Paid"), and
rejects (404, IDOR) if the caller's scope doesn't cover the bill.

**AD-4 — `Reverse` (the request's name for what `credit_memo` calls `Unapply`) soft-
deletes the live application row for (credit, bill) and recomputes both rollups. No
status gate — a reversal must always be possible.** Same operation as
`creditmemo.Unapply`, renamed to match the requested API vocabulary exactly (the
request lists Create/Update/Approve/Apply/Reverse/Cancel/Get/Search as the eight named
operations).

**AD-5 — `Cancel` is `Transition(..., "VOID", ...)` through the generic `/transition`
endpoint.** Mirrors `creditmemo.Transition`'s VOID path exactly: reverses every live
application in the same transaction before voiding (so cash — here, credit — already
consumed is unwound, not stranded), and `APPL → VOID` stays absent from the transition
map (an exhausted credit must be reversed first, mirroring `credit_memo` AD-14's
reasoning that "this credit was spent" should be a real, recoverable-only-by-reversal
state).

**AD-6 — Lock order `vendor_credit < vendor_bill`, a new hierarchy that does not overlap
the AR side's `credit_memo < payment < invoice` or the AP side's `vendor_payment <
vendor_bill`.** `vendor_bill` is always locked last in every hierarchy that touches it,
so no cross-hierarchy cycle — hence no deadlock — is possible. `Apply`/`Reverse` row-lock
`vendor_credit` first (`FOR UPDATE`), then `vendor_bill`, mirroring
`creditmemo.lockCreditMemoForUpdate` → `invoice.LockForUpdate`.

**AD-7 — `vendor_bill.RecomputeBalance` (`vendorbill/balance.go`) is extended to sum a
third live ledger.** Today it sums the bill-owned `vendor_bill_payment` ledger and
`vendor_payment_application` net `vendor_payment_refund` into `amount_paid`. This adds a
parallel, separately-tracked `vendor_bill_credit_total` computed from live
`vendor_credit_application` rows net nothing (credit applications have no refund
concept — `Reverse` soft-deletes the row outright), and `BalanceDue()` becomes
`grand_total - amount_paid - credit_total` — the exact shape `invoice.BalanceDue()`
already uses for `invoice_amount_paid`/`invoice_credit_total`. `DeriveStatus` gains a
`settled = amountPaid + creditTotal` input, mirroring `invoice.DeriveStatus`. This is the
one cross-cutting change into an existing module this design makes, and it is required:
without a separate `credit_total`, a credit application would have nothing to write to
that isn't already claimed by the cash-only `amount_paid` semantics documented in
`vendorbill/balance.go`'s own comments.

**AD-8 — "Reject inactive vendors" is enforced by a new, vendor-credit-local vendor
snapshot check requiring `vendor_status = ACT_`.** `vendorbill.vendorSnapshot` (reused
by `vendorbill`/`vendorpayment`/`purchaseorder`/`requisition`) only checks
`vendor_deleted_at IS NULL`, not active status — loosening that shared helper for every
existing caller is out of scope for this request. `vendorcredit`'s own snapshot helper
additionally joins `lkp_record_status` and requires the `ACT_` code, rejecting (400)
otherwise.

**AD-9 — "Reject inactive vendor payments" is already satisfied by existing code; no new
logic.** `vendorbill.RecomputeBalance`'s `vendor_payment_application` sum already filters
`vp.vendor_payment_deleted_at IS NULL`, and a voided `vendor_payment` has already had
every live application cascade-reversed (`vendor_payment` AD-8) by the time it reaches
`VOID` — so a bill's live balance never includes anything from an inactive payment. This
is documented and covered by a dbtest, not re-implemented.

**AD-10 — No line items, no tax, no shipping.** `GrandTotal` is the credit's face amount,
entered directly at Create (not derived from lines). `AppliedTotal`/`UnappliedAmount` are
rollups from `vendor_credit_application`, exactly like `credit_memo`'s.

**AD-11 — No notification system is built.** Grepped clean — this repo has no
notification table, dispatch mechanism, or precedent anywhere (same finding
`vendorpayment` AD-13 already made). Approval/apply/reverse/cancel events surface only
through `vendor_credit_history` + `GET .../audit`, same as every sibling module.

**AD-12 — Vendor is fixed at creation, name snapshotted** (mirrors `credit_memo_customer_id`/
`credit_memo` generally, and `vendor_bill_vendor_id`/`vendor_bill_vendor_name`).

---

## 3. New Tables — Per-Table Justification

### `vendor_credit`
- **(a)** No table models a credit owed back by a vendor.
- **(b)** Header only: identity, vendor (fixed), amount, reason, stored applied/unapplied
  rollup.
- **(c)** New master (hybrid PK). FKs to `vendor`, `employee`, `lkp_record_type`(=VCRD),
  `lkp_record_status`.

### `vendor_credit_history`
- **(b)** One row per status change / apply / reverse, mirrors `credit_memo_history`.
- **(c)** New child of `vendor_credit`; FKs to `lkp_record_status`, `employee`.

### `vendor_credit_application`
- **(a)** The credit↔bill ledger; no existing table fits (a credit may fund zero, one,
  or several bills — same "one instrument, many targets" shape as `credit_memo_application`
  and `vendor_payment_application`).
- **(b)** One row per live allocation of a credit's amount to a bill's balance.
- **(c)** New junction; FKs to `vendor_credit` (CASCADE) and `vendor_bill` (RESTRICT).

---

## 4. SQL — Schema Changes

> Appended to `database/migrations/tenant/schema.sql` via the **add-migration** skill,
> after the `vendor_bill`/`vendor_payment` blocks (both new blocks FK `vendor` and
> `vendor_bill`). Money `DECIMAL(15,2)`, consistent with every sibling. No new
> `lkp_record_type`/`lkp_record_status` rows — `VCRD` (id 17) and its four statuses are
> already seeded (§1).

### 4.1 `{ResourceVendorCredit, ActionApprove}` — `authz/catalog.go`

```go
{ResourceVendorCredit, ActionApprove},
```//added directly after the existing ResourceVendorCredit block (catalog.go:327-331), mirroring the ActionApprove comment style already used for ResourceCreditMemo/ResourceRefund/ResourceItemReceipt.

### 4.2 `vendor_bill_credit_total` — extend the existing table

```sql
ALTER TABLE vendor_bill ADD COLUMN IF NOT EXISTS vendor_bill_credit_total DECIMAL(15,2) NOT NULL DEFAULT 0;
```

### 4.3 `vendor_credit` (header)

```sql
CREATE TABLE IF NOT EXISTS vendor_credit (
    vendor_credit_id                SERIAL        PRIMARY KEY,
    vendor_credit_uuid              UUID          NOT NULL DEFAULT gen_random_uuid(),
    vendor_credit_number            VARCHAR(20)       NULL,  -- 'VCR-000001', generated post-insert in Go

    record_type                     INTEGER       NOT NULL REFERENCES lkp_record_type(record_type_id),   -- = VCRD (17)
    vendor_credit_status             INTEGER       NOT NULL REFERENCES lkp_record_status(record_status_id),

    vendor_credit_vendor_id           INTEGER       NOT NULL REFERENCES vendor(vendor_id),  -- fixed at creation
    vendor_credit_vendor_name         VARCHAR(150)  NOT NULL DEFAULT '',                      -- snapshot

    vendor_credit_reference_number     VARCHAR(50)   NOT NULL DEFAULT '',
    vendor_credit_date                  DATE          NOT NULL DEFAULT CURRENT_DATE,
    vendor_credit_reason                 VARCHAR(150)  NOT NULL DEFAULT '',
    vendor_credit_memo                    TEXT          NOT NULL DEFAULT '',
    vendor_credit_internal_notes           TEXT          NOT NULL DEFAULT '',

    vendor_credit_owner_id                  INTEGER           NULL REFERENCES employee(employee_id),

    vendor_credit_grand_total                DECIMAL(15,2) NOT NULL,                          -- face amount, entered directly
    vendor_credit_applied_total               DECIMAL(15,2) NOT NULL DEFAULT 0,                -- rollup
    vendor_credit_unapplied_amount             DECIMAL(15,2) NOT NULL DEFAULT 0,               -- rollup

    vendor_credit_custom_fields                 JSONB        NOT NULL DEFAULT '{}',
    vendor_credit_created_at                     TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    vendor_credit_created_by                      INTEGER          NULL REFERENCES employee(employee_id),
    vendor_credit_updated_at                       TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    vendor_credit_updated_by                        INTEGER          NULL REFERENCES employee(employee_id),
    vendor_credit_deleted_at                         TIMESTAMP        NULL,
    vendor_credit_deleted_by                          INTEGER          NULL REFERENCES employee(employee_id),
    vendor_credit_record_version                      INTEGER      NOT NULL DEFAULT 1,

    CONSTRAINT uq_vendor_credit_uuid       UNIQUE (vendor_credit_uuid),
    CONSTRAINT uq_vendor_credit_number     UNIQUE (vendor_credit_number),
    CONSTRAINT chk_vcrd_amount_pos          CHECK (vendor_credit_grand_total > 0),
    CONSTRAINT chk_vcrd_applied_nonneg      CHECK (vendor_credit_applied_total >= 0 AND vendor_credit_unapplied_amount >= 0),
    CONSTRAINT chk_vcrd_applied_le_amt      CHECK (vendor_credit_applied_total <= vendor_credit_grand_total),
    CONSTRAINT chk_vcrd_soft_delete         CHECK (
        (vendor_credit_deleted_at IS NULL AND vendor_credit_deleted_by IS NULL) OR
        (vendor_credit_deleted_at IS NOT NULL AND vendor_credit_deleted_by IS NOT NULL)
    )
);

CREATE TABLE IF NOT EXISTS vendor_credit_history (
    vendor_credit_history_id   SERIAL       PRIMARY KEY,
    vendor_credit_id            INTEGER      NOT NULL REFERENCES vendor_credit(vendor_credit_id) ON DELETE CASCADE,
    from_status_id                INTEGER          NULL REFERENCES lkp_record_status(record_status_id),
    to_status_id                   INTEGER          NULL REFERENCES lkp_record_status(record_status_id),
    action                          VARCHAR(32)  NOT NULL DEFAULT 'transition',  -- create|update|transition|apply|reverse
    actor_employee_id                INTEGER          NULL REFERENCES employee(employee_id),
    snapshot                          JSONB        NOT NULL DEFAULT '{}',
    at                                 TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS vendor_credit_application (
    application_id              SERIAL        PRIMARY KEY,
    application_uuid             UUID          NOT NULL DEFAULT gen_random_uuid(),
    vendor_credit_id              INTEGER       NOT NULL REFERENCES vendor_credit(vendor_credit_id) ON DELETE CASCADE,
    vendor_bill_id                 INTEGER       NOT NULL REFERENCES vendor_bill(vendor_bill_id),

    application_amount              DECIMAL(15,2) NOT NULL,

    application_created_at           TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    application_created_by            INTEGER          NULL REFERENCES employee(employee_id),
    application_deleted_at             TIMESTAMP        NULL,  -- set = "reversed"
    application_deleted_by              INTEGER          NULL REFERENCES employee(employee_id),
    application_record_version           INTEGER      NOT NULL DEFAULT 1,

    CONSTRAINT uq_vendor_credit_application_uuid UNIQUE (application_uuid),
    CONSTRAINT chk_vcrd_app_amount_pos            CHECK (application_amount > 0),
    CONSTRAINT chk_vcrd_app_soft_delete           CHECK (
        (application_deleted_at IS NULL AND application_deleted_by IS NULL) OR
        (application_deleted_at IS NOT NULL AND application_deleted_by IS NOT NULL)
    )
);

-- At most one LIVE application per (vendor_credit, vendor_bill) pair -- re-applying
-- increases the existing row's amount instead of creating a duplicate (mirrors
-- uq_cm_app_live_pair / uq_vpay_app_live_pair).
CREATE UNIQUE INDEX IF NOT EXISTS uq_vcrd_app_live_pair
    ON vendor_credit_application (vendor_credit_id, vendor_bill_id) WHERE application_deleted_at IS NULL;
```

### 4.4 Indexes

```sql
CREATE INDEX IF NOT EXISTS idx_vcrd_vendor        ON vendor_credit (vendor_credit_vendor_id) WHERE vendor_credit_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vcrd_status         ON vendor_credit (vendor_credit_status)    WHERE vendor_credit_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vcrd_date           ON vendor_credit (vendor_credit_date)      WHERE vendor_credit_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vcrd_owner          ON vendor_credit (vendor_credit_owner_id)  WHERE vendor_credit_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vcrd_created_id     ON vendor_credit (vendor_credit_created_at, vendor_credit_id) WHERE vendor_credit_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vcrd_unapplied_id   ON vendor_credit (vendor_credit_unapplied_amount, vendor_credit_id) WHERE vendor_credit_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vcrd_custom_gin     ON vendor_credit USING GIN (vendor_credit_custom_fields);
CREATE INDEX IF NOT EXISTS idx_vcrd_history_credit ON vendor_credit_history (vendor_credit_id);
CREATE INDEX IF NOT EXISTS idx_vcrd_app_credit     ON vendor_credit_application (vendor_credit_id) WHERE application_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vcrd_app_bill       ON vendor_credit_application (vendor_bill_id)     WHERE application_deleted_at IS NULL;
```

---

## 5. Status Transition Rules (record_type=17, VCRD; statuses already seeded)

```go
var allowedTransitions = map[string]map[string]bool{
    "DRFT": {"APPV": true, "VOID": true},
    "APPV": {"APPL": true, "VOID": true},
    "APPL": {},   // must Reverse first -- exhausted credit is not directly voidable
    "VOID": {},
}
```

- New credits start at **DRFT**.
- `DRFT → APPV` requires `vendor_credit:approve` (AD-2); every other move requires
  `vendor_credit:transition`.
- `APPV → APPL` is derived by `Apply`, never user-directed (mirrors `credit_memo`): a
  credit becomes Applied exactly when its unapplied balance reaches zero, and drops back
  to `APPV` the instant any of it is reversed.
- `VOID` from `DRFT`/`APPV` cascades: every live `vendor_credit_application` is reversed
  in the same transaction (AD-5).

---

## 6. API Contracts

All under `/api/tenant/`, through `tenantChain`, RBAC-checked in-handler, IDOR-guarded
(404 on scope denial), same envelope as every sibling (`{success, message?, ...}`).

| Method & path | Purpose | RBAC |
|---|---|---|
| `GET  /api/tenant/vendor-credits` | Simple in-scope list, cursor-paginated | `vendor_credit:read` + scope |
| `POST /api/tenant/vendor-credits/search` | Full filter + sort + search + pagination | `vendor_credit:read` + scope |
| `POST /api/tenant/vendor-credits` | Create | `vendor_credit:create` |
| `GET  /api/tenant/vendor-credits/{uuid}` | Get one (+ live applications) | `vendor_credit:read` + IDOR |
| `PATCH /api/tenant/vendor-credits/{uuid}` | Update (DRFT only) | `vendor_credit:update` + IDOR |
| `DELETE /api/tenant/vendor-credits/{uuid}` | Soft delete; 409 if live applications exist | `vendor_credit:delete` + IDOR |
| `POST /api/tenant/vendor-credits/{uuid}/transition` | Status change (§5); `DRFT→APPV` needs `approve`, `→VOID` (Cancel) needs `transition` | `vendor_credit:transition` or `:approve` + IDOR |
| `POST /api/tenant/vendor-credits/{uuid}/apply` | `{vendorBillUuid, amount}` | `vendor_credit:update` + IDOR, **and** `vendor_bill:update` scope-check |
| `POST /api/tenant/vendor-credits/{uuid}/reverse` | `{vendorBillUuid}` | same dual-check |
| `GET  /api/tenant/vendor-credits/{uuid}/audit` | Audit / history trail | `vendor_credit:read` + IDOR |

**Create request**
```json
POST /api/tenant/vendor-credits
{
  "vendorUuid": "9d0f…c2",
  "referenceNumber": "RMA-4471",
  "creditDate": "2026-08-13",
  "reason": "Returned defective slab",
  "amount": 850.00,
  "memo": "",
  "customFields": {}
}
→ 201 { "success": true, "vendorCredit": { "id": "…", "vendorCreditNumber": "VCR-000001",
        "status": "Draft", "grandTotal": 850.00, "appliedTotal": 0, "unappliedAmount": 850.00 } }
```

**Apply / Reverse** follow the `{vendorBillUuid[, amount]}` shape of `creditmemo`'s
`/apply`/`/unapply`, returning the updated credit (with `400` on cap-exceeded, `404` on
no-live-application for reverse).

---

## 7. Listing & Query Architecture

Reuses `query/` unchanged. New: `vendorcredit.resolver` (`FieldResolver` + `SortResolver`
+ `SearchResolver`), `vendorcredit.Search` (keyset), mirroring `vendorbill.resolver`
1:1 on its own columns.

**FieldResolver whitelist** (alias `vc`): `id`, `document_number`, `vendor_id`, `status`,
`reference_number`, `credit_date`, `reason`, `grand_total`, `applied_total`,
`unapplied_amount`, `owner_id`, `created_at`, `updated_at`, `cf:<key>`.

**SortResolver whitelist:** `document_number, credit_date, grand_total,
unapplied_amount, status, vendor_id, created_at, updated_at` — each `NOT NULL`, paired
with `vendor_credit_id` tiebreaker.

**SearchPredicate:** `vendor_credit_number`, `vendor_credit_reference_number`,
`vendor_credit_reason`, `vendor_credit_memo`, `vendor_credit_vendor_name` ILIKE, plus an
`EXISTS` sub-select on `vendor` legal/given/family name (mirrors `vendorbill.resolver`).

**Response envelope:** identical to every sibling — `{success, scope, records,
nextCursor, hasMore}`. Keyset only, no offset.

---

## 8. Validation Rules

**Header**
- `vendorUuid` required; must resolve to a live, **active** (`vendor_status = ACT_`)
  vendor in this tenant (AD-8); caller must have scope on it.
- `amount` > 0.
- `customFields` validated against the `vendor_credit` workflow's field definitions if
  seeded (≤15, type/required/enum/regex) — no-op if unseeded.

**Apply / Reverse**
- `amount` > 0 (Apply only).
- Application capped at `min(credit.unapplied_amount, bill.BalanceDue())`; rejected
  (400) above that, never clamped.
- Rejected (400) on a credit/bill vendor mismatch.
- Rejected (409) if the credit isn't `{APPV, APPL}` (Apply), or if the target bill isn't
  live and in `vendorbill.PayableStatuses` (`{APPV, PART, ODUE}` — active/non-deleted/
  non-cancelled bill in a valid status, per the request).
- Rejected (404, IDOR) if the caller's scope doesn't cover the target bill.

**Transitions** — only moves in §5's map; else 409. `DRFT→APPV` requires
`vendor_credit:approve`. `VOID` cascades (AD-5).

**Delete** — rejected (409) if any live `vendor_credit_application` row references this
credit.

**Tenant/RBAC/IDOR** — identical to every sibling: `authz.Check` before every mutation;
every single-record op scope/IDOR-guarded, 404 on denial, `idor_denied`/
`permission_denied` logged; scope composed into SQL, never filtered in Go.

---

## 9. Backend Implementation Map

| Concern | Action | Reference to mirror |
|---|---|---|
| RBAC | Add `{ResourceVendorCredit, ActionApprove}` to `authz/catalog.go` | `ResourceCreditMemo` block |
| Schema | Append to `database/migrations/tenant/schema.sql` via **add-migration** skill: `vendor_bill_credit_total` column + `vendor_credit`/`_history`/`_application` tables + indexes | §4 above |
| `vendor_bill` balance extension | Extend `vendorbill/balance.go`: `Locked.CreditTotal`, `BalanceDue()` subtracts it, `RecomputeBalance` sums live `vendor_credit_application`, `DeriveStatus` takes `settled` | `invoice/balance.go` (AD-7) |
| Vendor Credit package | New `vendorcredit/`: `types.go`, `numbering.go`, `transitions.go`, `store.go` (helpers/Get/ErrNotFound/ClientError), `store_create.go`, `store_update.go`, `store_transition.go`, `store_search.go`+`resolver.go`, `apply.go` (Apply/Reverse) | `creditmemo/*` with `credit_memo`↔`vendor_credit`, `invoice`↔`vendorbill`, `customer`↔`vendor` swapped |
| Vendor Credit controller | New `controllers/vendorcredit.go` (CRUD+List+Search), `controllers/vendorcredit_transition.go` (Transition+Apply+Reverse), `controllers/vendorcredit_audit.go` | `controllers/creditmemo*.go` |
| Route registration | `main.go`, new block alongside the Vendor Bill/Vendor Payment blocks | `main.go:849-879` |
| Tests | Table-driven for `transitions`/`numbering`/`resolver`/apply-cap math; `dbtest`-tagged integration tests for store/apply/reverse/cancel/rollback/concurrency/cross-tenant/RBAC | `creditmemo/*_test.go`, `vendorbill/*_test.go` |
| Review | **module-drift-checker**, **tenancy-security-reviewer**, **migration-auditor**, **filter-invariant-checker** before calling this done | — |

---

## 10. Open Decisions — Resolved During Classification/Design

1. **Header-only, no line items.** The request describes a flat credit amount; the
   pre-existing v1 workflow seed for `vendor_credit` (`credit_amount`, `reason` fields
   only) independently confirms this. Deliberately simpler than `credit_memo`.
2. **"Reverse" and "Cancel" map onto `credit_memo`'s existing `Unapply` and
   `Transition(...,"VOID",...)` operations respectively**, renamed/exposed to match the
   request's explicit API vocabulary — not two new kinds of operation.
3. **No dedicated `/approve` endpoint, no AD-6 multi-approver config.** VCRD's seeded
   status set matches `CRDT`'s simple gate, not `VPAY`'s configurable one.
4. **"Reject inactive vendor payments" needs no new code** — already guaranteed by
   `vendorbill.RecomputeBalance`'s existing live-row filtering plus `vendor_payment`'s
   own VOID-cascade behavior. Covered by a dbtest, not new logic.
5. **No notification system** — grepped clean, matches `vendorpayment` AD-13's finding.
   Audit trail (`vendor_credit_history` + `GET .../audit`) is the notification surface,
   same as every sibling.
