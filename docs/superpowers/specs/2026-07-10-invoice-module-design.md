# Invoice Module — Backend Design Spec

**Date:** 2026-07-10
**Status:** Draft — proceeding per explicit user direction (see Open Decisions §12 for the two scope calls made without a back-and-forth round).
**Scope:** New Invoice module (header + line items + status workflow + payments-lite) for the StoneSuite multi-tenant, database-per-tenant CRM/ERP backend, built as the sibling module to the Sales Order module (`docs/superpowers/specs/2026-07-08-sales-order-module-design.md`), including Sales Order → Invoice conversion.

---

## 1. Overview & Goals

Add a production-grade **Invoice** module that is a seamless extension of the v2 relational stack — same conventions as `customer` and `sales_order`: hybrid PK, employee-based audit, soft delete, `record_version`, RBAC/scope/IDOR, the `query/` filter engine, keyset pagination, R2 attachments. Not a parallel architecture.

**Non-negotiable constraints (from CLAUDE.md, identical to the Sales Order module):**

- Database-per-tenant; no `tenant_id` column anywhere.
- v2 relational conventions: hybrid PK (`SERIAL` + `UUID`), `employee(employee_id)`-based audit columns, paired soft delete, `record_version` optimistic concurrency.
- Idempotent, append-only migrations (`CREATE TABLE IF NOT EXISTS`, `ON CONFLICT DO NOTHING`).
- Mandatory security chain on every `/api/tenant/` route.
- All list/search goes through `query/` (whitelisted `FieldResolver`, parameterized values, keyset pagination, filter × scope ANDed).

### What already exists (reuse, do not recreate)

| Concern | Existing asset | Location |
|---|---|---|
| Billing customer | `customer` (v2 relational master) | `tenant/schema.sql:1087` |
| Actor / owner / sales rep | `employee` | `tenant/schema.sql:511` |
| Record type "Invoice" | `lkp_record_type` row `INVC` / `invoice` (id 7) | `tenant/schema.sql:697` |
| Invoice status lifecycle | `lkp_record_status` rows for `record_type=7`: `DRFT, PAPV, APPV, SENT, PART, PAID, ODUE, VOID` — **already seeded**, unused until now | `tenant/schema.sql:~724` |
| Sales Order (header + lines, the conversion source) | `sales_order`, `sales_order_item` (this branch, built immediately prior to this module) | `salesorder/` package + `tenant/schema.sql` |
| Payment terms | `lkp_payment_terms` | `tenant/schema.sql:832` |
| Price levels | `lkp_price_level` | `tenant/schema.sql:913` |
| Currency / Country / State | `lkp_currency`, `lkp_country`, `lkp_state` | `tenant/schema.sql` |
| RBAC pattern | `authz/catalog.go` — add `invoice` resource (5 actions), same shape as `sales_order` | `authz/catalog.go` |
| File attachments | `workflow_record_attachments` + R2, record-type-agnostic | `workflow/attachments.go` |
| Audit log | `audit_logs` (v2 path) | `tenant/schema.sql:1278` |
| Filter/sort/paginate/search | `query/` package (incl. the `SortResolver`/`SearchResolver` opt-in interfaces added for Sales Order) | `query/` |
| Money/line calc pattern | `salesorder/calc.go` (line + header totals) | `salesorder/calc.go` |
| Status transition pattern | `salesorder/transitions.go` (static Go map) | `salesorder/transitions.go` |
| Document numbering pattern | `salesorder/numbering.go` (Go post-insert format) | `salesorder/numbering.go` |

> **Key finding, mirrors the Sales Order precedent exactly:** "Invoice" today exists only as a bare **v1 generic JSONB workflow** (`workflows.key='invoice'`, seeded at `schema.sql:1699`, 5 flat custom fields incl. `total_amount`, states `inv_draft → inv_issued → inv_overdue → inv_paid` / `inv_void`, no line items, no customer/SO linkage). It has **no relational table or store**. This design introduces a dedicated v2 relational table set — a **sibling of `sales_order`** — reusing the `INVC` record-type and the `lkp_record_status` rows already reserved for it (`record_type=7`). The v1 workflow's state names (`Draft → Issued → Overdue → Paid`, `Void`) directly validate the seeded status set below. The legacy v1 `invoice` workflow is left untouched (no production data); the relational module supersedes it.

### What is genuinely missing (new tables — justified in §3)

- `invoice`, `invoice_item`, `invoice_history` — no existing table can represent a multi-line, snapshot-priced, SO-lineage AR document.

---

## 2. Architecture Decisions

**AD-1 — Dedicated relational tables, not the JSONB engine.** Same reasoning as Sales Order (AD-1 in that spec): line items, stored money, snapshots, and SO lineage don't fit the 15-field JSONB `workflow_records` model.

**AD-2 — `invoice` is a sibling of `sales_order`, not a child of it.** An invoice can exist standalone (direct billing, no SO) or be created from a Sales Order (`sales_order_id` nullable FK + lineage). This matches real AR practice (not every invoice traces to an SO) and mirrors how `sales_order.sales_order_parent_id` already anticipates a Quote→SO→Invoice chain (SO spec §14).

**AD-3 — Hybrid PK everywhere.** Identical to `customer`/`sales_order`: `SERIAL` internal, `UUID` external.

**AD-4 — Snapshot billing/shipping onto `invoice`; snapshot item data onto `invoice_item`.** When converting from a Sales Order, the invoice copies the SO's *already-snapshotted* billing/shipping block and each line's SKU/name/description/unit/price/tax/discount **as they existed at conversion time** — not a live re-read of `customer`/`inventory_item`. This is the "snapshot pricing" requirement: later price changes on the catalog item, or later edits to the customer's address, never retroactively change an issued invoice.

**AD-5 — Store computed money on rows; store AR balance on the header.** Line/header totals are computed once and stored (never recomputed on read), exactly like Sales Order (AD-5). Additionally, `invoice_amount_paid` / `invoice_balance_due` are stored and updated transactionally by a minimal payment-recording endpoint (§8) — there is no separate Payment/Credit-Memo module in this repo yet (those are reserved as `lkp_record_type` rows `PYMT`/`CRDT`, ids 8–9, for future work), so balance tracking lives directly on the invoice header for now. This is the smallest correct AR model that satisfies "invoice status workflow" without building an out-of-scope Payment module.

**AD-6 — Status transitions enforced in the Go service layer over the shared lookup**, identical pattern to `salesorder/transitions.go`. Reuses `lkp_record_status` (`record_type=7`, already seeded). Denied transitions → HTTP 409.

**AD-7 — Invoice number `INVC-000001` generated in Go post-insert**, identical pattern to `salesorder/numbering.go` / `customer_doc_num`. See §12.1 for why this — not a separate "numbering service" — is the correct interpretation of "reuse the existing Record Numbering service": **there is no standalone numbering microservice/package in this codebase; every v2 relational module (`customer`, `sales_order`) implements the same tiny insert→format→update pattern, and Invoice follows suit for consistency.**

**AD-8 — Sales Order → Invoice conversion is transactional and non-destructive to the source.** Converting an SO does not mutate or lock the `sales_order` row's status — it creates a new `invoice` + `invoice_item` rows in one Postgres transaction, with `invoice.sales_order_id` and each `invoice_item.sales_order_item_id` recording lineage. Multiple invoices may reference the same Sales Order (progress/partial billing is a legitimate real-world pattern); this design does not enforce 1:1 SO:Invoice.

---

## 3. New Tables — Per-Table Justification

### `invoice`
- **(a)** No existing table models a billable AR document with line items and a payment balance.
- **(b)** The invoice header: identity, classification, billing/shipping snapshot, terms, money totals, AR balance, optional SO lineage.
- **(c)** New master (hybrid PK, sibling of `sales_order`). FKs to `customer`, `sales_order` (nullable), `employee`, `lkp_record_type`(=INVC), `lkp_record_status`, `lkp_payment_terms`, `lkp_price_level`, `lkp_currency`, `lkp_state`, `lkp_country`.

### `invoice_item`
- **(a)** Line items are a distinct 1-to-many child; no existing table fits.
- **(b)** Invoiced lines with item snapshots + stored per-line money, optionally tracing back to the SO line it was converted from.
- **(c)** New child of `invoice`; FKs to `inventory_item` (nullable — free-text lines allowed, matches `sales_order_item`), `lkp_unit`, `lkp_tax_rate`, `sales_order_item` (nullable lineage).

### `invoice_history`
- **(a)** Generic `audit_logs` covers CRUD; the status lifecycle (incl. payment events) deserves a typed trail like `customer_history`/`sales_order_history`.
- **(b)** One row per status change / payment recorded, with `from_status_id`/`to_status_id` + JSONB snapshot.
- **(c)** New child of `invoice`; FKs to `lkp_record_status`, `employee`.

---

## 4. ER Diagram (text)

```
   lkp_currency      customer        sales_order (nullable)     employee        lkp_record_type/status
        │(1)            │(1)              │(0..1)                 │(1)                  │(1)
        │       ┌───────┼──────────────────┼───────────────────────┼──────────────────────┤
        ▼(N)    ▼(N)    ▼(N)                                                              ▼
 ┌──────────────────────────────────────────────────────────────────────────────────────────┐
 │                                    invoice  (header, NEW)                                  │
 │  PK invoice_id (serial) · UUID invoice_uuid · invoice_number "INVC-000001"                │
 │  FK customer_id · FK sales_order_id (nullable, lineage)                                    │
 │  record_type=INVC · invoice_status · billing/shipping snapshot                             │
 │  money totals (subtotal…grand_total) · amount_paid · balance_due · due_date                │
 └───────────┬───────────────────────────────────────────────┬───────────────────────────────┘
             │(1)                                             │(1)
     (N)     ▼                                         (N)    ▼
 ┌────────────────────────────┐                  ┌─────────────────────────────┐
 │  invoice_item  (NEW)       │                  │  invoice_history  (NEW)     │
 │  PK invoice_item_id        │                  │  from_status_id/to_status_id│
 │  FK invoice_id              │                  │  action (incl. 'payment')   │
 │  FK inventory_item_id (opt)│                  │  actor_employee_id·snapshot │
 │  FK sales_order_item_id ───┼──▶ sales_order_item (lineage, nullable)        │
 │  item snapshots · line $   │                  └─────────────────────────────┘
 │  FK unit_id · tax_rate_id  │
 └─────────────────────────────┘
```

**Cardinality summary**
- `customer` 1 ─── N `invoice`.
- `sales_order` 0..1 ─── N `invoice` (a SO may be invoiced in parts; an invoice traces to at most one SO).
- `invoice` 1 ─── N `invoice_item`, 1 ─── N `invoice_history`.
- `sales_order_item` 0..1 ─── N `invoice_item` (lineage for snapshot-priced conversion).

---

## 5. SQL — CREATE TABLE Statements

> Final DDL is appended to `database/migrations/tenant/schema.sql` via the **add-migration** skill. Same numeric standards as Sales Order: money `DECIMAL(15,2)`, quantity `DECIMAL(14,3)`, percent `DECIMAL(6,4)`, exchange rate `DECIMAL(18,6)`.

### 5.1 `invoice` (header)

```sql
CREATE TABLE IF NOT EXISTS invoice (
    invoice_id                  SERIAL        PRIMARY KEY,
    invoice_uuid                UUID          NOT NULL DEFAULT gen_random_uuid(),
    ss_customer_id               INTEGER          NULL,  -- platform owner stamp, no cross-DB FK (matches customer/sales_order)
    invoice_number               VARCHAR(20)      NULL,  -- 'INVC-000001', generated post-insert in Go

    -- Classification
    record_type                  INTEGER       NOT NULL REFERENCES lkp_record_type(record_type_id),   -- = INVC
    invoice_status                INTEGER       NOT NULL REFERENCES lkp_record_status(record_status_id),

    -- Source linkage
    invoice_customer_id          INTEGER       NOT NULL REFERENCES customer(customer_id),
    invoice_sales_order_id       INTEGER           NULL REFERENCES sales_order(sales_order_id),  -- lineage, nullable (standalone invoices allowed)

    -- Primary info
    invoice_po_number            VARCHAR(50)   NOT NULL DEFAULT '',
    invoice_reference_number     VARCHAR(50)   NOT NULL DEFAULT '',
    invoice_date                 DATE          NOT NULL DEFAULT CURRENT_DATE,
    invoice_due_date             DATE              NULL,
    invoice_sales_tax_percent    DECIMAL(6,4)  NOT NULL DEFAULT 0,
    invoice_memo                 TEXT          NOT NULL DEFAULT '',
    invoice_notes                TEXT          NOT NULL DEFAULT '',
    invoice_internal_notes       TEXT          NOT NULL DEFAULT '',
    invoice_terms_conditions     TEXT          NOT NULL DEFAULT '',

    -- Sales assignment
    invoice_sales_rep_id         INTEGER           NULL REFERENCES employee(employee_id),
    invoice_owner_id             INTEGER           NULL REFERENCES employee(employee_id),

    -- Terms / pricing / currency
    invoice_payment_terms        INTEGER           NULL REFERENCES lkp_payment_terms(payment_terms_id),
    invoice_price_level          INTEGER           NULL REFERENCES lkp_price_level(price_level_id),
    invoice_currency             INTEGER           NULL REFERENCES lkp_currency(currency_id),
    invoice_exchange_rate        DECIMAL(18,6) NOT NULL DEFAULT 1,

    -- Money summary (stored)
    invoice_subtotal             DECIMAL(15,2) NOT NULL DEFAULT 0,
    invoice_discount_total       DECIMAL(15,2) NOT NULL DEFAULT 0,
    invoice_tax_total            DECIMAL(15,2) NOT NULL DEFAULT 0,
    invoice_shipping_charge      DECIMAL(15,2) NOT NULL DEFAULT 0,
    invoice_adjustment           DECIMAL(15,2) NOT NULL DEFAULT 0,
    invoice_grand_total          DECIMAL(15,2) NOT NULL DEFAULT 0,

    -- AR balance (stored, updated by payment-recording + transitions)
    invoice_amount_paid          DECIMAL(15,2) NOT NULL DEFAULT 0,
    invoice_balance_due          DECIMAL(15,2) NOT NULL DEFAULT 0,

    -- Billing snapshot (copied from customer, or from sales_order on conversion)
    invoice_bill_customer_name   VARCHAR(150) NOT NULL DEFAULT '',
    invoice_bill_attention       VARCHAR(150) NOT NULL DEFAULT '',
    invoice_bill_addr_line1      VARCHAR(100) NOT NULL DEFAULT '',
    invoice_bill_addr_line2      VARCHAR(100) NOT NULL DEFAULT '',
    invoice_bill_addr_suitenum   VARCHAR(20)  NOT NULL DEFAULT '',
    invoice_bill_addr_city       VARCHAR(100) NOT NULL DEFAULT '',
    invoice_bill_addr_state      INTEGER          NULL REFERENCES lkp_state(state_id),
    invoice_bill_addr_zip        VARCHAR(10)  NOT NULL DEFAULT '',
    invoice_bill_addr_country    INTEGER          NULL REFERENCES lkp_country(country_id),
    invoice_bill_phone           VARCHAR(20)  NOT NULL DEFAULT '',
    invoice_bill_fax             VARCHAR(20)  NOT NULL DEFAULT '',
    invoice_bill_email           VARCHAR(100) NOT NULL DEFAULT '',

    -- Shipping snapshot
    invoice_ship_same_as_bill    BOOLEAN      NOT NULL DEFAULT FALSE,
    invoice_ship_customer_name   VARCHAR(150) NOT NULL DEFAULT '',
    invoice_ship_attention       VARCHAR(150) NOT NULL DEFAULT '',
    invoice_ship_addr_line1      VARCHAR(100) NOT NULL DEFAULT '',
    invoice_ship_addr_line2      VARCHAR(100) NOT NULL DEFAULT '',
    invoice_ship_addr_suitenum   VARCHAR(20)  NOT NULL DEFAULT '',
    invoice_ship_addr_city       VARCHAR(100) NOT NULL DEFAULT '',
    invoice_ship_addr_state      INTEGER          NULL REFERENCES lkp_state(state_id),
    invoice_ship_addr_zip        VARCHAR(10)  NOT NULL DEFAULT '',
    invoice_ship_addr_country    INTEGER          NULL REFERENCES lkp_country(country_id),
    invoice_ship_phone           VARCHAR(20)  NOT NULL DEFAULT '',
    invoice_ship_fax             VARCHAR(20)  NOT NULL DEFAULT '',
    invoice_ship_email           VARCHAR(100) NOT NULL DEFAULT '',

    -- Dynamic + lineage + audit
    invoice_custom_fields        JSONB        NOT NULL DEFAULT '{}',
    invoice_parent_id            INTEGER          NULL REFERENCES invoice(invoice_id),  -- amendments/duplicates
    invoice_created_at           TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    invoice_created_by           INTEGER          NULL REFERENCES employee(employee_id),
    invoice_updated_at           TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    invoice_updated_by            INTEGER          NULL REFERENCES employee(employee_id),
    invoice_deleted_at            TIMESTAMP        NULL,
    invoice_deleted_by            INTEGER          NULL REFERENCES employee(employee_id),
    invoice_record_version        INTEGER      NOT NULL DEFAULT 1,

    CONSTRAINT uq_invoice_uuid     UNIQUE (invoice_uuid),
    CONSTRAINT uq_invoice_number   UNIQUE (invoice_number),
    CONSTRAINT chk_invoice_tax_percent   CHECK (invoice_sales_tax_percent >= 0 AND invoice_sales_tax_percent <= 100),
    CONSTRAINT chk_invoice_totals_nonneg CHECK (invoice_subtotal >= 0 AND invoice_grand_total >= 0),
    CONSTRAINT chk_invoice_paid_nonneg   CHECK (invoice_amount_paid >= 0 AND invoice_balance_due >= 0),
    CONSTRAINT chk_invoice_soft_delete   CHECK (
        (invoice_deleted_at IS NULL AND invoice_deleted_by IS NULL) OR
        (invoice_deleted_at IS NOT NULL AND invoice_deleted_by IS NOT NULL)
    )
);
```

### 5.2 `invoice_item` (line items)

```sql
CREATE TABLE IF NOT EXISTS invoice_item (
    invoice_item_id          SERIAL        PRIMARY KEY,
    invoice_item_uuid        UUID          NOT NULL DEFAULT gen_random_uuid(),
    invoice_id                INTEGER       NOT NULL REFERENCES invoice(invoice_id) ON DELETE CASCADE,
    line_number               INTEGER       NOT NULL,
    inventory_item_id         INTEGER           NULL REFERENCES inventory_item(inventory_item_id),   -- NULL = free-text line
    sales_order_item_id       INTEGER           NULL REFERENCES sales_order_item(sales_order_item_id), -- lineage from SO conversion

    -- Snapshots (frozen at add/conversion time — never re-read from catalog)
    item_name                 VARCHAR(150)  NOT NULL DEFAULT '',
    sku                       VARCHAR(50)   NOT NULL DEFAULT '',
    description                TEXT          NOT NULL DEFAULT '',
    unit_id                    INTEGER           NULL REFERENCES lkp_unit(unit_id),
    unit_code                  VARCHAR(10)   NOT NULL DEFAULT '',
    quantity                   DECIMAL(14,3) NOT NULL DEFAULT 0,
    unit_price                 DECIMAL(15,2) NOT NULL DEFAULT 0,
    discount_percent           DECIMAL(6,4)  NOT NULL DEFAULT 0,
    tax_rate_id                 INTEGER           NULL REFERENCES lkp_tax_rate(tax_rate_id),
    tax_percent                 DECIMAL(6,4)  NOT NULL DEFAULT 0,

    -- Stored line money
    line_subtotal               DECIMAL(15,2) NOT NULL DEFAULT 0,
    line_discount                DECIMAL(15,2) NOT NULL DEFAULT 0,
    line_tax                     DECIMAL(15,2) NOT NULL DEFAULT 0,
    line_total                   DECIMAL(15,2) NOT NULL DEFAULT 0,

    item_created_at              TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    item_created_by              INTEGER           NULL REFERENCES employee(employee_id),
    item_updated_at              TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    item_deleted_at               TIMESTAMP        NULL,
    item_record_version           INTEGER       NOT NULL DEFAULT 1,

    CONSTRAINT uq_invoice_item_uuid UNIQUE (invoice_item_uuid),
    CONSTRAINT uq_ii_line           UNIQUE (invoice_id, line_number),
    CONSTRAINT chk_ii_qty           CHECK (quantity >= 0),
    CONSTRAINT chk_ii_unit_price    CHECK (unit_price >= 0),
    CONSTRAINT chk_ii_discount      CHECK (discount_percent >= 0 AND discount_percent <= 100),
    CONSTRAINT chk_ii_tax           CHECK (tax_percent >= 0 AND tax_percent <= 100)
);
```

### 5.3 `invoice_history`

```sql
CREATE TABLE IF NOT EXISTS invoice_history (
    invoice_history_id       SERIAL       PRIMARY KEY,
    invoice_id                INTEGER      NOT NULL REFERENCES invoice(invoice_id) ON DELETE CASCADE,
    from_status_id             INTEGER          NULL REFERENCES lkp_record_status(record_status_id),
    to_status_id                INTEGER          NULL REFERENCES lkp_record_status(record_status_id),
    action                      VARCHAR(32)  NOT NULL DEFAULT 'transition', -- create | transition | convert | payment | update
    actor_employee_id            INTEGER          NULL REFERENCES employee(employee_id),
    snapshot                     JSONB        NOT NULL DEFAULT '{}',
    at                           TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

### 5.4 Indexes

```sql
-- invoice (listing/filtering — all partial on live rows)
CREATE INDEX IF NOT EXISTS idx_inv_customer      ON invoice (invoice_customer_id)     WHERE invoice_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_inv_sales_order    ON invoice (invoice_sales_order_id)  WHERE invoice_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_inv_status          ON invoice (invoice_status)          WHERE invoice_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_inv_date            ON invoice (invoice_date)            WHERE invoice_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_inv_due_date        ON invoice (invoice_due_date)        WHERE invoice_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_inv_sales_rep       ON invoice (invoice_sales_rep_id)    WHERE invoice_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_inv_owner           ON invoice (invoice_owner_id)        WHERE invoice_deleted_at IS NULL;
-- Keyset pagination tiebreakers (per sortable column + id)
CREATE INDEX IF NOT EXISTS idx_inv_created_id      ON invoice (invoice_created_at, invoice_id)     WHERE invoice_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_inv_updated_id      ON invoice (invoice_updated_at, invoice_id)     WHERE invoice_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_inv_duedate_id      ON invoice (invoice_due_date, invoice_id)       WHERE invoice_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_inv_grandtotal_id   ON invoice (invoice_grand_total, invoice_id)    WHERE invoice_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_inv_balance_id      ON invoice (invoice_balance_due, invoice_id)    WHERE invoice_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_inv_status_created  ON invoice (invoice_status, invoice_created_at, invoice_id) WHERE invoice_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_inv_custom_gin      ON invoice USING GIN (invoice_custom_fields);

-- invoice_item
CREATE INDEX IF NOT EXISTS idx_ii_invoice     ON invoice_item (invoice_id)          WHERE item_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_ii_item        ON invoice_item (inventory_item_id);
CREATE INDEX IF NOT EXISTS idx_ii_so_item     ON invoice_item (sales_order_item_id);

-- invoice_history
CREATE INDEX IF NOT EXISTS idx_inv_history_invoice ON invoice_history (invoice_id);
```

> **Migration ordering:** `invoice` FKs `sales_order`, so this migration must be appended **after** the Sales Order tables exist. `invoice_item` FKs `sales_order_item` + `inventory_item` similarly.

---

## 6. Foreign Key Relationships (explained)

| Child column | → Parent | Meaning | On delete |
|---|---|---|---|
| `invoice.invoice_customer_id` | `customer.customer_id` | Billing customer | RESTRICT |
| `invoice.invoice_sales_order_id` | `sales_order.sales_order_id` | Optional conversion source | RESTRICT (nullable) |
| `invoice.record_type` | `lkp_record_type.record_type_id` | Always `INVC` | RESTRICT |
| `invoice.invoice_status` | `lkp_record_status.record_status_id` | Lifecycle status (`record_type=7` set) | RESTRICT |
| `invoice.invoice_payment_terms` / `_price_level` / `_currency` | `lkp_payment_terms` / `lkp_price_level` / `lkp_currency` | Defaulted from customer or SO | RESTRICT |
| `invoice.invoice_sales_rep_id` / `_owner_id` | `employee.employee_id` | Assignment | RESTRICT |
| `invoice.invoice_bill_addr_state` / `_ship_addr_state` | `lkp_state.state_id` | Snapshot address | RESTRICT |
| `invoice.invoice_bill_addr_country` / `_ship_addr_country` | `lkp_country.country_id` | Snapshot address | RESTRICT |
| `invoice.invoice_parent_id` | `invoice.invoice_id` | Self-ref for amendments | RESTRICT |
| `invoice_item.invoice_id` | `invoice.invoice_id` | Owning invoice | **CASCADE** |
| `invoice_item.inventory_item_id` | `inventory_item.inventory_item_id` | Catalog item (nullable) | RESTRICT |
| `invoice_item.sales_order_item_id` | `sales_order_item.sales_order_item_id` | Conversion lineage (nullable) | RESTRICT |
| `invoice_item.unit_id` / `tax_rate_id` | `lkp_unit` / `lkp_tax_rate` | Snapshot refs | RESTRICT |
| `invoice_history.invoice_id` | `invoice.invoice_id` | Owning invoice | **CASCADE** |
| `invoice_history.from_status_id` / `to_status_id` | `lkp_record_status.record_status_id` | Transition endpoints | RESTRICT |

No cross-database FKs. No `tenant_id` anywhere.

---

## 7. Status Transition Rules (service layer)

Statuses come from `lkp_record_status` where `record_status_record_type = 7` (INVC): `DRFT, PAPV, APPV, SENT, PART, PAID, ODUE, VOID` — validated against the legacy v1 workflow's state names (`Draft → Issued → Overdue → Paid`, `Void`), which map onto `SENT`≈"Issued".

```
DRFT (Draft) ──▶ PAPV (Pending Approval) ──▶ APPV (Approved) ──▶ SENT (Sent/Issued)
   │                  │                                              │
   │                  └──▶ DRFT (reject back to draft)                ├──▶ PART (Partially Paid)
   ▼                                                                  ├──▶ PAID (Paid)  [terminal]
 VOID ◀───────────── from DRFT / PAPV / APPV / SENT / PART / ODUE     └──▶ ODUE (Overdue)
 [terminal]                                                                │
                                                              ODUE ──▶ PART / PAID / VOID
                                                              PART ──▶ PAID / VOID
```

```go
// allowedInvoiceTransitions[fromCode] = set of reachable toCodes
var allowedInvoiceTransitions = map[string]map[string]bool{
    "DRFT": {"PAPV": true, "VOID": true},
    "PAPV": {"APPV": true, "DRFT": true, "VOID": true},
    "APPV": {"SENT": true, "VOID": true},
    "SENT": {"PART": true, "PAID": true, "ODUE": true, "VOID": true},
    "PART": {"PAID": true, "ODUE": true, "VOID": true},
    "ODUE": {"PART": true, "PAID": true, "VOID": true},
    "PAID": {},   // terminal
    "VOID": {},   // terminal
}
```

- New invoices start at **`DRFT`** (including SO-converted ones — conversion does not auto-approve).
- `PART`/`PAID`/`ODUE` are normally driven by the payment-recording endpoint (§8) recomputing `invoice_balance_due`, but manual transition is allowed for operators (e.g. marking VOID, or manually marking ODUE past due date).
- **Recording a payment auto-transitions**: `balance_due == 0` → `PAID`; `0 < amount_paid < grand_total` → `PART` (only from `SENT`/`ODUE`/`PART` — payments aren't accepted on `DRFT`/`PAPV`/`APPV`/`VOID`, checked in the service).
- Voiding does **not** release anything (no inventory allocation lives on `invoice` — that's owned by `sales_order`); it just freezes the record.

---

## 8. Money, Balance & Snapshot-Pricing Rules

**Per line (`invoice_item`, stored) — identical formula to `sales_order_item`:**
```
line_subtotal = round(quantity * unit_price, 2)
line_discount = round(line_subtotal * discount_percent / 100, 2)
line_tax      = round((line_subtotal - line_discount) * tax_percent / 100, 2)
line_total    = line_subtotal - line_discount + line_tax
```

**Per header (`invoice`, stored, recomputed on every line mutation inside the same transaction):**
```
subtotal       = Σ line_subtotal
discount_total = Σ line_discount
tax_total      = Σ line_tax
grand_total    = subtotal - discount_total + tax_total + shipping_charge + adjustment
balance_due    = grand_total - amount_paid
```

**Snapshot pricing (SO → Invoice conversion):** every `invoice_item` created by conversion copies `sales_order_item`'s **already-frozen** snapshot columns (`item_name, sku, description, unit_id, unit_code, quantity, unit_price, discount_percent, tax_rate_id, tax_percent`) verbatim — it does **not** re-read `inventory_item.inventory_item_unit_price` or re-derive tax. This guarantees the invoice reflects exactly what was quoted/ordered, even if the catalog price changes before invoicing. The invoice then recomputes its own `line_subtotal/discount/tax/total` from those copied inputs (not copied from the SO line's stored money, since header-level adjustments like `shipping_charge`/`adjustment` may differ between SO and invoice) — this is a defensive recompute, not a re-price.

**Payment recording (`invoice_amount_paid` += requested amount, clamped so `amount_paid` never exceeds `grand_total`):**
```
new_amount_paid = min(grand_total, amount_paid + payment.amount)
new_balance_due = grand_total - new_amount_paid
```
Overpayment (`payment.amount` would push `amount_paid` past `grand_total`) is rejected with 400, not silently clamped — clamping would silently discard money.

---

## 9. Sales Order → Invoice Conversion

**Endpoint:** `POST /api/tenant/sales-orders/{uuid}/convert-to-invoice`

**Preconditions (checked in the service, under a DB transaction):**
1. Caller has `sales_order:read` + scope on the SO, and `invoice:create`.
2. SO status is convertible: `APPV`, `OPEN`, `PART`, or `FILL` (not `DRFT`/`PAPV`/`CANC` — an unapproved or cancelled order has nothing billable). 409 otherwise.
3. SO has at least one non-deleted line. 409 (empty conversion) otherwise.

**Transaction body (single Postgres transaction, mirrors `salesorder/store.go`'s `Create`):**
1. Re-fetch the SO header + lines **inside the transaction** (`SELECT ... FOR UPDATE` on the SO row) to avoid converting a stale read if another request is mutating it concurrently.
2. Insert `invoice` row: `invoice_customer_id` = SO's customer, `invoice_sales_order_id` = SO id, billing/shipping snapshot copied verbatim from the SO's own snapshot columns (not re-read from `customer`), payment terms/price level/currency copied from SO, `invoice_status` = `DRFT`, `invoice_date` = today, `invoice_due_date` = today + payment-terms day offset (static Go lookup table `paymentTermsNetDays{"N30_": 30, "N60_": 60, ...}`, `nil` if terms unrecognized — no schema change needed since `lkp_payment_terms` has no numeric days column).
3. Insert one `invoice_item` per SO line: copy all snapshot columns + `sales_order_item_id` lineage FK; recompute line money via §8.
4. Compute + store header totals via §8 (`amount_paid=0`, `balance_due=grand_total`).
5. Generate `invoice_number` post-insert (`INVC-%06d`), same pattern as `sales_order_number`.
6. Insert `invoice_history` row: `action='convert'`, `to_status_id=DRFT`, `snapshot={"source_sales_order_id": ..., "source_sales_order_number": ...}`.
7. Commit. Return the created invoice (with items) — same envelope shape as `POST /api/tenant/invoices`.

**Idempotency note:** repeated calls create additional invoices (progress billing is intentional, per AD-8) — this is **not** idempotent by design, matching how repeatedly clicking "create invoice" in NetSuite/QBO creates multiple invoices. If the frontend needs single-click safety, that's a client-side disable-after-click concern, not a server-side dedup rule (no unique constraint ties one SO to one invoice).

---

## 10. API Contracts

All under `/api/tenant/`, through `tenantChain`, RBAC-checked in-handler, IDOR-guarded (404 on scope denial), same envelope as Sales Order (`{success, message?, ...}`).

| Method & path | Purpose | RBAC |
|---|---|---|
| `GET  /api/tenant/invoices` | Simple in-scope list, cursor-paginated | `invoice:read` + scope |
| `POST /api/tenant/invoices/search` | Full filter + sort + search + pagination | `invoice:read` + scope |
| `GET  /api/tenant/invoices/{uuid}` | Get one (+ items) | `invoice:read` + IDOR |
| `POST /api/tenant/invoices` | Create (header + items, standalone — not from an SO) | `invoice:create` |
| `PATCH /api/tenant/invoices/{uuid}` | Update header/items, recompute totals | `invoice:update` + IDOR |
| `DELETE /api/tenant/invoices/{uuid}` | Soft delete | `invoice:delete` + IDOR |
| `POST /api/tenant/invoices/{uuid}/transition` | Status change (validated map, §7) | `invoice:transition` + IDOR |
| `POST /api/tenant/invoices/{uuid}/payments` | Record a payment; updates balance + may auto-transition | `invoice:transition` + IDOR |
| `GET  /api/tenant/invoices/{uuid}/audit` | Audit / history trail | `invoice:read` + IDOR |
| `POST /api/tenant/sales-orders/{uuid}/convert-to-invoice` | SO → Invoice conversion (§9) | `sales_order:read` + `invoice:create` + IDOR on the SO |
| `*/attachments*` | Reuse existing generic attachment routes, `record_id = invoice_uuid` | — |

**Create request** (mirrors the Sales Order shape exactly, spec §10 of that doc):
```json
POST /api/tenant/invoices
{
  "customerUuid": "9d0f…c2",
  "salesOrderUuid": null,
  "poNumber": "PO-88213",
  "invoiceDate": "2026-07-10",
  "dueDate": "2026-08-09",
  "paymentTermsId": 3,
  "priceLevelId": 2,
  "currencyId": 1,
  "salesRepEmployeeId": 12,
  "ownerEmployeeId": 7,
  "salesTaxPercent": 8.25,
  "memo": "Progress billing #1",
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

**Payment request**
```json
POST /api/tenant/invoices/{uuid}/payments
{ "amount": 500.00, "note": "Check #1042" }
→ 200 { "success": true, "invoice": { "amountPaid": 500.00, "balanceDue": 1018.14, "status": "Partially Paid" } }
→ 400 { "success": false, "message": "Payment exceeds remaining balance." }
```

**Convert request/response**
```json
POST /api/tenant/sales-orders/{uuid}/convert-to-invoice
{} 
→ 201 { "success": true, "invoice": { "id": "…", "invoiceNumber": "INVC-000007", "status": "Draft",
        "salesOrder": { "id": "6f2c…9a", "salesOrderNumber": "SORD-000042" }, "grandTotal": 1518.14, "items": [...] } }
→ 409 { "success": false, "message": "Sales order must be Approved or later to convert to an invoice." }
```

---

## 11. Listing & Query Architecture (identical pattern to Sales Order §11)

Reuses `query/` unchanged — no new query engine. Only new code: `invoiceResolver` (`query.FieldResolver` + `SortResolver` + `SearchResolver`) and `Store.Search` (keyset).

**FieldResolver whitelist** (table alias `inv` = `invoice`):

| Logical key | SQL expression | DataType | Ops |
|---|---|---|---|
| `id` | `inv.invoice_uuid::text` | string | eq |
| `document_number` / `record_number` | `COALESCE(inv.invoice_number,'')` | string | eq, contains, startswith |
| `customer_id` | `inv.invoice_customer_id::text` | string | eq, in |
| `sales_order_id` | `inv.invoice_sales_order_id::text` | string | eq, is_null |
| `status` | `inv.invoice_status::text` | string | eq, in |
| `sales_rep_id` / `owner_id` | `inv.invoice_sales_rep_id::text` / `_owner_id::text` | string | eq, in, is_null |
| `invoice_date` | `inv.invoice_date` | date | eq, gt, gte, lt, lte, between |
| `due_date` | `inv.invoice_due_date` | date | gte, lte, between, is_null |
| `currency_id` / `payment_terms_id` / `price_level_id` | respective `inv.invoice_*` | string | eq, in, is_null |
| `grand_total` / `balance_due` | `inv.invoice_grand_total` / `inv.invoice_balance_due` | number | eq, gt, gte, lt, lte, between |
| `po_number` | `inv.invoice_po_number` | string | eq, contains, startswith |
| `created_by` / `updated_by` | `inv.invoice_created_by::text` / `_updated_by::text` | string | eq, in, is_null |
| `created_at` / `updated_at` | `inv.invoice_created_at` / `_updated_at` | date | gte, lte, between |
| `cf:<key>` | `inv.invoice_custom_fields->>'<key>'` | per `workflow_field_definitions` | per type |

**SortResolver whitelist:** `document_number, invoice_date, due_date, grand_total, balance_due, status, customer_id, created_at, updated_at` — each `NOT NULL`, paired with `invoice_id` tiebreaker.

**SearchPredicate:**
```sql
(   inv.invoice_number             ILIKE $n
 OR inv.invoice_po_number          ILIKE $n
 OR inv.invoice_memo               ILIKE $n
 OR inv.invoice_notes              ILIKE $n
 OR inv.invoice_bill_customer_name ILIKE $n
 OR EXISTS (SELECT 1 FROM invoice_item ii
             WHERE ii.invoice_id = inv.invoice_id
               AND (ii.sku ILIKE $n OR ii.item_name ILIKE $n))
 OR EXISTS (SELECT 1 FROM customer c
             WHERE c.customer_id = inv.invoice_customer_id
               AND (c.customer_name ILIKE $n OR c.customer_doc_num ILIKE $n)))
```

**Response envelope:** identical to Sales Order — `{success, scope, records, nextCursor, hasMore}`. No `COUNT(*)`, no offset. Keyset only.

---

## 12. Validation Rules

**Header**
- `customerUuid` required, must resolve to a live `customer` in this tenant; caller must have scope on it.
- `salesOrderUuid`, if present, must resolve to a live SO the caller can read; conversion path only (direct `POST /invoices` with a `salesOrderUuid` is rejected — use the conversion endpoint so the transactional snapshot logic runs).
- `salesTaxPercent` 0–100; `exchangeRate` > 0.
- `paymentTermsId`/`priceLevelId`/`currencyId`/`stateId`/`countryId` — must reference live, active lookup rows.
- Money totals never negative (CHECK + service). `amount_paid`/`balance_due` never negative.
- `customFields` validated against the `invoice` workflow's `workflow_field_definitions` (≤15, type/required/enum/regex) via `workflow.ValidateCustomFields`/`ValidateCustomFieldsPartial`.

**Lines**
- ≥1 line required to move past `DRFT`.
- `lineNumber` unique per invoice (DB `uq_ii_line`); dense 1..N enforced in service.
- `quantity` ≥ 0; `unitPrice` ≥ 0; `discountPercent`/`taxPercent` 0–100.
- `inventoryItemUuid` XOR `description`: catalog item or free-text line.

**Payments**
- `amount` > 0.
- Rejected (400) if it would push `amount_paid` above `grand_total`.
- Rejected (409) if invoice status is `DRFT`/`PAPV`/`APPV`/`VOID` (not yet sent, or already void).

**Transitions** — only moves in `allowedInvoiceTransitions`; else 409.

**Tenant/RBAC/IDOR** — identical to Sales Order §12: `authz.Check(invoice, action)` before every mutation; every single-record op scope/IDOR-guarded, 404 on denial, `idor_denied` logged; scope composed into SQL, never filtered in Go.

---

## 13. Backend Implementation Map

| Concern | Action | Reference to mirror |
|---|---|---|
| Schema | Append to `database/migrations/tenant/schema.sql` via **add-migration** skill, after Sales Order tables exist | `sales_order` block |
| RBAC | Add `ResourceInvoice` (+ 5 actions incl. `transition`) to `authz/catalog.go` | `authz/catalog.go` `sales_order` entry |
| Route registration | `main.go`, alongside Sales Order routes | `main.go` SO block |
| Controller | New `controllers/invoice.go` (`InvoiceOps`) mirroring `SalesOrderOps` | `controllers/salesorder.go` |
| Store | New `invoice/store.go`, transactional create/update/get/list/delete/search/transition/recordPayment | `salesorder/store.go` |
| Conversion service | `invoice/convert.go` — `ConvertSalesOrder(ctx, pool, soUUID, actorEmployeeID) (*Invoice, error)`, called from `controllers/salesorder.go`'s new `ConvertToInvoice` handler | `salesorder/store.go` `Create` (transaction shape) |
| Resolver | New `invoice/resolver.go` | `salesorder/resolver.go` |
| Calc | `invoice/calc.go` — same formulas, separate package (no cross-import of `salesorder` internals; both depend only on shared primitives) | `salesorder/calc.go` |
| Transitions | `invoice/transitions.go` | `salesorder/transitions.go` |
| Numbering | `invoice/numbering.go` — `INVC-%06d` | `salesorder/numbering.go` |
| Audit | `invoice_history` write helper, same shape as `sales_order_history` | `controllers/crm_audit.go` |
| Attachments | Reuse as-is; ensure `RecordKeyForAttachment` resolves an `invoice` UUID | `workflow/attachments.go` |
| Tests | Table-driven for `calc`/`transitions`/`numbering`; `dbtest`-tagged integration tests for store/conversion; filter-invariant tests for resolver | `salesorder/*_test.go` |
| Security review | **tenancy-security-reviewer**, **migration-auditor**, **filter-invariant-checker** before merge | — |

---

## 14. Open Decisions — Resolved Without a Back-and-Forth (flagging per brainstorming practice)

1. **"Existing Record Numbering service" (AD-7).** There is no standalone numbering service/package in this codebase — `customer_doc_num` and `sales_order_number` are both generated by a tiny per-module Go post-insert formatter. Invoice follows the identical pattern (`invoice/numbering.go`, `INVC-%06d`). If a *real* shared numbering service is wanted later, it should be extracted once (e.g. a `numbering` package with `Format(prefix string, id int) string` + an atomic-sequence-per-prefix option), and all three modules (`customer`, `sales_order`, `invoice`) migrated onto it together — out of scope here to avoid an unrelated refactor of already-shipped code.
2. **Payment recording scope (AD-5, §8).** The request asks for "invoice status workflow" but not a full Payment/Credit-Memo module (those already have reserved `lkp_record_type` rows `PYMT`/`CRDT` for future work). Rather than block on building an entire Payment subsystem, this design adds the smallest correct primitive — `amount_paid`/`balance_due` on the invoice header plus one `POST .../payments` endpoint — sufficient to drive `SENT → PART → PAID` and `ODUE`. A real Payment module (multi-invoice payment application, refunds, reconciliation) is future work that can layer on top without a schema change (it would write to `invoice.invoice_amount_paid` the same way this endpoint does).
3. **Due date default (§9).** `lkp_payment_terms` has no numeric "net days" column (only name/code, e.g. `N30_`). A static Go lookup table maps known codes to day offsets for defaulting `invoice_due_date` on conversion; explicit `dueDate` in the request always overrides it. If precise terms math matters later, add a `payment_terms_net_days` column via a normal additive migration — not needed for this scope.
4. **Multiple invoices per Sales Order (AD-8).** Deliberately unconstrained (no unique `sales_order_id` on `invoice`) to support progress/partial billing, a standard AR pattern. If the business wants strict 1:1, add a partial unique index later (`WHERE invoice_deleted_at IS NULL`) — not assumed here since it wasn't requested and would block legitimate partial-invoice workflows.
