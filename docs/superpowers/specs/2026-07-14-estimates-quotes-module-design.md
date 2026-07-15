# Estimates & Quotes Modules — Backend Design Spec

**Date:** 2026-07-14
**Status:** Approved architecture; ready for implementation planning
**Scope:** New Estimate module and new Quote module (each: header + line items + status workflow + configuration-driven approval + revision history), the Estimate → Quote conversion, and the Quote → Sales Order conversion, for the StoneSuite multi-tenant, database-per-tenant CRM/ERP backend. Built as the pipeline stage that precedes `sales_order` (`docs/superpowers/specs/2026-07-08-sales-order-module-design.md`) and `invoice` (`docs/superpowers/specs/2026-07-10-invoice-module-design.md`).

---

## 1. Overview & Goals

Add production-grade **Estimate** and **Quote** modules that are seamless extensions of the v2 relational stack — same conventions as `customer`, `sales_order`, and `invoice`: hybrid PK, employee-based audit, soft delete, `record_version`, RBAC/scope/IDOR, the `query/` filter engine, keyset pagination, configuration-driven approval, R2 attachments. Not a parallel architecture.

**Non-negotiable constraints (from CLAUDE.md, identical to the Sales Order and Invoice modules):**

- Database-per-tenant; no `tenant_id` column anywhere.
- v2 relational conventions: hybrid PK (`SERIAL` + `UUID`), `employee(employee_id)`-based audit columns, paired soft delete, `record_version` optimistic concurrency.
- Idempotent, append-only migrations (`CREATE TABLE IF NOT EXISTS`, `ON CONFLICT DO NOTHING`). **No `ALTER TABLE ADD COLUMN` on any table that already exists in this codebase** — this specifically shapes the Quote→Sales Order lineage design (§9.2).
- Mandatory security chain on every `/api/tenant/` route.
- All list/search goes through `query/` (whitelisted `FieldResolver`, parameterized values, keyset pagination, filter × scope ANDed).

### What already exists (reuse, do not recreate)

| Concern | Existing asset | Location |
|---|---|---|
| Billing customer | `customer` (v2 relational master) | `tenant/schema.sql:1087` |
| Actor / owner / sales rep | `employee` | `tenant/schema.sql:511` |
| Record type "Estimate" | `lkp_record_type` row `ESTM` / `estimate` (id 4) | `tenant/schema.sql:694` |
| Record type "Quote" | `lkp_record_type` row `QUOT` / `quote` (id 5) | `tenant/schema.sql:695` |
| Estimate status lifecycle | `lkp_record_status` rows for `record_type=4`: `DRFT, PAPV, APPV, SENT, CANC, RJCT, EXPR` — **already seeded, unused until now** | `tenant/schema.sql:730-731` |
| Quote status lifecycle | `lkp_record_status` rows for `record_type=5`: `DRFT, PAPV, APPV, SENT, CANC, RJCT, EXPR, CONV` — **already seeded, unused until now** (note the `CONV`/"Converted" status Estimate lacks — a direct signal only Quote carries a terminal "became a Sales Order" state) | `tenant/schema.sql:732-733` |
| RBAC resources | `authz.ResourceEstimate` / `authz.ResourceQuote`, each already granted all 5 actions (`create/read/update/delete/transition`) | `authz/catalog.go:36-37,120-130` — **fully seeded, zero catalog changes needed** |
| Sales Order (conversion target) | `sales_order`, `sales_order_item` | `salesorder/` package + `tenant/schema.sql:2435-2573` |
| Payment terms (incl. net-days) | `lkp_payment_terms` (+ `payment_terms_net_days`, added in migration 029) | `tenant/schema.sql:2639-2640` |
| Price levels / Currency / Country / State / Unit / Tax rate | `lkp_price_level`, `lkp_currency`, `lkp_country`, `lkp_state`, `lkp_unit`, `lkp_tax_rate` | `tenant/schema.sql` |
| File attachments | `workflow_record_attachments` + R2, record-type-agnostic | `workflow/attachments.go` |
| Audit log | `audit_logs` (v2 path) | `tenant/schema.sql:1278` |
| Filter/sort/paginate/search | `query/` package | `query/` |
| Money/line calc pattern | `salesorder/calc.go`, `invoice/calc.go` | mirrored verbatim, see §8 |
| Status transition pattern | `salesorder/transitions.go`, `invoice/transitions.go` (static Go map) | mirrored, see §7 |
| Document numbering pattern | `salesorder/numbering.go`, `invoice/numbering.go` (Go post-insert format) | mirrored, see §2 AD-7 |
| Approval pattern (AD-10) | `sales_order_approver` / `sales_order_approval` + `salesorder/approval.go` | mirrored twice, see §5 |

> **Key finding, mirrors the Sales Order and Invoice precedent exactly:** "Estimate" and "Quote" today exist only as bare **v1 generic JSONB workflows** (`workflows.key IN ('estimate','quote')`, seeded at `schema.sql:1543-1615`, 4 flat custom fields each, no line items, no customer/document linkage). They have **no relational table or store**. This design introduces dedicated v2 relational table sets — siblings of `sales_order`/`invoice` — reusing the `ESTM`/`QUOT` record-types and the `lkp_record_status` rows already reserved for them. The v1 workflows' field names (`customer_name`, `total_amount`, `valid_until`, `notes`) directly validate the header columns chosen below. The legacy v1 workflows are left untouched (no production data); the relational modules supersede them.

### What is genuinely missing (new tables — justified in §3)

- `estimate`, `estimate_item`, `estimate_history`, `estimate_approver`, `estimate_approval` — no existing table represents a multi-line, snapshot-priced, approvable pre-sales document.
- `quote`, `quote_item`, `quote_history`, `quote_approver`, `quote_approval` — same, plus lineage back to a source Estimate.
- `quote_conversion` — no existing table can record which Sales Order(s) a Quote became, since `sales_order`/`sales_order_item` already exist and cannot be `ALTER`ed to hold a lineage column pointing at a table (`quote`) that doesn't exist yet at the time they were created.

---

## 2. Architecture Decisions

**AD-1 — Dedicated relational tables, not the JSONB engine.** Same reasoning as Sales Order (AD-1) and Invoice (AD-1): line items, stored money, snapshots, and approval don't fit the 15-field JSONB `workflow_records` model.

**AD-2 — Estimate and Quote are each dedicated modules, not variants of one table.** Unlike the CRM lead→prospect→customer flow (which is same-table, differentiated by `record_type`), Estimate and Quote get their own tables, packages (`estimate/`, `quote/`), and controllers — matching how `sales_order` and `invoice` are siblings, not stages of one table. Rationale: each has a materially different column/line shape once approval and lineage are added, and keeping them separate avoids a sprawling polymorphic table with mostly-null columns.

**AD-3 — Hybrid PK everywhere.** Identical to every other v2 module: `SERIAL` internal, `UUID` external.

**AD-4 — Snapshot billing/shipping onto the header; snapshot line data onto items; frozen at creation, never re-read live.** Matches AD-4 in both prior specs exactly — pricing does not track the live catalog after a line is added (confirmed design decision).

**AD-5 — Estimate → Quote is a one-shot snapshot copy, not a live link.** `quote.quote_estimate_id` is a plain nullable FK on the *new* `quote` table pointing at `estimate` (no `ALTER` needed — `quote` doesn't exist yet at migration time, so any column shape is free to choose). One Estimate may produce multiple Quotes (e.g. quoting different option sets), mirroring the "non-destructive to source, repeatable" precedent from Invoice's SO→Invoice conversion (Invoice spec AD-8). Converting does **not** force the Estimate into a terminal status — it stays wherever it is, consistent with Estimate's status set having no `CONV`-equivalent.

**AD-6 — Quote → Sales Order lineage lives in a new `quote_conversion` mapping table, not a column on `sales_order`.** `sales_order` and `sales_order_item` already exist; CLAUDE.md forbids `ALTER TABLE ADD COLUMN` on existing tables. A dedicated table (`quote_id` FK, `sales_order_id` FK **unique**, `converted_at`/`converted_by`, `snapshot` JSONB) records each conversion event without touching `sales_order`'s schema at all — and is exactly the "conversion mapping" component called out in the project requirements. One Quote may convert into multiple Sales Orders (progress/phased conversion, same precedent as Invoice's SO→Invoice, AD-8); each conversion inserts one `quote_conversion` row. The **unique** constraint is on `sales_order_id` (each Sales Order traces back to exactly one conversion event) — **not** on `quote_id` (a Quote may appear in many rows). First conversion sets `quote_status = CONV`; further conversions remain permitted (`CONV` is not treated as a hard lock by the conversion service, only excluded from the *manual* transition map — see §7).

**AD-7 — In-place edit + history trail for revisions, no new "revision" table.** `{x}_record_version` bump + `{x}_history` row per mutation, identical to every existing module. A Sent/Approved document can be edited back to `DRFT` via the existing transition-back pattern (`PAPV`→`DRFT`) before re-editing, exactly like Invoice.

**AD-8 — Configuration-driven approval gate on both Estimate and Quote.** Both status lifecycles include `PAPV`/`APPV` in the already-seeded `lkp_record_status` rows — a direct signal both were meant to gate through approval before being sent to a customer. `estimate_approver`/`estimate_approval` and `quote_approver`/`quote_approval` are exact structural copies of `sales_order_approver`/`sales_order_approval` (Sales Order spec AD-10).

**AD-9 — Notes are header `TEXT` columns** (`{x}_notes`, `{x}_internal_notes`), matching `invoice`/`sales_order`. No dedicated notes table exists anywhere in this codebase to mirror, so none is introduced here.

**AD-10 — Attachments reuse the existing generic `workflow_record_attachments` + R2 mechanism as-is** (already record-type-agnostic). No new table.

**AD-11 — Document numbers generated in Go post-insert**, identical pattern to `salesorder/numbering.go`/`invoice/numbering.go`: `ESTM-000001` / `QUOT-000001`, matching each row's own `lkp_record_type.record_type_code` exactly.

**AD-12 — No self-referential `{x}_parent_id` "amendment" column.** `sales_order`/`invoice` each carry an unused `{x}_parent_id` self-reference reserved for a future amendment concept that isn't exercised by any code path today. Since AD-7 already covers revisions via in-place edit + history, adding an equivalent vestigial column here would be copying an unjustified pattern rather than a needed one — omitted by deliberate choice, not oversight.

---

## 3. New Tables — Per-Table Justification

### `estimate`
- **(a)** No existing table models a billable pre-sales document with line items and approval.
- **(b)** The estimate header: identity, classification, billing/shipping snapshot, terms, money totals, approval status.
- **(c)** New master (hybrid PK). FKs to `customer`, `employee`, `lkp_record_type`(=ESTM), `lkp_record_status`, `lkp_payment_terms`, `lkp_price_level`, `lkp_currency`, `lkp_state`, `lkp_country`.

### `estimate_item`
- **(a)** Line items are a distinct 1-to-many child; no existing table fits.
- **(b)** Estimated lines with item snapshots + stored per-line money.
- **(c)** New child of `estimate`; FKs to `inventory_item` (nullable — free-text lines allowed), `lkp_unit`, `lkp_tax_rate`.

### `estimate_history`
- **(a)** Generic `audit_logs` covers CRUD; the status lifecycle deserves a typed trail like `sales_order_history`/`invoice_history`.
- **(b)** One row per status change, with `from_status_id`/`to_status_id` + JSONB snapshot.
- **(c)** New child of `estimate`; FKs to `lkp_record_status`, `employee`.

### `estimate_approver` / `estimate_approval`
- **(a)** No existing approval-configuration table is keyed for `ESTM`.
- **(b)** Which employee(s) may approve an Estimate at `PAPV`, and who has signed off.
- **(c)** Exact structural copy of `sales_order_approver`/`sales_order_approval`.

### `quote`
- **(a)** No existing table models a formal, approvable, customer-facing offer document with lineage to a source Estimate and to Sales Orders it becomes.
- **(b)** The quote header: identity, classification, billing/shipping snapshot, terms, money totals, approval status, `quote_estimate_id` lineage.
- **(c)** New master; FKs to `customer`, `estimate` (nullable), `employee`, `lkp_record_type`(=QUOT), `lkp_record_status`, `lkp_payment_terms`, `lkp_price_level`, `lkp_currency`, `lkp_state`, `lkp_country`.

### `quote_item`
- **(a)** Line items are a distinct 1-to-many child; no existing table fits.
- **(b)** Quoted lines with item snapshots + stored per-line money, optionally tracing back to the Estimate line it was converted from.
- **(c)** New child of `quote`; FKs to `inventory_item` (nullable), `lkp_unit`, `lkp_tax_rate`, `estimate_item` (nullable lineage).

### `quote_history`, `quote_approver`, `quote_approval`
- Same shape and justification as the Estimate-side equivalents, keyed to `QUOT`.

### `quote_conversion`
- **(a)** No existing table can represent "which Sales Order(s) did this Quote become" — and none can be retrofitted onto `sales_order` without a forbidden `ALTER TABLE ADD COLUMN`.
- **(b)** One row per Quote→Sales Order conversion event: which Quote, which resulting Sales Order, when, by whom, and a lightweight line-mapping snapshot.
- **(c)** New table; FKs to `quote`, `sales_order`, `employee`.

---

## 4. ER Diagram (text)

```
   lkp_currency      customer        employee        lkp_record_type/status
        │(1)            │(1)            │(1)                  │(1)
        │       ┌───────┼────────────────┼──────────────────────┤
        ▼(N)    ▼(N)                                            ▼
 ┌───────────────────────────────────────────────────────────────────┐
 │                     estimate  (header, NEW)                       │
 │  PK estimate_id · UUID estimate_uuid · "ESTM-000001"              │
 │  record_type=ESTM · estimate_status · approval_status             │
 │  billing/shipping snapshot · money totals                         │
 └───────────┬─────────────────────────────┬───────────────┬─────────┘
             │(1)                          │(1)            │(1)
     (N)     ▼                      (N)    ▼        (N)    ▼
 ┌─────────────────────┐   ┌──────────────────────┐  ┌──────────────────────┐
 │  estimate_item (NEW) │   │ estimate_history(NEW)│  │ estimate_approver/    │
 │  snapshot · line $   │   └──────────────────────┘  │ estimate_approval(NEW)│
 └──────────┬────────────┘                            └──────────────────────┘
            │ (0..1 lineage, referenced FROM quote_item)
            │
 ┌──────────▼──────────────────────────────────────────────────────────────┐
 │                        quote  (header, NEW)                              │
 │  PK quote_id · UUID quote_uuid · "QUOT-000001"                           │
 │  FK quote_estimate_id (nullable, lineage) ──────────────────────────────▶│ estimate
 │  record_type=QUOT · quote_status (incl. CONV) · approval_status          │
 │  billing/shipping snapshot · money totals                                │
 └───────────┬───────────────────┬───────────────┬──────────────┬──────────┘
             │(1)                │(1)             │(1)           │(1)
     (N)     ▼            (N)    ▼          (N)    ▼         (N) ▼
 ┌─────────────────────┐ ┌───────────────┐ ┌──────────────┐ ┌─────────────────┐
 │  quote_item (NEW)    │ │quote_history  │ │quote_approver/│ │ quote_conversion │
 │  FK estimate_item_id │ │  (NEW)        │ │quote_approval │ │  (NEW)           │
 │  (lineage, nullable) │ └───────────────┘ │  (NEW)        │ │  FK quote_id     │
 └───────────────────────┘                  └───────────────┘ │  FK sales_order_id (UNIQUE) │
                                                                └────────┬─────────┘
                                                                         │
                                                                         ▼
                                                                    sales_order (EXISTING,
                                                                    untouched schema)
```

**Cardinality summary**
- `customer` 1 ─── N `estimate`, 1 ─── N `quote`.
- `estimate` 1 ─── N `estimate_item`, 1 ─── N `estimate_history`.
- `estimate` 1 ─── N `quote` (via `quote.quote_estimate_id`, nullable — a Quote may be standalone).
- `estimate_item` 0..1 ─── N `quote_item` (lineage).
- `quote` 1 ─── N `quote_item`, 1 ─── N `quote_history`.
- `quote` 1 ─── N `quote_conversion` ─── 1 `sales_order` (each conversion row ties one Quote to exactly one Sales Order; a Quote may have many such rows).

---

## 5. SQL — CREATE TABLE Statements

> Final DDL is appended to `database/migrations/tenant/schema.sql` via the **add-migration** skill, **after** the `sales_order` block (since `quote_conversion` FKs it). Same numeric standards as Sales Order/Invoice: money `DECIMAL(15,2)`, quantity `DECIMAL(14,3)`, percent `DECIMAL(6,4)`, exchange rate `DECIMAL(18,6)`.

### 5.1 `estimate` (header)

```sql
CREATE TABLE IF NOT EXISTS estimate (
    estimate_id                  SERIAL        PRIMARY KEY,
    estimate_uuid                UUID          NOT NULL DEFAULT gen_random_uuid(),
    ss_customer_id                INTEGER          NULL,  -- platform owner stamp, no cross-DB FK (matches customer/sales_order/invoice)
    estimate_number               VARCHAR(20)      NULL,  -- 'ESTM-000001', generated post-insert in Go

    -- Classification
    record_type                   INTEGER       NOT NULL REFERENCES lkp_record_type(record_type_id),   -- = ESTM
    estimate_status                INTEGER       NOT NULL REFERENCES lkp_record_status(record_status_id),

    -- Approval (optional, configuration-driven — AD-8, mirrors sales_order_approval_status)
    estimate_approval_status       VARCHAR(10)   NOT NULL DEFAULT 'none',  -- none | pending | approved
    estimate_approved_by           INTEGER           NULL REFERENCES employee(employee_id),

    -- Primary info
    estimate_customer_id           INTEGER       NOT NULL REFERENCES customer(customer_id),
    estimate_po_number             VARCHAR(50)   NOT NULL DEFAULT '',
    estimate_reference_number      VARCHAR(50)   NOT NULL DEFAULT '',
    estimate_date                  DATE          NOT NULL DEFAULT CURRENT_DATE,
    estimate_valid_until           DATE              NULL,  -- matches v1 workflow field 'valid_until'
    estimate_sales_tax_percent     DECIMAL(6,4)  NOT NULL DEFAULT 0,
    estimate_memo                  TEXT          NOT NULL DEFAULT '',
    estimate_notes                 TEXT          NOT NULL DEFAULT '',
    estimate_internal_notes        TEXT          NOT NULL DEFAULT '',
    estimate_terms_conditions      TEXT          NOT NULL DEFAULT '',

    -- Sales assignment
    estimate_sales_rep_id          INTEGER           NULL REFERENCES employee(employee_id),
    estimate_owner_id              INTEGER           NULL REFERENCES employee(employee_id),

    -- Terms / pricing / currency
    estimate_payment_terms         INTEGER           NULL REFERENCES lkp_payment_terms(payment_terms_id),
    estimate_price_level           INTEGER           NULL REFERENCES lkp_price_level(price_level_id),
    estimate_currency              INTEGER           NULL REFERENCES lkp_currency(currency_id),
    estimate_exchange_rate         DECIMAL(18,6) NOT NULL DEFAULT 1,

    -- Money summary (stored)
    estimate_subtotal              DECIMAL(15,2) NOT NULL DEFAULT 0,
    estimate_discount_total        DECIMAL(15,2) NOT NULL DEFAULT 0,
    estimate_tax_total             DECIMAL(15,2) NOT NULL DEFAULT 0,
    estimate_shipping_charge       DECIMAL(15,2) NOT NULL DEFAULT 0,
    estimate_adjustment            DECIMAL(15,2) NOT NULL DEFAULT 0,
    estimate_grand_total           DECIMAL(15,2) NOT NULL DEFAULT 0,

    -- Billing snapshot (copied from customer)
    estimate_bill_customer_name    VARCHAR(150) NOT NULL DEFAULT '',
    estimate_bill_attention        VARCHAR(150) NOT NULL DEFAULT '',
    estimate_bill_addr_line1       VARCHAR(100) NOT NULL DEFAULT '',
    estimate_bill_addr_line2       VARCHAR(100) NOT NULL DEFAULT '',
    estimate_bill_addr_suitenum    VARCHAR(20)  NOT NULL DEFAULT '',
    estimate_bill_addr_city        VARCHAR(100) NOT NULL DEFAULT '',
    estimate_bill_addr_state       INTEGER          NULL REFERENCES lkp_state(state_id),
    estimate_bill_addr_zip         VARCHAR(10)  NOT NULL DEFAULT '',
    estimate_bill_addr_country     INTEGER          NULL REFERENCES lkp_country(country_id),
    estimate_bill_phone            VARCHAR(20)  NOT NULL DEFAULT '',
    estimate_bill_fax              VARCHAR(20)  NOT NULL DEFAULT '',
    estimate_bill_email            VARCHAR(100) NOT NULL DEFAULT '',

    -- Shipping snapshot
    estimate_ship_same_as_bill     BOOLEAN      NOT NULL DEFAULT FALSE,
    estimate_ship_customer_name    VARCHAR(150) NOT NULL DEFAULT '',
    estimate_ship_attention        VARCHAR(150) NOT NULL DEFAULT '',
    estimate_ship_addr_line1       VARCHAR(100) NOT NULL DEFAULT '',
    estimate_ship_addr_line2       VARCHAR(100) NOT NULL DEFAULT '',
    estimate_ship_addr_suitenum    VARCHAR(20)  NOT NULL DEFAULT '',
    estimate_ship_addr_city        VARCHAR(100) NOT NULL DEFAULT '',
    estimate_ship_addr_state       INTEGER          NULL REFERENCES lkp_state(state_id),
    estimate_ship_addr_zip         VARCHAR(10)  NOT NULL DEFAULT '',
    estimate_ship_addr_country     INTEGER          NULL REFERENCES lkp_country(country_id),
    estimate_ship_phone            VARCHAR(20)  NOT NULL DEFAULT '',
    estimate_ship_fax              VARCHAR(20)  NOT NULL DEFAULT '',
    estimate_ship_email            VARCHAR(100) NOT NULL DEFAULT '',

    -- Dynamic + audit
    estimate_custom_fields         JSONB        NOT NULL DEFAULT '{}',
    estimate_created_at            TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    estimate_created_by            INTEGER          NULL REFERENCES employee(employee_id),
    estimate_updated_at            TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    estimate_updated_by            INTEGER          NULL REFERENCES employee(employee_id),
    estimate_deleted_at            TIMESTAMP        NULL,
    estimate_deleted_by            INTEGER          NULL REFERENCES employee(employee_id),
    estimate_record_version        INTEGER      NOT NULL DEFAULT 1,

    CONSTRAINT uq_estimate_uuid       UNIQUE (estimate_uuid),
    CONSTRAINT uq_estimate_number     UNIQUE (estimate_number),
    CONSTRAINT chk_est_approval_status CHECK (estimate_approval_status IN ('none','pending','approved')),
    CONSTRAINT chk_est_tax_percent    CHECK (estimate_sales_tax_percent >= 0 AND estimate_sales_tax_percent <= 100),
    CONSTRAINT chk_est_totals_nonneg  CHECK (estimate_subtotal >= 0 AND estimate_grand_total >= 0),
    CONSTRAINT chk_est_soft_delete    CHECK (
        (estimate_deleted_at IS NULL AND estimate_deleted_by IS NULL) OR
        (estimate_deleted_at IS NOT NULL AND estimate_deleted_by IS NOT NULL)
    )
);
```

### 5.2 `estimate_item` (line items)

```sql
CREATE TABLE IF NOT EXISTS estimate_item (
    estimate_item_id          SERIAL        PRIMARY KEY,
    estimate_item_uuid        UUID          NOT NULL DEFAULT gen_random_uuid(),
    estimate_id                INTEGER       NOT NULL REFERENCES estimate(estimate_id) ON DELETE CASCADE,
    line_number                 INTEGER      NOT NULL,
    inventory_item_id           INTEGER          NULL REFERENCES inventory_item(inventory_item_id),   -- NULL = free-text line

    -- Snapshots (frozen at add time — never re-read from catalog)
    item_name                   VARCHAR(150)  NOT NULL DEFAULT '',
    sku                          VARCHAR(50)   NOT NULL DEFAULT '',
    description                  TEXT          NOT NULL DEFAULT '',
    unit_id                      INTEGER          NULL REFERENCES lkp_unit(unit_id),
    unit_code                    VARCHAR(10)   NOT NULL DEFAULT '',
    quantity                     DECIMAL(14,3) NOT NULL DEFAULT 0,
    unit_price                   DECIMAL(15,2) NOT NULL DEFAULT 0,
    discount_percent             DECIMAL(6,4)  NOT NULL DEFAULT 0,
    tax_rate_id                   INTEGER          NULL REFERENCES lkp_tax_rate(tax_rate_id),
    tax_percent                   DECIMAL(6,4)  NOT NULL DEFAULT 0,

    -- Stored line money
    line_subtotal                 DECIMAL(15,2) NOT NULL DEFAULT 0,
    line_discount                  DECIMAL(15,2) NOT NULL DEFAULT 0,
    line_tax                       DECIMAL(15,2) NOT NULL DEFAULT 0,
    line_total                      DECIMAL(15,2) NOT NULL DEFAULT 0,

    item_created_at                 TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    item_created_by                 INTEGER           NULL REFERENCES employee(employee_id),
    item_updated_at                 TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    item_deleted_at                  TIMESTAMP        NULL,
    item_record_version              INTEGER       NOT NULL DEFAULT 1,

    CONSTRAINT uq_estimate_item_uuid UNIQUE (estimate_item_uuid),
    CONSTRAINT chk_esti_qty          CHECK (quantity >= 0),
    CONSTRAINT chk_esti_unit_price   CHECK (unit_price >= 0),
    CONSTRAINT chk_esti_discount     CHECK (discount_percent >= 0 AND discount_percent <= 100),
    CONSTRAINT chk_esti_tax          CHECK (tax_percent >= 0 AND tax_percent <= 100)
);
```

### 5.3 `estimate_history`

```sql
CREATE TABLE IF NOT EXISTS estimate_history (
    estimate_history_id       SERIAL       PRIMARY KEY,
    estimate_id                 INTEGER      NOT NULL REFERENCES estimate(estimate_id) ON DELETE CASCADE,
    from_status_id               INTEGER          NULL REFERENCES lkp_record_status(record_status_id),
    to_status_id                  INTEGER          NULL REFERENCES lkp_record_status(record_status_id),
    action                        VARCHAR(32)  NOT NULL DEFAULT 'transition', -- create | transition | convert | update | approve
    actor_employee_id              INTEGER          NULL REFERENCES employee(employee_id),
    snapshot                       JSONB        NOT NULL DEFAULT '{}',
    at                             TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

### 5.4 `estimate_approver` / `estimate_approval` (AD-8, mirrors `sales_order_approver`/`_approval`)

```sql
CREATE TABLE IF NOT EXISTS estimate_approver (
    estimate_approver_id    SERIAL      PRIMARY KEY,
    record_type_id          INTEGER     NOT NULL REFERENCES lkp_record_type(record_type_id),      -- = ESTM
    record_status_id        INTEGER     NOT NULL REFERENCES lkp_record_status(record_status_id),  -- e.g. PAPV
    approver_employee_id    INTEGER     NOT NULL REFERENCES employee(employee_id),
    is_active                BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at                TIMESTAMP   NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_by                INTEGER         NULL REFERENCES employee(employee_id),
    CONSTRAINT uq_estimate_approver UNIQUE (record_type_id, record_status_id, approver_employee_id)
);

CREATE TABLE IF NOT EXISTS estimate_approval (
    estimate_approval_id    SERIAL      PRIMARY KEY,
    estimate_id              INTEGER     NOT NULL REFERENCES estimate(estimate_id) ON DELETE CASCADE,
    record_status_id         INTEGER     NOT NULL REFERENCES lkp_record_status(record_status_id),  -- status the sign-off was for
    approver_employee_id     INTEGER     NOT NULL REFERENCES employee(employee_id),
    approved_at               TIMESTAMP   NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT uq_estimate_approval UNIQUE (estimate_id, record_status_id, approver_employee_id)
);
```

### 5.5 `quote` (header)

```sql
CREATE TABLE IF NOT EXISTS quote (
    quote_id                     SERIAL        PRIMARY KEY,
    quote_uuid                   UUID          NOT NULL DEFAULT gen_random_uuid(),
    ss_customer_id                 INTEGER          NULL,  -- platform owner stamp, no cross-DB FK
    quote_number                   VARCHAR(20)      NULL,  -- 'QUOT-000001', generated post-insert in Go

    -- Classification
    record_type                    INTEGER       NOT NULL REFERENCES lkp_record_type(record_type_id),   -- = QUOT
    quote_status                    INTEGER       NOT NULL REFERENCES lkp_record_status(record_status_id),

    -- Approval (optional, configuration-driven — AD-8)
    quote_approval_status           VARCHAR(10)   NOT NULL DEFAULT 'none',  -- none | pending | approved
    quote_approved_by               INTEGER           NULL REFERENCES employee(employee_id),

    -- Lineage (AD-5): source Estimate, if any. Nullable — a Quote may be created standalone.
    quote_estimate_id                INTEGER          NULL REFERENCES estimate(estimate_id),

    -- Primary info
    quote_customer_id                INTEGER       NOT NULL REFERENCES customer(customer_id),
    quote_po_number                  VARCHAR(50)   NOT NULL DEFAULT '',
    quote_reference_number           VARCHAR(50)   NOT NULL DEFAULT '',
    quote_date                       DATE          NOT NULL DEFAULT CURRENT_DATE,
    quote_valid_until                DATE              NULL,
    quote_sales_tax_percent          DECIMAL(6,4)  NOT NULL DEFAULT 0,
    quote_memo                       TEXT          NOT NULL DEFAULT '',
    quote_notes                      TEXT          NOT NULL DEFAULT '',
    quote_internal_notes             TEXT          NOT NULL DEFAULT '',
    quote_terms_conditions           TEXT          NOT NULL DEFAULT '',

    -- Sales assignment
    quote_sales_rep_id               INTEGER           NULL REFERENCES employee(employee_id),
    quote_owner_id                   INTEGER           NULL REFERENCES employee(employee_id),

    -- Terms / pricing / currency
    quote_payment_terms              INTEGER           NULL REFERENCES lkp_payment_terms(payment_terms_id),
    quote_price_level                INTEGER           NULL REFERENCES lkp_price_level(price_level_id),
    quote_currency                   INTEGER           NULL REFERENCES lkp_currency(currency_id),
    quote_exchange_rate              DECIMAL(18,6) NOT NULL DEFAULT 1,

    -- Money summary (stored)
    quote_subtotal                   DECIMAL(15,2) NOT NULL DEFAULT 0,
    quote_discount_total             DECIMAL(15,2) NOT NULL DEFAULT 0,
    quote_tax_total                  DECIMAL(15,2) NOT NULL DEFAULT 0,
    quote_shipping_charge            DECIMAL(15,2) NOT NULL DEFAULT 0,
    quote_adjustment                 DECIMAL(15,2) NOT NULL DEFAULT 0,
    quote_grand_total                DECIMAL(15,2) NOT NULL DEFAULT 0,

    -- Billing snapshot
    quote_bill_customer_name         VARCHAR(150) NOT NULL DEFAULT '',
    quote_bill_attention             VARCHAR(150) NOT NULL DEFAULT '',
    quote_bill_addr_line1            VARCHAR(100) NOT NULL DEFAULT '',
    quote_bill_addr_line2            VARCHAR(100) NOT NULL DEFAULT '',
    quote_bill_addr_suitenum         VARCHAR(20)  NOT NULL DEFAULT '',
    quote_bill_addr_city             VARCHAR(100) NOT NULL DEFAULT '',
    quote_bill_addr_state            INTEGER          NULL REFERENCES lkp_state(state_id),
    quote_bill_addr_zip              VARCHAR(10)  NOT NULL DEFAULT '',
    quote_bill_addr_country          INTEGER          NULL REFERENCES lkp_country(country_id),
    quote_bill_phone                 VARCHAR(20)  NOT NULL DEFAULT '',
    quote_bill_fax                   VARCHAR(20)  NOT NULL DEFAULT '',
    quote_bill_email                 VARCHAR(100) NOT NULL DEFAULT '',

    -- Shipping snapshot
    quote_ship_same_as_bill          BOOLEAN      NOT NULL DEFAULT FALSE,
    quote_ship_customer_name         VARCHAR(150) NOT NULL DEFAULT '',
    quote_ship_attention             VARCHAR(150) NOT NULL DEFAULT '',
    quote_ship_addr_line1            VARCHAR(100) NOT NULL DEFAULT '',
    quote_ship_addr_line2            VARCHAR(100) NOT NULL DEFAULT '',
    quote_ship_addr_suitenum         VARCHAR(20)  NOT NULL DEFAULT '',
    quote_ship_addr_city             VARCHAR(100) NOT NULL DEFAULT '',
    quote_ship_addr_state            INTEGER          NULL REFERENCES lkp_state(state_id),
    quote_ship_addr_zip              VARCHAR(10)  NOT NULL DEFAULT '',
    quote_ship_addr_country          INTEGER          NULL REFERENCES lkp_country(country_id),
    quote_ship_phone                 VARCHAR(20)  NOT NULL DEFAULT '',
    quote_ship_fax                   VARCHAR(20)  NOT NULL DEFAULT '',
    quote_ship_email                 VARCHAR(100) NOT NULL DEFAULT '',

    -- Dynamic + audit
    quote_custom_fields               JSONB        NOT NULL DEFAULT '{}',
    quote_created_at                  TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    quote_created_by                  INTEGER          NULL REFERENCES employee(employee_id),
    quote_updated_at                  TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    quote_updated_by                  INTEGER          NULL REFERENCES employee(employee_id),
    quote_deleted_at                  TIMESTAMP        NULL,
    quote_deleted_by                  INTEGER          NULL REFERENCES employee(employee_id),
    quote_record_version              INTEGER      NOT NULL DEFAULT 1,

    CONSTRAINT uq_quote_uuid       UNIQUE (quote_uuid),
    CONSTRAINT uq_quote_number     UNIQUE (quote_number),
    CONSTRAINT chk_quo_approval_status CHECK (quote_approval_status IN ('none','pending','approved')),
    CONSTRAINT chk_quo_tax_percent    CHECK (quote_sales_tax_percent >= 0 AND quote_sales_tax_percent <= 100),
    CONSTRAINT chk_quo_totals_nonneg  CHECK (quote_subtotal >= 0 AND quote_grand_total >= 0),
    CONSTRAINT chk_quo_soft_delete    CHECK (
        (quote_deleted_at IS NULL AND quote_deleted_by IS NULL) OR
        (quote_deleted_at IS NOT NULL AND quote_deleted_by IS NOT NULL)
    )
);
```

### 5.6 `quote_item` (line items)

```sql
CREATE TABLE IF NOT EXISTS quote_item (
    quote_item_id              SERIAL        PRIMARY KEY,
    quote_item_uuid             UUID          NOT NULL DEFAULT gen_random_uuid(),
    quote_id                     INTEGER       NOT NULL REFERENCES quote(quote_id) ON DELETE CASCADE,
    line_number                   INTEGER      NOT NULL,
    inventory_item_id             INTEGER          NULL REFERENCES inventory_item(inventory_item_id),   -- NULL = free-text line
    estimate_item_id               INTEGER          NULL REFERENCES estimate_item(estimate_item_id),     -- lineage from Estimate conversion

    -- Snapshots (frozen at add/conversion time — never re-read from catalog)
    item_name                      VARCHAR(150)  NOT NULL DEFAULT '',
    sku                              VARCHAR(50)   NOT NULL DEFAULT '',
    description                      TEXT          NOT NULL DEFAULT '',
    unit_id                           INTEGER          NULL REFERENCES lkp_unit(unit_id),
    unit_code                         VARCHAR(10)   NOT NULL DEFAULT '',
    quantity                          DECIMAL(14,3) NOT NULL DEFAULT 0,
    unit_price                        DECIMAL(15,2) NOT NULL DEFAULT 0,
    discount_percent                  DECIMAL(6,4)  NOT NULL DEFAULT 0,
    tax_rate_id                        INTEGER          NULL REFERENCES lkp_tax_rate(tax_rate_id),
    tax_percent                        DECIMAL(6,4)  NOT NULL DEFAULT 0,

    -- Stored line money
    line_subtotal                      DECIMAL(15,2) NOT NULL DEFAULT 0,
    line_discount                       DECIMAL(15,2) NOT NULL DEFAULT 0,
    line_tax                             DECIMAL(15,2) NOT NULL DEFAULT 0,
    line_total                            DECIMAL(15,2) NOT NULL DEFAULT 0,

    item_created_at                       TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    item_created_by                       INTEGER           NULL REFERENCES employee(employee_id),
    item_updated_at                       TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    item_deleted_at                        TIMESTAMP        NULL,
    item_record_version                    INTEGER       NOT NULL DEFAULT 1,

    CONSTRAINT uq_quote_item_uuid UNIQUE (quote_item_uuid),
    CONSTRAINT chk_qi_qty         CHECK (quantity >= 0),
    CONSTRAINT chk_qi_unit_price  CHECK (unit_price >= 0),
    CONSTRAINT chk_qi_discount    CHECK (discount_percent >= 0 AND discount_percent <= 100),
    CONSTRAINT chk_qi_tax         CHECK (tax_percent >= 0 AND tax_percent <= 100)
);
```

### 5.7 `quote_history`

```sql
CREATE TABLE IF NOT EXISTS quote_history (
    quote_history_id         SERIAL       PRIMARY KEY,
    quote_id                   INTEGER      NOT NULL REFERENCES quote(quote_id) ON DELETE CASCADE,
    from_status_id               INTEGER          NULL REFERENCES lkp_record_status(record_status_id),
    to_status_id                  INTEGER          NULL REFERENCES lkp_record_status(record_status_id),
    action                         VARCHAR(32)  NOT NULL DEFAULT 'transition', -- create | transition | convert | update | approve
    actor_employee_id               INTEGER          NULL REFERENCES employee(employee_id),
    snapshot                         JSONB        NOT NULL DEFAULT '{}',
    at                                TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

### 5.8 `quote_approver` / `quote_approval` (AD-8)

```sql
CREATE TABLE IF NOT EXISTS quote_approver (
    quote_approver_id       SERIAL      PRIMARY KEY,
    record_type_id           INTEGER     NOT NULL REFERENCES lkp_record_type(record_type_id),      -- = QUOT
    record_status_id         INTEGER     NOT NULL REFERENCES lkp_record_status(record_status_id),  -- e.g. PAPV
    approver_employee_id     INTEGER     NOT NULL REFERENCES employee(employee_id),
    is_active                 BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at                 TIMESTAMP   NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_by                 INTEGER         NULL REFERENCES employee(employee_id),
    CONSTRAINT uq_quote_approver UNIQUE (record_type_id, record_status_id, approver_employee_id)
);

CREATE TABLE IF NOT EXISTS quote_approval (
    quote_approval_id       SERIAL      PRIMARY KEY,
    quote_id                  INTEGER     NOT NULL REFERENCES quote(quote_id) ON DELETE CASCADE,
    record_status_id          INTEGER     NOT NULL REFERENCES lkp_record_status(record_status_id),
    approver_employee_id      INTEGER     NOT NULL REFERENCES employee(employee_id),
    approved_at                 TIMESTAMP   NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT uq_quote_approval UNIQUE (quote_id, record_status_id, approver_employee_id)
);
```

### 5.9 `quote_conversion` (AD-6 — Quote → Sales Order lineage)

```sql
CREATE TABLE IF NOT EXISTS quote_conversion (
    quote_conversion_id      SERIAL       PRIMARY KEY,
    quote_id                   INTEGER      NOT NULL REFERENCES quote(quote_id) ON DELETE CASCADE,
    sales_order_id              INTEGER      NOT NULL REFERENCES sales_order(sales_order_id) ON DELETE CASCADE,
    converted_at                 TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    converted_by                  INTEGER          NULL REFERENCES employee(employee_id),
    snapshot                       JSONB        NOT NULL DEFAULT '{}',  -- lightweight {quoteItemId: salesOrderItemId} line mapping for audit

    CONSTRAINT uq_quote_conversion_sales_order UNIQUE (sales_order_id)
);
```

### 5.10 Indexes

```sql
-- estimate (listing/filtering — all partial on live rows)
CREATE INDEX IF NOT EXISTS idx_est_customer      ON estimate (estimate_customer_id)  WHERE estimate_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_est_status        ON estimate (estimate_status)       WHERE estimate_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_est_date          ON estimate (estimate_date)         WHERE estimate_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_est_sales_rep     ON estimate (estimate_sales_rep_id) WHERE estimate_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_est_owner         ON estimate (estimate_owner_id)     WHERE estimate_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_est_created_id    ON estimate (estimate_created_at, estimate_id)     WHERE estimate_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_est_updated_id    ON estimate (estimate_updated_at, estimate_id)     WHERE estimate_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_est_validuntil_id ON estimate (estimate_valid_until, estimate_id)    WHERE estimate_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_est_grandtotal_id ON estimate (estimate_grand_total, estimate_id)    WHERE estimate_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_est_custom_gin    ON estimate USING GIN (estimate_custom_fields);

CREATE INDEX IF NOT EXISTS idx_esti_estimate ON estimate_item (estimate_id) WHERE item_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_esti_item     ON estimate_item (inventory_item_id);
CREATE UNIQUE INDEX IF NOT EXISTS uq_esti_line_active
    ON estimate_item (estimate_id, line_number) WHERE item_deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_est_history_estimate ON estimate_history (estimate_id);

CREATE INDEX IF NOT EXISTS idx_estimate_approver_lookup
    ON estimate_approver (record_type_id, record_status_id) WHERE is_active;
CREATE INDEX IF NOT EXISTS idx_estimate_approval_estimate ON estimate_approval (estimate_id);

-- quote (listing/filtering — all partial on live rows)
CREATE INDEX IF NOT EXISTS idx_quo_customer      ON quote (quote_customer_id)  WHERE quote_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_quo_estimate       ON quote (quote_estimate_id)  WHERE quote_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_quo_status         ON quote (quote_status)       WHERE quote_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_quo_date           ON quote (quote_date)         WHERE quote_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_quo_sales_rep      ON quote (quote_sales_rep_id) WHERE quote_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_quo_owner          ON quote (quote_owner_id)     WHERE quote_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_quo_created_id     ON quote (quote_created_at, quote_id)     WHERE quote_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_quo_updated_id     ON quote (quote_updated_at, quote_id)     WHERE quote_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_quo_validuntil_id  ON quote (quote_valid_until, quote_id)    WHERE quote_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_quo_grandtotal_id  ON quote (quote_grand_total, quote_id)    WHERE quote_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_quo_custom_gin     ON quote USING GIN (quote_custom_fields);

CREATE INDEX IF NOT EXISTS idx_qi_quote     ON quote_item (quote_id)        WHERE item_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_qi_item      ON quote_item (inventory_item_id);
CREATE INDEX IF NOT EXISTS idx_qi_est_item  ON quote_item (estimate_item_id);
CREATE UNIQUE INDEX IF NOT EXISTS uq_qi_line_active
    ON quote_item (quote_id, line_number) WHERE item_deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_quo_history_quote ON quote_history (quote_id);

CREATE INDEX IF NOT EXISTS idx_quote_approver_lookup
    ON quote_approver (record_type_id, record_status_id) WHERE is_active;
CREATE INDEX IF NOT EXISTS idx_quote_approval_quote ON quote_approval (quote_id);

CREATE INDEX IF NOT EXISTS idx_quote_conversion_quote ON quote_conversion (quote_id);
```

> **Migration ordering:** append after the Sales Order block (`quote_conversion` FKs `sales_order`). `estimate`/`estimate_item` have no forward dependency and could technically go earlier, but keeping the whole Estimate+Quote block together (after Sales Order) keeps the migration readable as one unit.

---

## 6. Foreign Key Relationships (explained)

| Child column | → Parent | Meaning | On delete |
|---|---|---|---|
| `estimate.estimate_customer_id` | `customer.customer_id` | Estimate recipient | RESTRICT |
| `estimate.record_type` / `estimate_status` | `lkp_record_type` / `lkp_record_status` | Classification / lifecycle | RESTRICT |
| `estimate.estimate_sales_rep_id` / `_owner_id` / `_approved_by` | `employee.employee_id` | Assignment / approval | RESTRICT |
| `estimate_item.estimate_id` | `estimate.estimate_id` | Owning estimate | **CASCADE** |
| `estimate_item.inventory_item_id` | `inventory_item.inventory_item_id` | Catalog item (nullable) | RESTRICT |
| `estimate_history.estimate_id` | `estimate.estimate_id` | Owning estimate | **CASCADE** |
| `estimate_approver.record_status_id` | `lkp_record_status.record_status_id` | Gate config | RESTRICT |
| `estimate_approval.estimate_id` | `estimate.estimate_id` | Sign-off target | **CASCADE** |
| `quote.quote_customer_id` | `customer.customer_id` | Quote recipient | RESTRICT |
| `quote.quote_estimate_id` | `estimate.estimate_id` | Optional conversion source | RESTRICT (nullable) |
| `quote.record_type` / `quote_status` | `lkp_record_type` / `lkp_record_status` | Classification / lifecycle | RESTRICT |
| `quote_item.quote_id` | `quote.quote_id` | Owning quote | **CASCADE** |
| `quote_item.estimate_item_id` | `estimate_item.estimate_item_id` | Conversion lineage (nullable) | RESTRICT |
| `quote_history.quote_id` | `quote.quote_id` | Owning quote | **CASCADE** |
| `quote_approval.quote_id` | `quote.quote_id` | Sign-off target | **CASCADE** |
| `quote_conversion.quote_id` | `quote.quote_id` | Source quote | **CASCADE** |
| `quote_conversion.sales_order_id` | `sales_order.sales_order_id` | Resulting sales order (unique) | **CASCADE** |

No cross-database FKs. No `tenant_id` anywhere. **`sales_order` and `sales_order_item` schemas are untouched by this migration** — the only new FK pointing at them is `quote_conversion.sales_order_id`, on a brand-new table.

---

## 7. Status Transition Rules (service layer)

**Estimate** (`lkp_record_status`, `record_type=4`: `DRFT, PAPV, APPV, SENT, CANC, RJCT, EXPR`):

```go
var allowedEstimateTransitions = map[string]map[string]bool{
    "DRFT": {"PAPV": true, "CANC": true},
    "PAPV": {"APPV": true, "DRFT": true, "CANC": true},
    "APPV": {"SENT": true, "CANC": true},
    "SENT": {"RJCT": true, "EXPR": true, "CANC": true},
    "RJCT": {},  // terminal
    "EXPR": {},  // terminal
    "CANC": {},  // terminal
}
```

There is no explicit "Accepted" status for Estimate — acceptance is expressed by converting the Estimate into one or more Quotes (§9.1); a Sent Estimate can remain in `SENT` indefinitely while that happens.

**Quote** (`record_type=5`: `DRFT, PAPV, APPV, SENT, CANC, RJCT, EXPR, CONV`):

```go
var allowedQuoteTransitions = map[string]map[string]bool{
    "DRFT": {"PAPV": true, "CANC": true},
    "PAPV": {"APPV": true, "DRFT": true, "CANC": true},
    "APPV": {"SENT": true, "CANC": true},
    "SENT": {"RJCT": true, "EXPR": true, "CANC": true},
    "RJCT": {},  // terminal
    "EXPR": {},  // terminal
    "CANC": {},  // terminal
    // CONV is intentionally absent as a map value anywhere — it is never a
    // manually-requested transition target. It is set only by the Quote→Sales
    // Order conversion service (§9.2), in the same transaction as the
    // quote_conversion insert. A quote already in CONV keeps whatever map
    // entry its current status had before conversion, since conversion does
    // not remove or replace the row's transition eligibility for further
    // conversions (checked independently in the conversion service, not
    // through this map).
}
```

`POST .../{uuid}/transition` rejects any request whose target is `CONV` with 400 (`ClientError`, not a valid manual target) — distinct from 409 (a real but currently-illegal transition), since `CONV` is never a legal *manual* target at all.

**Approval** (both modules) — identical to `salesorder/approval.go`: entering `PAPV` sets `{x}_approval_status='pending'` if approvers are configured for that `(record_type, record_status)` pair; leaving `PAPV` is blocked (`ErrApprovalRequired` → 409) until sign-off count meets the active configured-approver count; `Approve()` locks the row `FOR UPDATE`, records an idempotent (`ON CONFLICT DO NOTHING`) sign-off row, and flips `{x}_approval_status='approved'` once met. Zero configured approvers at a status = no gate (same as Sales Order).

---

## 8. Money & Snapshot-Pricing Rules

**Per line (`estimate_item`/`quote_item`, stored) — identical formula to `sales_order_item`/`invoice_item`:**
```
line_subtotal = round(quantity * unit_price, 2)
line_discount = round(line_subtotal * discount_percent / 100, 2)
line_tax      = round((line_subtotal - line_discount) * tax_percent / 100, 2)
line_total    = line_subtotal - line_discount + line_tax
```

**Per header (`estimate`/`quote`, stored, recomputed on every line mutation inside the same transaction):**
```
subtotal       = Σ line_subtotal
discount_total = Σ line_discount
tax_total      = Σ line_tax
grand_total    = subtotal - discount_total + tax_total + shipping_charge + adjustment
```

Neither document tracks a payment balance (`amount_paid`/`balance_due`) — that concept belongs to Invoice, not to a pre-sales document.

**Snapshot pricing (Estimate → Quote conversion):** every `quote_item` created by conversion copies `estimate_item`'s already-frozen snapshot columns (`item_name, sku, description, unit_id, unit_code, quantity, unit_price, discount_percent, tax_rate_id, tax_percent`) verbatim — it does **not** re-read `inventory_item`. The quote then recomputes its own `line_subtotal/discount/tax/total` from those copied inputs (a defensive recompute, not a re-price — matches Invoice §8's rationale, since header-level `shipping_charge`/`adjustment` may differ between the Estimate and the Quote).

**Snapshot pricing (Quote → Sales Order conversion):** identical mechanism — `sales_order_item` rows copy `quote_item`'s frozen snapshot columns verbatim; `sales_order`/`sales_order_item` money is recomputed from those copied inputs via the existing `salesorder/calc.go` functions (already-existing code, unchanged by this design).

---

## 9. Conversions

### 9.1 Estimate → Quote

**Endpoint:** `POST /api/tenant/estimates/{uuid}/convert-to-quote`

**Preconditions (checked in the service, under a DB transaction):**
1. Caller has `estimate:read` + scope on the Estimate, and `quote:create`.
2. Estimate status is `APPV` or `SENT` (409 otherwise — an unapproved or already-terminal estimate has nothing quotable).
3. Estimate has at least one non-deleted line (409 otherwise).

**Transaction body:**
1. Re-fetch the Estimate header + lines **inside the transaction** (`SELECT ... FOR UPDATE` on the estimate row) to avoid converting a stale read.
2. Insert `quote` row: `quote_customer_id` = Estimate's customer, `quote_estimate_id` = Estimate id, billing/shipping/terms snapshot copied verbatim from the Estimate's own snapshot columns (not re-read from `customer`), `quote_status` = `DRFT`, `quote_date` = today.
3. Insert one `quote_item` per Estimate line: copy all snapshot columns + `estimate_item_id` lineage FK; recompute line money via §8.
4. Compute + store header totals via §8.
5. Generate `quote_number` post-insert (`QUOT-%06d`).
6. Insert `quote_history` row: `action='convert'`, `to_status_id=DRFT`, `snapshot={"source_estimate_id": ..., "source_estimate_number": ...}`.
7. Commit. Return the created quote (with items) — same envelope shape as `POST /api/tenant/quotes`.

**Idempotency note:** repeated calls create additional Quotes (e.g. quoting different option sets from one Estimate) — this is **not** idempotent by design, matching the SO→Invoice precedent. The Estimate's status is never mutated by this endpoint.

### 9.2 Quote → Sales Order

**Endpoint:** `POST /api/tenant/quotes/{uuid}/convert-to-sales-order`

**Preconditions:**
1. Caller has `quote:read` + scope on the Quote, and `sales_order:create`.
2. Quote status is `APPV`, `SENT`, or `CONV` (409 otherwise — allows repeat conversions once already converted once).
3. Quote has at least one non-deleted line (409 otherwise).

**Transaction body:**
1. Re-fetch the Quote header + lines `FOR UPDATE`.
2. Insert `sales_order` row using the existing `salesorder.Create`-shaped logic: `sales_order_customer_id` = Quote's customer, billing/shipping/terms snapshot copied verbatim from the Quote's own snapshot columns, `sales_order_status` = `DRFT`.
3. Insert one `sales_order_item` per Quote line: copy snapshot columns (no lineage FK back to `quote_item` — see the trade-off note below); recompute line money via the existing `salesorder/calc.go`.
4. Compute + store header totals via the existing Sales Order logic.
5. Generate `sales_order_number` post-insert (existing `salesorder/numbering.go`, unchanged).
6. Insert one `quote_conversion` row: `quote_id`, `sales_order_id`, `converted_by` = actor, `snapshot` = `{"lineMapping": [{"quoteItemId": ..., "salesOrderItemId": ...}, ...]}` (captures per-line lineage informally, in lieu of a full relational child table — see AD-6/§3 trade-off).
7. If `quote_status != CONV`, transition it to `CONV` (bypassing the manual transition map, since this is a system-driven change — §7).
8. Insert `quote_history` row: `action='convert'`, `snapshot={"sales_order_id": ..., "sales_order_number": ...}`.
9. Commit. Return the created sales order (with items) — same envelope shape as `POST /api/tenant/sales-orders`.

**Idempotency note:** repeated calls create additional Sales Orders (progress/phased conversion) — not idempotent by design (per your confirmed decision). **`sales_order`/`sales_order_item` schemas are not modified by this step** — the conversion writes only to the pre-existing `salesorder.Create` code path and the new `quote_conversion` table.

---

## 10. API Contracts

All under `/api/tenant/`, through `tenantChain`, RBAC-checked in-handler, IDOR-guarded (404 on scope denial), same envelope shape as Sales Order/Invoice (`{success, message?, ...}`).

| Method & path | Purpose | RBAC |
|---|---|---|
| `GET  /api/tenant/estimates` | Simple in-scope list, cursor-paginated | `estimate:read` + scope |
| `POST /api/tenant/estimates/search` | Full filter + sort + search + pagination | `estimate:read` + scope |
| `GET  /api/tenant/estimates/{uuid}` | Get one (+ items) | `estimate:read` + IDOR |
| `POST /api/tenant/estimates` | Create (header + items) | `estimate:create` |
| `PATCH /api/tenant/estimates/{uuid}` | Update header/items, recompute totals | `estimate:update` + IDOR |
| `DELETE /api/tenant/estimates/{uuid}` | Soft delete | `estimate:delete` + IDOR |
| `POST /api/tenant/estimates/{uuid}/transition` | Status change (validated map, §7) | `estimate:transition` + IDOR |
| `POST /api/tenant/estimates/{uuid}/approve` | Sign off at current `PAPV` status | `estimate:transition` + IDOR |
| `POST /api/tenant/estimates/{uuid}/convert-to-quote` | Estimate → Quote conversion (§9.1) | `estimate:read`+IDOR **and** `quote:create` |
| `GET  /api/tenant/estimates/{uuid}/audit` | Audit / history trail | `estimate:read` + IDOR |
| `GET  /api/tenant/quotes` | Simple in-scope list, cursor-paginated | `quote:read` + scope |
| `POST /api/tenant/quotes/search` | Full filter + sort + search + pagination | `quote:read` + scope |
| `GET  /api/tenant/quotes/{uuid}` | Get one (+ items) | `quote:read` + IDOR |
| `POST /api/tenant/quotes` | Create (header + items, standalone or via convert) | `quote:create` |
| `PATCH /api/tenant/quotes/{uuid}` | Update header/items, recompute totals | `quote:update` + IDOR |
| `DELETE /api/tenant/quotes/{uuid}` | Soft delete | `quote:delete` + IDOR |
| `POST /api/tenant/quotes/{uuid}/transition` | Status change (validated map, §7; `CONV` rejected as a manual target) | `quote:transition` + IDOR |
| `POST /api/tenant/quotes/{uuid}/approve` | Sign off at current `PAPV` status | `quote:transition` + IDOR |
| `POST /api/tenant/quotes/{uuid}/convert-to-sales-order` | Quote → Sales Order conversion (§9.2) | `quote:read`+IDOR **and** `sales_order:create` |
| `GET  /api/tenant/quotes/{uuid}/audit` | Audit / history trail | `quote:read` + IDOR |
| `*/attachments*` | Reuse existing generic attachment routes, `record_id = estimate_uuid`/`quote_uuid` | — |

Confirmed via `controllers/salesorder.go:266-268`: the existing `Approve` handler is gated by `authz.ActionTransition`, not a dedicated `ActionApprove` — so the already-seeded `{ResourceEstimate/ResourceQuote, ActionTransition}` catalog rows are sufficient for both `/transition` and `/approve`. **No `authz/catalog.go` changes are required by this module.**

**Create request** (mirrors the Sales Order/Invoice shape exactly):
```json
POST /api/tenant/estimates
{
  "customerUuid": "9d0f…c2",
  "poNumber": "PO-88213",
  "estimateDate": "2026-07-14",
  "validUntil": "2026-08-13",
  "paymentTermsId": 3,
  "priceLevelId": 2,
  "currencyId": 1,
  "salesRepEmployeeId": 12,
  "ownerEmployeeId": 7,
  "salesTaxPercent": 8.25,
  "memo": "Kitchen remodel — ballpark",
  "shipSameAsBilling": true,
  "shippingCharge": 0,
  "adjustment": 0,
  "customFields": {},
  "items": [
    { "lineNumber": 1, "inventoryItemUuid": "aa11…", "quantity": 25.5,
      "unitPrice": 42.00, "discountPercent": 5, "taxRateId": 4 }
  ]
}
```

**Convert requests/responses**
```json
POST /api/tenant/estimates/{uuid}/convert-to-quote
{}
→ 201 { "success": true, "quote": { "id": "…", "quoteNumber": "QUOT-000003", "status": "Draft",
        "estimate": { "id": "6f2c…9a", "estimateNumber": "ESTM-000012" }, "grandTotal": 1518.14, "items": [...] } }
→ 409 { "success": false, "message": "Estimate must be Approved or later to convert to a quote." }

POST /api/tenant/quotes/{uuid}/convert-to-sales-order
{}
→ 201 { "success": true, "salesOrder": { "id": "…", "salesOrderNumber": "SORD-000051", "status": "Draft",
        "quote": { "id": "9a1b…", "quoteNumber": "QUOT-000003" }, "grandTotal": 1518.14, "items": [...] } }
→ 409 { "success": false, "message": "Quote must be Approved, Sent, or already Converted to convert to a sales order." }
```

---

## 11. Listing & Query Architecture

Reuses `query/` unchanged — no new query engine code. Only new code: `estimateResolver`/`quoteResolver` (`query.FieldResolver` + `SortResolver` + `SearchResolver`) and each module's `Store.Search` (keyset), mirroring `invoice/resolver.go` and `invoice/search.go` exactly.

**FieldResolver whitelist** (table alias `est` = `estimate`, `quo` = `quote`):

| Logical key | SQL expression | DataType | Ops |
|---|---|---|---|
| `id` | `{est\|quo}.{x}_uuid::text` | string | eq |
| `document_number` / `record_number` | `COALESCE({x}.{x}_number,'')` | string | eq, contains, startswith |
| `customer_id` | `{x}.{x}_customer_id::text` | string | eq, in |
| `estimate_id` *(quote only)* | `quo.quote_estimate_id::text` | string | eq, is_null |
| `status` | `{x}.{x}_status::text` | string | eq, in |
| `sales_rep_id` / `owner_id` | `{x}.{x}_sales_rep_id::text` / `_owner_id::text` | string | eq, in, is_null |
| `{x}_date` | `{x}.{x}_date` | date | eq, gt, gte, lt, lte, between |
| `valid_until` | `{x}.{x}_valid_until` | date | gte, lte, between, is_null |
| `currency_id` / `payment_terms_id` / `price_level_id` | respective `{x}.{x}_*` | string | eq, in, is_null |
| `grand_total` | `{x}.{x}_grand_total` | number | eq, gt, gte, lt, lte, between |
| `po_number` | `{x}.{x}_po_number` | string | eq, contains, startswith |
| `created_by` / `updated_by` | `{x}.{x}_created_by::text` / `_updated_by::text` | string | eq, in, is_null |
| `created_at` / `updated_at` | `{x}.{x}_created_at` / `_updated_at` | date | gte, lte, between |
| `cf:<key>` | `{x}.{x}_custom_fields->>'<key>'` | per `workflow_field_definitions` | per type |

**SortResolver whitelist:** `document_number, {x}_date, valid_until, grand_total, status, customer_id, created_at, updated_at` — each `NOT NULL` except `valid_until` (excluded from sort per the same NULL/keyset-cursor rationale documented in `invoice/resolver.go`), paired with `{x}_id` tiebreaker.

**SearchPredicate** (pattern, `{x}` = `estimate`/`quote`):
```sql
(   {x}.{x}_number             ILIKE $n
 OR {x}.{x}_po_number          ILIKE $n
 OR {x}.{x}_memo               ILIKE $n
 OR {x}.{x}_notes              ILIKE $n
 OR {x}.{x}_bill_customer_name ILIKE $n
 OR EXISTS (SELECT 1 FROM {x}_item xi
             WHERE xi.{x}_id = {x}.{x}_id
               AND (xi.sku ILIKE $n OR xi.item_name ILIKE $n))
 OR EXISTS (SELECT 1 FROM customer c
             WHERE c.customer_id = {x}.{x}_customer_id
               AND (c.customer_name ILIKE $n OR c.customer_doc_num ILIKE $n)))
```

**Response envelope:** identical to Sales Order/Invoice — `{success, scope, records, nextCursor, hasMore}`. No `COUNT(*)`, no offset. Keyset only.

---

## 12. Validation Rules

**Header (both)**
- `customerUuid` required, must resolve to a live `customer` in this tenant; caller must have scope on it.
- On the Quote side: `estimateUuid`, if present, must resolve to a live Estimate the caller can read; **the conversion path only** — direct `POST /quotes` with an `estimateUuid` body field is rejected (400), same rule Invoice already applies to `salesOrderUuid`.
- `salesTaxPercent` 0–100; `exchangeRate` > 0.
- `paymentTermsId`/`priceLevelId`/`currencyId`/`stateId`/`countryId` — must reference live, active lookup rows.
- Money totals never negative (CHECK + service).
- `customFields` is stored as-is, **unvalidated** — this matches the actual, shipped behavior of `salesorder`/`invoice`'s `Create`/`Update`, confirmed during implementation planning: neither calls `workflow.ValidateCustomFields`/`ValidateCustomFieldsPartial`, because the only `workflow_field_definitions` rows that exist for `estimate`/`quote` belong to the legacy v1 JSONB workflow (an unrelated placeholder schema — see §1's "Key finding"), and validating against those would silently reject or misinterpret real custom-field data. Giving the new relational modules their own admin-configurable field-definition set (decoupled from the legacy workflow's) is real, non-trivial follow-up work applicable to `sales_order`/`invoice` too, not something to bolt onto this module alone — out of scope here.

**Lines (both)**
- ≥1 line required to move past `DRFT`.
- `lineNumber` unique per document among live rows (`uq_esti_line_active`/`uq_qi_line_active`); dense `1..N` enforced in service.
- `quantity` ≥ 0; `unitPrice` ≥ 0; `discountPercent`/`taxPercent` 0–100.
- `inventoryItemUuid` XOR `description`: catalog item or free-text line.

**Transitions** — only moves in `allowedEstimateTransitions`/`allowedQuoteTransitions`; `CONV` rejected as a manual target (400) on Quote; else 409.

**Conversions**
- Estimate → Quote: source status must be `APPV`/`SENT`; ≥1 line; 409 otherwise.
- Quote → Sales Order: source status must be `APPV`/`SENT`/`CONV`; ≥1 line; 409 otherwise.

**Tenant/RBAC/IDOR** — identical to Sales Order/Invoice: `authz.Check(resource, action)` before every mutation; every single-record op scope/IDOR-guarded, 404 on denial, `idor_denied` logged; scope composed into SQL, never filtered in Go.

---

## 13. Backend Implementation Map

| Concern | Action | Reference to mirror |
|---|---|---|
| Schema | Append 11 tables + indexes to `database/migrations/tenant/schema.sql` via **add-migration** skill, after the Sales Order block | `sales_order` + `sales_order_approver`/`_approval` blocks |
| RBAC | None — `ResourceEstimate`/`ResourceQuote` already fully seeded with all 5 actions | `authz/catalog.go:36-37,120-130` |
| Route registration | `main.go`, alongside Sales Order/Invoice routes | `main.go` SO/Invoice blocks |
| Controllers | New `controllers/estimate.go` (`EstimateOps`), `controllers/quote.go` (`QuoteOps`), each + `_audit.go`, mirroring `SalesOrderOps`/`InvoiceOps` | `controllers/salesorder.go`, `controllers/invoice.go` |
| Stores | New `estimate/store.go`, `quote/store.go` — transactional create/update/get/list/delete/search/transition/approve, split like `invoice/store_*.go` | `salesorder/store.go`, `invoice/store_*.go` |
| Conversion services | `estimate/convert.go` — `ConvertToQuote(ctx, pool, estimateUUID, actorEmployeeID) (*Quote, error)`; `quote/convert.go` — `ConvertToSalesOrder(ctx, pool, quoteUUID, actorEmployeeID) (*salesorder.Order, error)` | `salesorder/store.go` `Create` (transaction shape) |
| Resolvers | `estimate/resolver.go`, `quote/resolver.go` | `invoice/resolver.go` |
| Calc | `estimate/calc.go`, `quote/calc.go` — same formulas, separate packages | `salesorder/calc.go` |
| Transitions | `estimate/transitions.go`, `quote/transitions.go` | `salesorder/transitions.go` |
| Approval | `estimate/approval.go`, `quote/approval.go` | `salesorder/approval.go` |
| Numbering | `estimate/numbering.go` (`ESTM-%06d`), `quote/numbering.go` (`QUOT-%06d`) | `salesorder/numbering.go` |
| Audit | `estimate_history`/`quote_history` write helper, same shape as `sales_order_history` | `controllers/salesorder_audit.go` |
| Attachments | Reuse as-is; ensure `RecordKeyForAttachment` resolves `estimate`/`quote` UUIDs | `workflow/attachments.go` |
| Tests | Table-driven stdlib for `calc`/`transitions`/`numbering`/`resolver` (both packages); `//go:build dbtest`-gated `store_test.go` for Create/Update/Transition/Approve/Convert; filter-invariant tests for both resolvers | `salesorder/*_test.go`, `invoice/*_test.go` |
| Reviews | migration-auditor, tenancy-security-reviewer, filter-invariant-checker, feature-dev:code-reviewer, code-simplifier — before merge | — |

---

## 14. Open Decisions — Resolved During Brainstorming

1. **Estimate → Quote relationship (snapshot vs. editable).** Resolved: snapshot copy, not a live link. One Estimate may produce multiple Quotes.
2. **Revision strategy (new document vs. history).** Resolved: in-place edit + `{x}_history` trail, identical to every existing module — no new "revision" table.
3. **Approval workflow scope (Estimate, Quote, or both).** Resolved: both, using the exact `sales_order_approver`/`_approval` structural pattern twice.
4. **Quote → Sales Order conversion (single vs. multiple SOs).** Resolved: one Quote may convert into multiple Sales Orders (progress/phased conversion). Enforced via `UNIQUE (sales_order_id)` (not `quote_id`) on `quote_conversion`.
5. **Pricing snapshot vs. live pricing.** Resolved: snapshot at creation, frozen thereafter, identical to every existing document module.
6. **Quote → Sales Order lineage storage, given `sales_order`/`sales_order_item` already exist and cannot be `ALTER`ed.** Resolved: new `quote_conversion` mapping table, header-level lineage only (FK-backed); line-level lineage captured informally in that table's `snapshot` JSONB rather than a second relational mapping table, since `sales_order_item` cannot carry a lineage FK back to `quote_item` without an `ALTER TABLE` this codebase forbids. This is an explicit scope trade-off: line-level Quote→SalesOrder lineage is audit-JSONB, not FK-enforced (unlike Estimate→Quote line lineage, which *is* FK-enforced via `quote_item.estimate_item_id`, because `quote_item` is a brand-new table free to define that column).
7. **`{x}_parent_id` self-reference (present on `sales_order`/`invoice`, unused by any code path).** Omitted from `estimate`/`quote` by deliberate choice (AD-12) — copying an unexercised column would be pattern-matching, not reuse, given AD-7 already covers revisions via in-place edit + history.
