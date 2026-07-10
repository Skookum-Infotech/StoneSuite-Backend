# Sales Order Module — Backend Design Spec

**Date:** 2026-07-08
**Status:** Approved architecture; ready for implementation planning
**Author:** Backend architecture pass (Claude)
**Scope:** New Sales Order module + shared Inventory domain module for the StoneSuite multi-tenant, database-per-tenant CRM/ERP backend.

---

## 1. Overview & Goals

Add a production-grade **Sales Order** module to the StoneSuite backend, plus the shared **Inventory** domain it depends on (items, stock, allocations, and the unit/warehouse/tax-rate reference data). The module must be a seamless extension of the existing v2 relational CRM (`customer`) — same conventions, same middleware chain, same RBAC/audit/attachment/filter infrastructure — not a parallel architecture.

**Non-negotiable constraints (from CLAUDE.md, verified against `database/migrations/tenant/schema.sql`):**

- **Database-per-tenant.** No `tenant_id`/`ss_tenant_id` column on any tenant-DB table; the DB connection *is* the tenant scope.
- **v2 relational conventions.** Hybrid PK (`SERIAL` internal + `UUID` public), table-name-prefixed columns on master tables, `employee(employee_id)` for `created_by`/`updated_by`, soft delete via paired `deleted_at`/`deleted_by`, `record_version` optimistic-concurrency counter.
- **Idempotent, append-only migrations.** Edit the single canonical `tenant/schema.sql` with `CREATE TABLE IF NOT EXISTS` / `INSERT ... ON CONFLICT DO NOTHING`. Never `ALTER ... ADD COLUMN` without `IF NOT EXISTS` + `DEFAULT`; never drop/rename.
- **Mandatory security chain** on every `/api/tenant/` route: `RequireAuth → per-tenant rate limit → TenantResolver → RBAC check → scope filter → IDOR guard (404 on denial) → security logging`.
- **Filter engine invariants.** All list/search goes through the `query/` package: whitelisted `FieldResolver`, parameterized values, keyset pagination, filter × scope ANDed.

### What already exists (reuse, do not recreate)

| Concern | Existing asset | Location |
|---|---|---|
| Billing customer | `customer` (v2 relational master) | `tenant/schema.sql:1087` |
| Actor / owner / sales rep | `employee` | `tenant/schema.sql:511` |
| Record type "Sales Order" | `lkp_record_type` row `SORD` / `salesorder` (id 6) | `tenant/schema.sql:696` |
| SO status lifecycle | `lkp_record_status` rows for `record_type=6`: `DRFT, PAPV, APPV, OPEN, PART, FILL, CANC` | `tenant/schema.sql:734` |
| Payment terms | `lkp_payment_terms` | `tenant/schema.sql:832` |
| Price levels | `lkp_price_level` | `tenant/schema.sql:913` |
| Currency | `lkp_currency` | `tenant/schema.sql:557` |
| Country / State | `lkp_country`, `lkp_state` | `tenant/schema.sql:584`, `:611` |
| RBAC resource `sales_order` (all 5 actions) | `authz/catalog.go:37,124-128` (+ `resourceForKey` `crm.go:59`, drift test) | already seeded |
| File attachments | `workflow_record_attachments` + R2 client, record-type-agnostic (fallback already handles "sales order, invoice") | `workflow/attachments.go:139-143`, `storage/r2.go` |
| Audit log | `audit_logs` (v2 path writes `actor_user_id = NULL`, stores `employee_id` in `details`) | `tenant/schema.sql:1278` |
| Filter/sort/paginate | `query/` package + per-entity `FieldResolver` | `query/`, `crmstore/relational_filter.go` |

### What is genuinely missing (new tables — justified in §3)

- **Inventory domain:** `inventory_item`, `inventory_stock`, `inventory_allocation`, `lkp_unit`, `lkp_warehouse`, `lkp_tax_rate`.
- **Sales Order:** `sales_order`, `sales_order_item`, `sales_order_history` (transition trail, mirrors `customer_history`).

> **Key finding:** "Sales Order" today exists only as a bare **v1 generic JSONB workflow** (`workflows.key='sales_order'`, seeded at `schema.sql:1617`, 4 flat custom fields, no line items, no inventory). It has **no relational table or store**. This design introduces a dedicated v2 relational table set — a **sibling of `customer`** — reusing the `SORD` record-type and `lkp_record_status` rows already reserved for it. The legacy v1 `sales_order` workflow is left untouched (it holds no production data of value); the relational module supersedes it.

---

## 2. Architecture Decisions

**AD-1 — Dedicated relational tables, not the generic JSONB engine.**
A real Sales Order has *ordered line items*, *inventory linkage*, *stored money totals*, and *snapshots* — none of which the JSONB `workflow_records` model supports (custom fields cap at 15, no child rows). We follow the `customer` v2 pattern: purpose-built relational tables with a hand-written store + controller mirroring `CRMOps`.

**AD-2 — Inventory is its own shared domain module, not owned by Sales Order.**
`inventory_item`/`inventory_stock`/`inventory_allocation` + `lkp_unit`/`lkp_warehouse`/`lkp_tax_rate` are shared entities. Purchase Orders, Invoices, GRN, and Manufacturing will consume them. They get their own RBAC resource, store, and controller. Sales Order *references* them.

**AD-3 — Hybrid PK everywhere (`SERIAL` + `UUID`).**
Internal `*_id SERIAL` PK for FK joins; external `*_uuid UUID` for API ids (non-enumerable). Matches `customer`. All API paths use the UUID; all FKs use the serial.

**AD-4 — Snapshot billing/shipping onto `sales_order`; snapshot item data onto `sales_order_item`.**
There is no normalized address/contact model. Copying the customer's billing/shipping block (and each line's SKU/name/description/unit/price/tax/discount) onto the order at creation preserves historical accuracy when master data later changes — the standard NetSuite/SAP B1 approach. `sales_order` still keeps `customer_id` FK for "current customer" navigation.

**AD-5 — Store computed money on rows, derive inventory quantities live.**
Money totals (`line_*`, header `subtotal/discount_total/tax_total/grand_total`) are **stored** — recomputing per query is wasteful and snapshots must be immutable once frozen. Inventory `available`/`allocated` quantities are **derived** (`on_hand − Σ active allocations`) because they change on every allocation and must be real-time correct; a cached column would drift. (An optional materialized cache is deferred to §13.)

**AD-6 — Status transitions enforced in the Go service layer over the shared lookup.**
Reuse `lkp_record_status` (already seeded for SORD). Legality of moves is a static Go transition map (mirrors how the relational `customer` store uses `crmCodeRank`), not a `workflow_transitions` table.

**AD-7 — Order number `SORD-000001` generated in Go post-insert** (mirrors `customer_doc_num`): insert row → get `SERIAL` id → `UPDATE ... SET sales_order_number = 'SORD-' || lpad(id::text, 6, '0')`. UUID is the public id; number is the human-readable business id.

---

## 3. New Tables — Per-Table Justification

For each new table: **(a)** why an existing table can't be reused, **(b)** why it's necessary, **(c)** how it relates to the existing schema.

### `lkp_unit` (Unit of Measure)
- **(a)** No unit/UOM table exists anywhere (grep-confirmed absent).
- **(b)** Line items and inventory need a controlled UOM list (EA, BOX, SQFT, SLAB, LFT…), especially for the stone industry.
- **(c)** New reference table in the `lkp_*` family; referenced by `inventory_item.inventory_item_unit_id` and `sales_order_item.unit_id`.

### `lkp_warehouse`
- **(a)** No warehouse/location concept exists.
- **(b)** Stock, allocations, and fulfillment are per-location. Needed even for a single default warehouse.
- **(c)** New reference table; `warehouse_state_id`/`warehouse_country_id` reuse `lkp_state`/`lkp_country`. Referenced by `inventory_stock`, `inventory_allocation`, `sales_order_item.warehouse_id`.

### `lkp_tax_rate`
- **(a)** No tax-rate lookup exists — tax lives only as flat `customer_sales_tax_percent` and exemption flags on `customer`.
- **(b)** Reusable named tax rates (jurisdiction/rate) applied per line and defaulted on the header; supports future tax reporting.
- **(c)** New reference table; referenced by `inventory_item.inventory_item_tax_rate_id` and `sales_order_item.tax_rate_id`. Complements existing customer exemption fields.

### `inventory_item`
- **(a)** No product/item/SKU table exists.
- **(b)** The catalog of sellable items every order line references.
- **(c)** New master (hybrid PK). FKs to `lkp_unit`, `lkp_tax_rate`, `lkp_currency`. Has its own `custom_fields` JSONB governed by `workflow_field_definitions` (same pattern as `customer`).

### `inventory_stock`
- **(a)** No stock/on-hand table exists.
- **(b)** Tracks physical on-hand quantity per item **per warehouse** (one row per item×warehouse).
- **(c)** New child of `inventory_item` (+ `lkp_warehouse`). `available`/`allocated` are derived from `inventory_allocation`, not stored here.

### `inventory_allocation`
- **(a)** No reservation/allocation concept exists.
- **(b)** Reserves stock against a specific order line without decrementing on-hand — enables partial fulfillment, multi-warehouse picks, and future picking/packing workflows. Kept **separate** from `inventory_stock` (per review rec #6).
- **(c)** New table linking `inventory_item` + `lkp_warehouse` + `sales_order` + `sales_order_item`.

### `sales_order`
- **(a)** The v1 JSONB `sales_order` workflow can't hold line items, stored money, or address snapshots. `customer` can't be reused — it models a party, not a transaction, and its `Store` bakes in CRM convert/approve semantics.
- **(b)** The order header: identity, classification, billing/shipping snapshots, terms, money totals.
- **(c)** New master (hybrid PK, sibling of `customer`). FKs to `customer`, `employee`, `lkp_record_type`(=SORD), `lkp_record_status`, `lkp_payment_terms`, `lkp_price_level`, `lkp_currency`, `lkp_state`, `lkp_country`.

### `sales_order_item`
- **(a)** Line items are a distinct 1-to-many child; no such table exists.
- **(b)** Ordered lines with item snapshots + stored per-line money.
- **(c)** New child of `sales_order`; FKs to `inventory_item`, `lkp_unit`, `lkp_tax_rate`, `lkp_warehouse`. Nullable `inventory_item_id` permits free-text/non-catalog lines.

### `sales_order_history`
- **(a)** Generic `audit_logs` is fine for CRUD, but the status lifecycle deserves a typed from/to trail like `customer_history`.
- **(b)** One row per status change (create/transition/cancel) with `from_status_id`/`to_status_id` + JSONB snapshot.
- **(c)** New child of `sales_order`, mirrors `customer_history` shape exactly; FKs to `lkp_record_status`, `employee`.

**Extended, not created:** `authz/catalog.go` gains an `inventory_item` resource (the `sales_order` resource already exists). No existing table is altered destructively.

---

## 4. ER Diagram (text)

```
                          ┌─────────────────────────── EXISTING (reused) ───────────────────────────┐
   lkp_currency          customer            employee           lkp_record_type     lkp_record_status
        │(1)               │(1)                 │(1)                  │(1)                 │(1)
        │                  │                    │                    │                    │
        │        ┌─────────┼────────────────────┼────────────────────┼────────────────────┤
        │        │         │ bill customer      │ created_by /        │ record_type=SORD   │ status
        │        │         │                    │ sales_rep / owner   │                    │
        ▼(N)     ▼(N)      ▼(N)                                                            ▼
 ┌──────────────────────────────────────────────────────────────────────────────────────────┐
 │                                    sales_order  (header, NEW)                              │
 │  PK sales_order_id (serial) · UUID sales_order_uuid · sales_order_number "SORD-000001"     │
 │  FK customer_id · record_type · sales_order_status · payment/price_level/currency          │
 │  billing snapshot · shipping snapshot · money totals (subtotal…grand_total)                │
 └───────────┬───────────────────────────────────────────────┬───────────────────────────────┘
             │(1)                                             │(1)
             │                                                │
     (N)     ▼                                         (N)    ▼
 ┌────────────────────────────┐                  ┌─────────────────────────────┐
 │  sales_order_item  (NEW)   │                  │  sales_order_history  (NEW) │
 │  PK sales_order_item_id    │                  │  from_status_id/to_status_id│
 │  FK sales_order_id         │                  │  actor_employee_id·snapshot │
 │  FK inventory_item_id ─────┼───┐              └─────────────────────────────┘
 │  item snapshots · line $   │   │
 │  FK unit_id·tax_rate_id·wh │   │
 └──────────┬─────────────────┘   │
            │(1)                   │(N)
            │                      ▼
     (N)    ▼            ┌──────────────────────────── INVENTORY DOMAIN (NEW, shared) ──────────────┐
 ┌───────────────────────────┐   ┌───────────────────────────┐   ┌──────────────────────────────┐
 │  inventory_allocation     │   │      inventory_item       │   │       inventory_stock         │
 │  PK …_id · UUID           │   │  PK inventory_item_id     │(1)│  PK inventory_stock_id        │
 │  FK inventory_item_id ────┼──▶│  UUID · sku · name · desc │──▶│  FK inventory_item_id         │
 │  FK warehouse_id          │   │  FK unit_id·tax_rate_id   │(N)│  FK warehouse_id              │
 │  FK sales_order_id        │   │  unit_price·custom_fields │   │  quantity_on_hand·reorder_pt  │
 │  FK sales_order_item_id   │   └─────────┬────────┬────────┘   │  UNIQUE(item_id, warehouse_id)│
 │  allocated/fulfilled qty  │             │        │            └───────────────┬───────────────┘
 └──────────┬────────────────┘             │(N)     │(N)                         │(N)
            │(N)                            ▼        ▼                            ▼
            └───────────────────────▶ lkp_warehouse   lkp_unit  lkp_tax_rate  (NEW reference, lkp_* family)
                                            │(N)         (reused: lkp_state, lkp_country on warehouse)
                                            ▼
                                     lkp_state / lkp_country
```

**Cardinality summary**

- `customer` 1 ─── N `sales_order` (one customer, many orders).
- `sales_order` 1 ─── N `sales_order_item` (one order, many lines).
- `sales_order` 1 ─── N `sales_order_history`.
- `inventory_item` 1 ─── N `sales_order_item` (one item on many orders).
- `inventory_item` 1 ─── N `inventory_stock` (one row per warehouse).
- `sales_order_item` 1 ─── N `inventory_allocation` (a line may allocate across warehouses).
- `inventory_item` / `lkp_warehouse` 1 ─── N `inventory_allocation`.

---

## 5. SQL — CREATE TABLE Statements

> Illustrative migration content. Final DDL is appended to `database/migrations/tenant/schema.sql` via the **add-migration** skill (idempotent, non-destructive, `IF NOT EXISTS` everywhere). Numeric types standardized: **money `DECIMAL(15,2)`**, **quantity `DECIMAL(14,3)`**, **percent `DECIMAL(6,4)`**, **exchange rate `DECIMAL(18,6)`** (money/percent match existing `customer` columns).

### 5.1 Inventory reference lookups

```sql
-- ── lkp_unit ────────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS lkp_unit (
    unit_id             SERIAL       PRIMARY KEY,
    unit_name           VARCHAR(50)  NOT NULL,
    unit_code           VARCHAR(10)  NOT NULL,
    unit_category       VARCHAR(20)  NOT NULL DEFAULT 'count', -- count|length|area|volume|weight
    unit_is_active      BOOLEAN      NOT NULL DEFAULT TRUE,
    unit_is_system      BOOLEAN      NOT NULL DEFAULT FALSE,
    unit_created_at     TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    unit_created_by     INTEGER      NOT NULL REFERENCES employee(employee_id),
    unit_deleted_at     TIMESTAMP        NULL,
    unit_deleted_by     INTEGER          NULL REFERENCES employee(employee_id),
    unit_record_version INTEGER      NOT NULL DEFAULT 1,
    CONSTRAINT uq_unit_code UNIQUE (unit_code),
    CONSTRAINT chk_unit_category CHECK (unit_category IN ('count','length','area','volume','weight'))
);

INSERT INTO lkp_unit (unit_name, unit_code, unit_category, unit_is_system, unit_created_by) VALUES
    ('Each','EA','count',TRUE,1), ('Box','BOX','count',TRUE,1), ('Set','SET','count',TRUE,1),
    ('Pallet','PLT','count',TRUE,1), ('Slab','SLAB','count',TRUE,1),
    ('Square Foot','SQFT','area',TRUE,1), ('Square Meter','SQM','area',TRUE,1),
    ('Linear Foot','LFT','length',TRUE,1), ('Kilogram','KG','weight',TRUE,1), ('Pound','LB','weight',TRUE,1)
ON CONFLICT (unit_code) DO NOTHING;

-- ── lkp_warehouse ───────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS lkp_warehouse (
    warehouse_id            SERIAL       PRIMARY KEY,
    warehouse_uuid          UUID         NOT NULL DEFAULT gen_random_uuid(),
    warehouse_name          VARCHAR(100) NOT NULL,
    warehouse_code          VARCHAR(20)  NOT NULL,
    warehouse_addr_line1    VARCHAR(100) NOT NULL DEFAULT '',
    warehouse_addr_line2    VARCHAR(100) NOT NULL DEFAULT '',
    warehouse_addr_city     VARCHAR(100) NOT NULL DEFAULT '',
    warehouse_addr_state    INTEGER          NULL REFERENCES lkp_state(state_id),
    warehouse_addr_zip      VARCHAR(10)  NOT NULL DEFAULT '',
    warehouse_addr_country  INTEGER          NULL REFERENCES lkp_country(country_id),
    warehouse_is_default    BOOLEAN      NOT NULL DEFAULT FALSE,
    warehouse_is_active     BOOLEAN      NOT NULL DEFAULT TRUE,
    warehouse_is_system     BOOLEAN      NOT NULL DEFAULT FALSE,
    warehouse_created_at    TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    warehouse_created_by    INTEGER      NOT NULL REFERENCES employee(employee_id),
    warehouse_deleted_at    TIMESTAMP        NULL,
    warehouse_deleted_by    INTEGER          NULL REFERENCES employee(employee_id),
    warehouse_record_version INTEGER     NOT NULL DEFAULT 1,
    CONSTRAINT uq_warehouse_code UNIQUE (warehouse_code),
    CONSTRAINT uq_warehouse_uuid UNIQUE (warehouse_uuid)
);
-- At most one default warehouse
CREATE UNIQUE INDEX IF NOT EXISTS uq_warehouse_default
    ON lkp_warehouse (warehouse_is_default) WHERE warehouse_is_default = TRUE;

INSERT INTO lkp_warehouse (warehouse_name, warehouse_code, warehouse_is_default, warehouse_is_system, warehouse_created_by) VALUES
    ('Main Warehouse','MAIN',TRUE,TRUE,1)
ON CONFLICT (warehouse_code) DO NOTHING;

-- ── lkp_tax_rate ────────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS lkp_tax_rate (
    tax_rate_id            SERIAL       PRIMARY KEY,
    tax_rate_name          VARCHAR(50)  NOT NULL,
    tax_rate_code          VARCHAR(20)  NOT NULL,
    tax_rate_percent       DECIMAL(6,4) NOT NULL DEFAULT 0,
    tax_rate_jurisdiction  VARCHAR(100) NOT NULL DEFAULT '',
    tax_rate_is_active     BOOLEAN      NOT NULL DEFAULT TRUE,
    tax_rate_is_system     BOOLEAN      NOT NULL DEFAULT FALSE,
    tax_rate_created_at    TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    tax_rate_created_by    INTEGER      NOT NULL REFERENCES employee(employee_id),
    tax_rate_deleted_at    TIMESTAMP        NULL,
    tax_rate_deleted_by    INTEGER          NULL REFERENCES employee(employee_id),
    tax_rate_record_version INTEGER     NOT NULL DEFAULT 1,
    CONSTRAINT uq_tax_rate_code UNIQUE (tax_rate_code),
    CONSTRAINT chk_tax_rate_percent CHECK (tax_rate_percent >= 0 AND tax_rate_percent <= 100)
);

INSERT INTO lkp_tax_rate (tax_rate_name, tax_rate_code, tax_rate_percent, tax_rate_is_system, tax_rate_created_by) VALUES
    ('No Tax','NONE',0,TRUE,1)
ON CONFLICT (tax_rate_code) DO NOTHING;
```

### 5.2 Inventory domain

```sql
-- ── inventory_item ──────────────────────────────────────────────────────────
CREATE TABLE IF NOT EXISTS inventory_item (
    inventory_item_id            SERIAL        PRIMARY KEY,
    inventory_item_uuid          UUID          NOT NULL DEFAULT gen_random_uuid(),
    inventory_item_sku           VARCHAR(50)   NOT NULL,
    inventory_item_name          VARCHAR(150)  NOT NULL,
    inventory_item_description    TEXT          NOT NULL DEFAULT '',
    inventory_item_unit_id       INTEGER       NOT NULL REFERENCES lkp_unit(unit_id),
    inventory_item_unit_price    DECIMAL(15,2) NOT NULL DEFAULT 0,
    inventory_item_currency_id   INTEGER           NULL REFERENCES lkp_currency(currency_id),
    inventory_item_tax_rate_id   INTEGER           NULL REFERENCES lkp_tax_rate(tax_rate_id),
    inventory_item_is_active     BOOLEAN       NOT NULL DEFAULT TRUE,
    inventory_item_custom_fields JSONB         NOT NULL DEFAULT '{}',
    inventory_item_created_at    TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    inventory_item_created_by    INTEGER           NULL REFERENCES employee(employee_id),
    inventory_item_updated_at    TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    inventory_item_updated_by    INTEGER           NULL REFERENCES employee(employee_id),
    inventory_item_deleted_at    TIMESTAMP         NULL,
    inventory_item_deleted_by    INTEGER           NULL REFERENCES employee(employee_id),
    inventory_item_record_version INTEGER      NOT NULL DEFAULT 1,
    CONSTRAINT uq_inventory_item_uuid UNIQUE (inventory_item_uuid),
    CONSTRAINT chk_inventory_item_unit_price CHECK (inventory_item_unit_price >= 0),
    CONSTRAINT chk_inventory_item_soft_delete CHECK (
        (inventory_item_deleted_at IS NULL AND inventory_item_deleted_by IS NULL) OR
        (inventory_item_deleted_at IS NOT NULL AND inventory_item_deleted_by IS NOT NULL)
    )
);
-- SKU unique among live rows only (case-insensitive), so a SKU can be reused after soft delete
CREATE UNIQUE INDEX IF NOT EXISTS uq_inventory_item_sku_active
    ON inventory_item (LOWER(inventory_item_sku)) WHERE inventory_item_deleted_at IS NULL;

-- ── inventory_stock  (one row per item × warehouse) ─────────────────────────
CREATE TABLE IF NOT EXISTS inventory_stock (
    inventory_stock_id      SERIAL        PRIMARY KEY,
    inventory_item_id       INTEGER       NOT NULL REFERENCES inventory_item(inventory_item_id) ON DELETE CASCADE,
    warehouse_id            INTEGER       NOT NULL REFERENCES lkp_warehouse(warehouse_id),
    quantity_on_hand        DECIMAL(14,3) NOT NULL DEFAULT 0,
    reorder_point           DECIMAL(14,3) NOT NULL DEFAULT 0,
    stock_created_at        TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    stock_updated_at        TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    stock_record_version    INTEGER       NOT NULL DEFAULT 1,
    CONSTRAINT uq_inventory_stock_item_wh UNIQUE (inventory_item_id, warehouse_id),
    CONSTRAINT chk_inventory_stock_on_hand CHECK (quantity_on_hand >= 0)
);

-- ── inventory_allocation  (reservation per order line, not owned by SO module) ─
CREATE TABLE IF NOT EXISTS inventory_allocation (
    inventory_allocation_id   SERIAL        PRIMARY KEY,
    inventory_allocation_uuid UUID          NOT NULL DEFAULT gen_random_uuid(),
    inventory_item_id         INTEGER       NOT NULL REFERENCES inventory_item(inventory_item_id),
    warehouse_id              INTEGER       NOT NULL REFERENCES lkp_warehouse(warehouse_id),
    sales_order_id            INTEGER       NOT NULL REFERENCES sales_order(sales_order_id) ON DELETE CASCADE,
    sales_order_item_id       INTEGER       NOT NULL REFERENCES sales_order_item(sales_order_item_id) ON DELETE CASCADE,
    allocated_quantity        DECIMAL(14,3) NOT NULL DEFAULT 0,
    fulfilled_quantity        DECIMAL(14,3) NOT NULL DEFAULT 0,
    allocation_status         VARCHAR(20)   NOT NULL DEFAULT 'reserved', -- reserved|partially_fulfilled|fulfilled|released
    allocation_created_at     TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    allocation_created_by     INTEGER           NULL REFERENCES employee(employee_id),
    allocation_updated_at     TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    allocation_record_version INTEGER       NOT NULL DEFAULT 1,
    CONSTRAINT uq_inventory_allocation_uuid UNIQUE (inventory_allocation_uuid),
    CONSTRAINT chk_alloc_qty        CHECK (allocated_quantity >= 0),
    CONSTRAINT chk_alloc_fulfilled  CHECK (fulfilled_quantity >= 0 AND fulfilled_quantity <= allocated_quantity),
    CONSTRAINT chk_alloc_status     CHECK (allocation_status IN ('reserved','partially_fulfilled','fulfilled','released'))
);
```

### 5.3 Sales Order header

```sql
-- ── sales_order  (header — sibling of `customer`) ───────────────────────────
CREATE TABLE IF NOT EXISTS sales_order (
    sales_order_id                SERIAL        PRIMARY KEY,
    sales_order_uuid              UUID          NOT NULL DEFAULT gen_random_uuid(),
    ss_customer_id                INTEGER           NULL,  -- platform owner stamp, no cross-DB FK (matches customer)
    sales_order_number            VARCHAR(20)       NULL,  -- 'SORD-000001', generated post-insert in Go

    -- Classification (reused lookups)
    record_type                   INTEGER       NOT NULL REFERENCES lkp_record_type(record_type_id),   -- = SORD
    sales_order_status            INTEGER       NOT NULL REFERENCES lkp_record_status(record_status_id),

    -- Primary info
    sales_order_customer_id       INTEGER       NOT NULL REFERENCES customer(customer_id),
    sales_order_po_number         VARCHAR(50)   NOT NULL DEFAULT '',   -- customer's Purchase Order Number
    sales_order_reference_number  VARCHAR(50)   NOT NULL DEFAULT '',   -- generic external reference (future-proof)
    sales_order_date              DATE          NOT NULL DEFAULT CURRENT_DATE,  -- business "Date Created"
    sales_order_expected_delivery DATE              NULL,
    sales_order_sales_tax_percent DECIMAL(6,4)  NOT NULL DEFAULT 0,    -- header default tax %
    sales_order_memo              TEXT          NOT NULL DEFAULT '',
    sales_order_notes             TEXT          NOT NULL DEFAULT '',
    sales_order_internal_notes    TEXT          NOT NULL DEFAULT '',
    sales_order_terms_conditions  TEXT          NOT NULL DEFAULT '',

    -- Sales assignment
    sales_order_sales_rep_id      INTEGER           NULL REFERENCES employee(employee_id),
    sales_order_owner_id          INTEGER           NULL REFERENCES employee(employee_id),

    -- Terms / pricing / currency
    sales_order_payment_terms     INTEGER           NULL REFERENCES lkp_payment_terms(payment_terms_id),
    sales_order_price_level       INTEGER           NULL REFERENCES lkp_price_level(price_level_id),
    sales_order_currency          INTEGER           NULL REFERENCES lkp_currency(currency_id),
    sales_order_exchange_rate     DECIMAL(18,6) NOT NULL DEFAULT 1,

    -- Money summary (stored)
    sales_order_subtotal          DECIMAL(15,2) NOT NULL DEFAULT 0,
    sales_order_discount_total    DECIMAL(15,2) NOT NULL DEFAULT 0,
    sales_order_tax_total         DECIMAL(15,2) NOT NULL DEFAULT 0,
    sales_order_shipping_charge   DECIMAL(15,2) NOT NULL DEFAULT 0,
    sales_order_adjustment        DECIMAL(15,2) NOT NULL DEFAULT 0,
    sales_order_grand_total       DECIMAL(15,2) NOT NULL DEFAULT 0,

    -- Billing snapshot
    sales_order_bill_customer_name VARCHAR(150) NOT NULL DEFAULT '',
    sales_order_bill_attention     VARCHAR(150) NOT NULL DEFAULT '',
    sales_order_bill_addr_line1    VARCHAR(100) NOT NULL DEFAULT '',
    sales_order_bill_addr_line2    VARCHAR(100) NOT NULL DEFAULT '',
    sales_order_bill_addr_suitenum VARCHAR(20)  NOT NULL DEFAULT '',
    sales_order_bill_addr_city     VARCHAR(100) NOT NULL DEFAULT '',
    sales_order_bill_addr_state    INTEGER          NULL REFERENCES lkp_state(state_id),
    sales_order_bill_addr_zip      VARCHAR(10)  NOT NULL DEFAULT '',
    sales_order_bill_addr_country  INTEGER          NULL REFERENCES lkp_country(country_id),
    sales_order_bill_phone         VARCHAR(20)  NOT NULL DEFAULT '',
    sales_order_bill_fax           VARCHAR(20)  NOT NULL DEFAULT '',
    sales_order_bill_email         VARCHAR(100) NOT NULL DEFAULT '',

    -- Shipping snapshot
    sales_order_ship_same_as_bill  BOOLEAN      NOT NULL DEFAULT FALSE,
    sales_order_ship_customer_name VARCHAR(150) NOT NULL DEFAULT '',
    sales_order_ship_attention     VARCHAR(150) NOT NULL DEFAULT '',
    sales_order_ship_addr_line1    VARCHAR(100) NOT NULL DEFAULT '',
    sales_order_ship_addr_line2    VARCHAR(100) NOT NULL DEFAULT '',
    sales_order_ship_addr_suitenum VARCHAR(20)  NOT NULL DEFAULT '',
    sales_order_ship_addr_city     VARCHAR(100) NOT NULL DEFAULT '',
    sales_order_ship_addr_state    INTEGER          NULL REFERENCES lkp_state(state_id),
    sales_order_ship_addr_zip      VARCHAR(10)  NOT NULL DEFAULT '',
    sales_order_ship_addr_country  INTEGER          NULL REFERENCES lkp_country(country_id),
    sales_order_ship_phone         VARCHAR(20)  NOT NULL DEFAULT '',
    sales_order_ship_fax           VARCHAR(20)  NOT NULL DEFAULT '',
    sales_order_ship_email         VARCHAR(100) NOT NULL DEFAULT '',

    -- Dynamic + lineage + audit
    sales_order_custom_fields      JSONB        NOT NULL DEFAULT '{}',
    sales_order_parent_id          INTEGER          NULL REFERENCES sales_order(sales_order_id),
    sales_order_created_at         TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    sales_order_created_by         INTEGER          NULL REFERENCES employee(employee_id),
    sales_order_updated_at         TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    sales_order_updated_by         INTEGER          NULL REFERENCES employee(employee_id),
    sales_order_deleted_at         TIMESTAMP        NULL,
    sales_order_deleted_by         INTEGER          NULL REFERENCES employee(employee_id),
    sales_order_record_version     INTEGER      NOT NULL DEFAULT 1,

    CONSTRAINT uq_sales_order_uuid   UNIQUE (sales_order_uuid),
    CONSTRAINT uq_sales_order_number UNIQUE (sales_order_number),
    CONSTRAINT chk_so_tax_percent    CHECK (sales_order_sales_tax_percent >= 0 AND sales_order_sales_tax_percent <= 100),
    CONSTRAINT chk_so_totals_nonneg  CHECK (sales_order_subtotal >= 0 AND sales_order_grand_total >= 0),
    CONSTRAINT chk_so_soft_delete    CHECK (
        (sales_order_deleted_at IS NULL AND sales_order_deleted_by IS NULL) OR
        (sales_order_deleted_at IS NOT NULL AND sales_order_deleted_by IS NOT NULL)
    )
);
```

### 5.4 Sales Order line items + history

```sql
-- ── sales_order_item  (child; relaxed column naming per customer_history precedent) ─
CREATE TABLE IF NOT EXISTS sales_order_item (
    sales_order_item_id     SERIAL        PRIMARY KEY,
    sales_order_item_uuid   UUID          NOT NULL DEFAULT gen_random_uuid(),
    sales_order_id          INTEGER       NOT NULL REFERENCES sales_order(sales_order_id) ON DELETE CASCADE,
    line_number             INTEGER       NOT NULL,
    inventory_item_id       INTEGER           NULL REFERENCES inventory_item(inventory_item_id), -- NULL = free-text line
    warehouse_id            INTEGER           NULL REFERENCES lkp_warehouse(warehouse_id),

    -- Snapshots (frozen at add time)
    item_name               VARCHAR(150)  NOT NULL DEFAULT '',
    sku                     VARCHAR(50)   NOT NULL DEFAULT '',
    description             TEXT          NOT NULL DEFAULT '',
    unit_id                 INTEGER           NULL REFERENCES lkp_unit(unit_id),
    unit_code               VARCHAR(10)   NOT NULL DEFAULT '',
    quantity                DECIMAL(14,3) NOT NULL DEFAULT 0,
    unit_price              DECIMAL(15,2) NOT NULL DEFAULT 0,
    discount_percent        DECIMAL(6,4)  NOT NULL DEFAULT 0,
    tax_rate_id             INTEGER           NULL REFERENCES lkp_tax_rate(tax_rate_id),
    tax_percent             DECIMAL(6,4)  NOT NULL DEFAULT 0,

    -- Stored line money
    line_subtotal           DECIMAL(15,2) NOT NULL DEFAULT 0,
    line_discount           DECIMAL(15,2) NOT NULL DEFAULT 0,
    line_tax                DECIMAL(15,2) NOT NULL DEFAULT 0,
    line_total              DECIMAL(15,2) NOT NULL DEFAULT 0,

    item_created_at         TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    item_created_by         INTEGER           NULL REFERENCES employee(employee_id),
    item_updated_at         TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    item_deleted_at         TIMESTAMP         NULL,
    item_record_version     INTEGER       NOT NULL DEFAULT 1,
    CONSTRAINT uq_sales_order_item_uuid UNIQUE (sales_order_item_uuid),
    CONSTRAINT uq_soi_line              UNIQUE (sales_order_id, line_number),
    CONSTRAINT chk_soi_qty              CHECK (quantity >= 0),
    CONSTRAINT chk_soi_unit_price       CHECK (unit_price >= 0),
    CONSTRAINT chk_soi_discount         CHECK (discount_percent >= 0 AND discount_percent <= 100),
    CONSTRAINT chk_soi_tax              CHECK (tax_percent >= 0 AND tax_percent <= 100)
);

-- ── sales_order_history  (mirrors customer_history) ─────────────────────────
CREATE TABLE IF NOT EXISTS sales_order_history (
    sales_order_history_id  SERIAL       PRIMARY KEY,
    sales_order_id          INTEGER      NOT NULL REFERENCES sales_order(sales_order_id) ON DELETE CASCADE,
    from_status_id          INTEGER          NULL REFERENCES lkp_record_status(record_status_id),
    to_status_id            INTEGER          NULL REFERENCES lkp_record_status(record_status_id),
    action                  VARCHAR(32)  NOT NULL DEFAULT 'transition', -- create | transition | cancel | update
    actor_employee_id       INTEGER          NULL REFERENCES employee(employee_id),
    snapshot                JSONB        NOT NULL DEFAULT '{}',
    at                      TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP
);
```

---

## 6. Recommended Indexes

```sql
-- sales_order (listing/filtering — all partial on live rows)
CREATE INDEX IF NOT EXISTS idx_so_customer   ON sales_order (sales_order_customer_id) WHERE sales_order_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_so_status     ON sales_order (sales_order_status)      WHERE sales_order_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_so_date       ON sales_order (sales_order_date)        WHERE sales_order_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_so_sales_rep  ON sales_order (sales_order_sales_rep_id) WHERE sales_order_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_so_owner      ON sales_order (sales_order_owner_id)     WHERE sales_order_deleted_at IS NULL;
-- Keyset pagination tiebreaker (created_at, id) — matches query/ default sort
CREATE INDEX IF NOT EXISTS idx_so_created    ON sales_order (sales_order_created_at, sales_order_id) WHERE sales_order_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_so_custom_gin ON sales_order USING GIN (sales_order_custom_fields);

-- sales_order_item
CREATE INDEX IF NOT EXISTS idx_soi_order ON sales_order_item (sales_order_id) WHERE item_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_soi_item  ON sales_order_item (inventory_item_id);

-- sales_order_history
CREATE INDEX IF NOT EXISTS idx_so_history_order ON sales_order_history (sales_order_id);

-- inventory
CREATE INDEX IF NOT EXISTS idx_inv_item_active ON inventory_item (inventory_item_is_active) WHERE inventory_item_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_inv_item_gin    ON inventory_item USING GIN (inventory_item_custom_fields);
CREATE INDEX IF NOT EXISTS idx_inv_stock_wh    ON inventory_stock (warehouse_id);
CREATE INDEX IF NOT EXISTS idx_alloc_item      ON inventory_allocation (inventory_item_id);
CREATE INDEX IF NOT EXISTS idx_alloc_item_wh   ON inventory_allocation (inventory_item_id, warehouse_id);
CREATE INDEX IF NOT EXISTS idx_alloc_order     ON inventory_allocation (sales_order_id);
CREATE INDEX IF NOT EXISTS idx_alloc_line      ON inventory_allocation (sales_order_item_id);
-- Partial index for the "available/allocated" aggregation (open reservations only)
CREATE INDEX IF NOT EXISTS idx_alloc_open      ON inventory_allocation (inventory_item_id, warehouse_id)
    WHERE allocation_status IN ('reserved','partially_fulfilled');
```

> **Migration ordering note:** `inventory_allocation` FKs `sales_order` and `sales_order_item`, which FK nothing in inventory — so create order is: lookups → `inventory_item` → `inventory_stock` → `sales_order` → `sales_order_item` → `inventory_allocation` → `sales_order_history`.

---

## 7. Foreign Key Relationships (explained)

| Child column | → Parent | Meaning | On delete |
|---|---|---|---|
| `sales_order.sales_order_customer_id` | `customer.customer_id` | The billing customer this order is for | RESTRICT (default) — can't delete a customer with orders |
| `sales_order.record_type` | `lkp_record_type.record_type_id` | Always the `SORD` row | RESTRICT |
| `sales_order.sales_order_status` | `lkp_record_status.record_status_id` | Current lifecycle status (SORD set) | RESTRICT |
| `sales_order.sales_order_payment_terms` | `lkp_payment_terms.payment_terms_id` | Terms (defaulted from customer) | RESTRICT |
| `sales_order.sales_order_price_level` | `lkp_price_level.price_level_id` | Pricing tier | RESTRICT |
| `sales_order.sales_order_currency` | `lkp_currency.currency_id` | Order currency | RESTRICT |
| `sales_order.sales_order_sales_rep_id` / `_owner_id` | `employee.employee_id` | Sales rep / customer owner | RESTRICT |
| `sales_order.sales_order_bill_addr_state` / `_ship_addr_state` | `lkp_state.state_id` | Snapshot address state | RESTRICT |
| `sales_order.sales_order_bill_addr_country` / `_ship_addr_country` | `lkp_country.country_id` | Snapshot address country | RESTRICT |
| `sales_order.sales_order_parent_id` | `sales_order.sales_order_id` | Self-ref for amendments/duplicates | RESTRICT |
| `sales_order.*_created_by/_updated_by/_deleted_by` | `employee.employee_id` | Audit actors | RESTRICT |
| `sales_order_item.sales_order_id` | `sales_order.sales_order_id` | Owning order | **CASCADE** (lines die with order) |
| `sales_order_item.inventory_item_id` | `inventory_item.inventory_item_id` | Catalog item (nullable for free-text) | RESTRICT |
| `sales_order_item.unit_id` / `tax_rate_id` / `warehouse_id` | `lkp_unit` / `lkp_tax_rate` / `lkp_warehouse` | Snapshot refs | RESTRICT |
| `sales_order_history.sales_order_id` | `sales_order.sales_order_id` | Owning order | **CASCADE** |
| `sales_order_history.from_status_id` / `to_status_id` | `lkp_record_status.record_status_id` | Transition endpoints | RESTRICT |
| `inventory_stock.inventory_item_id` | `inventory_item.inventory_item_id` | Item stocked | **CASCADE** |
| `inventory_stock.warehouse_id` | `lkp_warehouse.warehouse_id` | Location | RESTRICT |
| `inventory_allocation.inventory_item_id` / `warehouse_id` | `inventory_item` / `lkp_warehouse` | What/where reserved | RESTRICT |
| `inventory_allocation.sales_order_id` / `sales_order_item_id` | `sales_order` / `sales_order_item` | Demand source | **CASCADE** |
| `inventory_item.inventory_item_unit_id` | `lkp_unit.unit_id` | Base UOM | RESTRICT |
| `inventory_item.inventory_item_tax_rate_id` / `currency_id` | `lkp_tax_rate` / `lkp_currency` | Default tax / currency | RESTRICT |

> No cross-database FKs (control-plane `identities` is referenced only via denormalized `employee`/`users` in the tenant DB). No `tenant_id` anywhere.

---

## 8. Status Transition Rules (service layer)

Statuses come from `lkp_record_status` where `record_status_record_type = 6` (SORD). Legality is enforced by a static Go map (no `workflow_transitions` table), mirroring the relational customer store's `crmCodeRank` approach. Denied transitions → HTTP 409.

```
DRFT (Draft) ──────▶ PAPV (Pending Approval) ──────▶ APPV (Approved) ──────▶ OPEN
   │                     │                                                     │
   │                     └──▶ DRFT (reject back to draft)                      ▼
   │                                                          PART (Partially Filled)
   ▼                                                                          │
 CANC (Cancelled) ◀───────── from DRFT / PAPV / APPV / OPEN / PART            ▼
                                                                        FILL (Filled)  [terminal]
 CANC [terminal]
```

```go
// allowedSOTransitions[fromCode] = set of reachable toCodes
var allowedSOTransitions = map[string]map[string]bool{
    "DRFT": {"PAPV": true, "CANC": true},
    "PAPV": {"APPV": true, "DRFT": true, "CANC": true},
    "APPV": {"OPEN": true, "CANC": true},
    "OPEN": {"PART": true, "FILL": true, "CANC": true},
    "PART": {"FILL": true, "CANC": true},
    "FILL": {},   // terminal
    "CANC": {},   // terminal
}
```

- New orders start at **`DRFT`**.
- `OPEN → PART → FILL` are normally driven by fulfillment (allocation `fulfilled_quantity` reaching `allocated_quantity` across lines), but manual transition is allowed for operators.
- Cancelling **releases** open allocations (`allocation_status = 'released'`) so reserved stock returns to available.

---

## 9. Money & Inventory Calculation Rules

**Per line (`sales_order_item`, stored):**
```
line_subtotal = round(quantity * unit_price, 2)
line_discount = round(line_subtotal * discount_percent / 100, 2)
line_tax      = round((line_subtotal - line_discount) * tax_percent / 100, 2)
line_total    = line_subtotal - line_discount + line_tax
```
`tax_percent` is snapshotted from the line's `tax_rate_id`; if a line has no tax rate, the header `sales_order_sales_tax_percent` is used as the default.

**Per header (`sales_order`, stored, recomputed on every line mutation inside the same transaction):**
```
subtotal       = Σ line_subtotal
discount_total = Σ line_discount
tax_total      = Σ line_tax
grand_total    = subtotal - discount_total + tax_total + shipping_charge + adjustment
```

**Inventory (derived, not stored — the "Inventory tab"):**
```
allocated_qty(item, wh) = Σ allocated_quantity  WHERE allocation_status IN ('reserved','partially_fulfilled')
on_hand_qty(item, wh)   = inventory_stock.quantity_on_hand
available_qty(item, wh) = on_hand_qty - allocated_qty
so_qty(item, order)     = Σ sales_order_item.quantity for that item on that order
```

---

## 10. API Contracts & Payload Examples

Routes are dedicated (not through the generic `/api/tenant/crm/{workflowKey}/records` router, which is customer-table-specific). All under `/api/tenant/`, all through `tenantChain` (`RequireAuth → rate limit → TenantResolver`), RBAC checked in-handler, IDOR-guarded, 404 on scope denial. Response envelope: `{ "success": bool, "message"?: str, ... }`.

**Sales Order**
| Method & path | Purpose | RBAC |
|---|---|---|
| `POST /api/tenant/sales-orders/search` | List: pagination, global search, column filters, sort, status filter, date range (via `query/`) | `sales_order:read` + scope |
| `GET  /api/tenant/sales-orders/{uuid}` | Get one (+ items) | `sales_order:read` + IDOR |
| `POST /api/tenant/sales-orders` | Create (header + items, snapshots + totals in one tx) | `sales_order:create` |
| `PATCH /api/tenant/sales-orders/{uuid}` | Update header/items, recompute totals | `sales_order:update` + IDOR |
| `DELETE /api/tenant/sales-orders/{uuid}` | Soft delete | `sales_order:delete` + IDOR |
| `POST /api/tenant/sales-orders/{uuid}/transition` | Status change (validated map) | `sales_order:transition` + IDOR |
| `GET  /api/tenant/sales-orders/{uuid}/inventory` | Inventory tab (on-hand/available/allocated/SO-qty per line) | `sales_order:read` + IDOR |
| `GET  /api/tenant/sales-orders/{uuid}/audit` | Audit history | `sales_order:read` + IDOR |
| `*/attachments*` | Drawings / uploaded files | reuse existing generic `/api/tenant/records/{uuid}/attachments/*` with `record_id = sales_order_uuid` |

**Inventory** (`inventory_item` RBAC resource): `POST /api/tenant/inventory/items/search`, `GET|POST|PATCH|DELETE /api/tenant/inventory/items[/{uuid}]`. Reference data (`GET /api/tenant/inventory/units`, `/warehouses`, `/tax-rates`) via a lookups endpoint like `crm_lookups`.

**Create request**
```json
POST /api/tenant/sales-orders
{
  "customerUuid": "9d0f…c2",
  "poNumber": "PO-88213",
  "orderDate": "2026-07-08",
  "expectedDelivery": "2026-07-20",
  "paymentTermsId": 3,
  "priceLevelId": 2,
  "currencyId": 1,
  "salesRepEmployeeId": 12,
  "ownerEmployeeId": 7,
  "salesTaxPercent": 8.25,
  "memo": "Rush order — call before delivery",
  "shipSameAsBilling": false,
  "billing":  { "attention": "Accounts Payable", "email": "ap@acme.com" },
  "shipping": { "customerName": "Acme Job Site", "addrLine1": "500 Quarry Rd",
                "city": "Austin", "stateId": 41, "zip": "78701", "countryId": 1 },
  "shippingCharge": 150.00,
  "adjustment": 0,
  "customFields": { "install_required": true },
  "items": [
    { "lineNumber": 1, "inventoryItemUuid": "aa11…", "quantity": 25.5,
      "unitPrice": 42.00, "discountPercent": 5, "taxRateId": 4, "warehouseId": 1 },
    { "lineNumber": 2, "description": "Custom edge polishing", "quantity": 1,
      "unitPrice": 300.00, "discountPercent": 0 }
  ]
}
```
Server snapshots billing/shipping from the customer (unless overridden), snapshots each item's `sku/name/description/unit/unit_price/tax_percent`, computes line + header money, assigns `sales_order_number`, starts at `DRFT`, all in one transaction.

**Create response `201`**
```json
{
  "success": true,
  "salesOrder": {
    "id": "6f2c…9a", "salesOrderNumber": "SORD-000042", "status": "Draft",
    "customer": { "id": "9d0f…c2", "name": "Acme Stone Co." },
    "orderDate": "2026-07-08",
    "subtotal": 1317.45, "discountTotal": 53.55, "taxTotal": 104.24,
    "shippingCharge": 150.00, "adjustment": 0, "grandTotal": 1518.14,
    "items": [ { "id": "1a…", "lineNumber": 1, "sku": "SLB-CAR-3CM", "itemName": "Carrara Slab 3cm",
                 "quantity": 25.5, "unitPrice": 42.00, "lineTotal": 1088.19 } ]
  }
}
```

**Search request**
```json
POST /api/tenant/sales-orders/search
{
  "filters": [
    { "field": "status", "op": "in", "value": ["OPEN","PART"] },
    { "field": "created_at", "op": "between", "value": ["2026-06-01","2026-07-08"] },
    { "field": "record_number", "op": "contains", "value": "SORD-0004" }
  ],
  "sort": [ { "field": "created_at", "dir": "desc" } ],
  "limit": 25,
  "cursor": null
}
```
**Search response**
```json
{ "success": true, "scope": "team",
  "records": [ /* order summaries */ ],
  "nextCursor": "eyJzIjoiY3JlYXRlZF9hdCIsImQiOiJkZXNjIiwidiI6…",
  "hasMore": true }
```

**Transition**
```json
POST /api/tenant/sales-orders/{uuid}/transition
{ "toStatusCode": "PAPV" }
→ 200 { "success": true, "salesOrder": { "status": "Pending Approval" } }
→ 409 { "success": false, "message": "Cannot move a Filled order to Draft." }
```

**Inventory tab**
```json
GET /api/tenant/sales-orders/{uuid}/inventory
{ "success": true, "items": [
  { "itemId":"aa11…","sku":"SLB-CAR-3CM","onHand":120.0,"available":94.5,
    "allocated":25.5,"salesOrderQuantity":25.5 } ] }
```

---

## 11. Listing & Query Architecture (CRM parity)

The Sales Order list **reuses the existing CRM listing stack unchanged** — the `query/` package (builder, cursor, coercion, `FieldResolver`, `InvalidFilterError`, `MaxLimit`/`DefaultLimit`), the relational store's `SearchRecords` method shape, and the controller/response conventions from `CRMOps`. **No new query engine, no separate listing implementation.** The only per-entity code is a `salesOrderResolver` (a `query.FieldResolver`, mirroring `crmstore.relationalResolver`) plus its `SearchRecords` store method (mirroring `relationalStore.SearchRecords`).

> **Two honest deviations from the literal request, because "match CRM exactly" requires them.** CRM's engine (a) uses **keyset cursor pagination, not offset/limit**, and (b) returns **no `totalCount`/`filteredCount`** (it never issues `COUNT(*)` — that is the design choice that keeps lists O(page) at millions of rows, which is this module's stated scalability goal). Reusing CRM's engine and reusing the frontend's existing table/pagination components therefore **means adopting the cursor contract**, not offset+total. Both an optional count and an optional offset mode are documented below as opt-in extensions; they are **off by default** so the frontend components remain byte-compatible with the customer/lead/prospect lists. If you want offset+`totalCount` as the *default* contract, that is a deliberate divergence from CRM — say so and I will switch it (the frontend list components would then need a matching mode).

### 11.1 Endpoints

Matches the CRM route pair (`ListRecords` GET + `SearchRecords` POST). All under `/api/tenant/` (the actual convention; there is no `/api/v1/` prefix in this codebase), all through `tenantChain` (`RequireAuth → per-tenant rate limit → TenantResolver`), RBAC `sales_order:read` + scope enforced in-handler.

| Method & path | Purpose | Body |
|---|---|---|
| `GET  /api/tenant/sales-orders` | Simple in-scope list (no filters), cursor-paginated — mirrors CRM `ListRecords` | — (query params `?limit=&cursor=`) |
| `POST /api/tenant/sales-orders/search` | Full filter + sort + search + pagination — mirrors CRM `SearchRecords` | `query.Request` JSON |

Filters travel in the **POST body** (a `query.Request`), exactly as CRM does — GET query-string filtering is not how the engine is fed. The `GET` form exists only for the unfiltered default list.

### 11.2 Pagination (reused verbatim)

- **Mechanism:** keyset pagination via opaque base64 cursor (`query/cursor.go`), pinned to the sort field+direction it was minted under. **No `OFFSET`, no `COUNT(*)`.**
- **Configurable page size:** `limit` in the request body (or `?limit=` for GET). Clamped by the shared engine: default **25** (`DefaultLimit`), max **100** (`MaxLimit`). Same constants CRM uses — no override.
- **Continuation:** response carries `nextCursor` + `hasMore`; the client passes `cursor` back to fetch the next page. The store fetches `limit+1` rows to compute `hasMore` without a count.
- **No offset paging (decided):** offset/limit is deliberately not the default and not used for large lists; jump-to-page is not part of the standard contract. Cursor + `hasMore` is the model, identical to CRM.

### 11.3 FieldResolver — `salesOrderResolver`

A `query.FieldResolver` mirroring `relationalResolver` (`crmstore/relational_filter.go`): a whitelist map of friendly logical keys → `(sqlExpr, DataType)`, plus the `cf:<key>` namespace for custom fields. An unresolved key returns `ok=false` → `*query.InvalidFilterError` → **HTTP 400** (never raw SQL). All client values are bound as `$n` — nothing but the fixed resolver expressions is ever concatenated. Table alias `so` = `sales_order`.

| Logical key | SQL expression | DataType | Ops | Requested filter |
|---|---|---|---|---|
| `id` | `so.sales_order_uuid::text` | string | eq | (internal) |
| `document_number` / `record_number` | `COALESCE(so.sales_order_number,'')` | string | eq, contains, startswith | **Document Number** |
| `customer_id` | `so.sales_order_customer_id::text` | string | eq, in | **Customer** |
| `status` | `so.sales_order_status::text` | string | eq, in | **Status** (multi-select) |
| `sales_rep_id` | `so.sales_order_sales_rep_id::text` | string | eq, in, is_null | **Sales Representative** |
| `owner_id` | `so.sales_order_owner_id::text` | string | eq, in, is_null | Customer Owner |
| `order_date` | `so.sales_order_date` | date | eq, gt, gte, lt, lte, between | **Order Date** (range) |
| `expected_delivery` | `so.sales_order_expected_delivery` | date | gte, lte, between, is_null | **Expected Delivery Date** (range) |
| `currency_id` | `so.sales_order_currency::text` | string | eq, in, is_null | **Currency** |
| `payment_terms_id` | `so.sales_order_payment_terms::text` | string | eq, in, is_null | **Payment Terms** |
| `price_level_id` | `so.sales_order_price_level::text` | string | eq, in, is_null | **Price Level** |
| `grand_total` | `so.sales_order_grand_total` | number | eq, gt, gte, lt, lte, between | **Grand Total** (range) |
| `po_number` | `so.sales_order_po_number` | string | eq, contains, startswith | PO Number |
| `ship_same_as_billing` | `so.sales_order_ship_same_as_bill` | bool | eq, is_null | (boolean filter) |
| `created_by` | `so.sales_order_created_by::text` | string | eq, in, is_null | **Created By** |
| `updated_by` | `so.sales_order_updated_by::text` | string | eq, in, is_null | **Updated By** |
| `created_at` | `so.sales_order_created_at` | date | gte, lte, between | **Created Date** (range) |
| `updated_at` | `so.sales_order_updated_at` | date | gte, lte, between | **Updated Date** (range) |
| `cf:<key>` | `so.sales_order_custom_fields->>'<key>'` | per `workflow_field_definitions` | per declared type | **Custom Fields** |

The allowed-ops column follows the engine's own `opsByType` table (`query/filter.go`) — e.g. `TypeBool` only permits `eq`/`is_null`, `TypeNumber`/`TypeDate` permit ranges. `cf:<key>` keys are validated against the sales-order workflow's `workflow_field_definitions` and only interpolated after passing the `^[a-z][a-z0-9_]{0,62}$` guard, identical to CRM.

### 11.4 Filtering (all advanced-filter kinds covered by existing operators)

Every advanced-filter kind maps onto an existing engine operator — **no new operators**:

- **Date ranges** → `between` (or paired `gte`/`lte`) on `order_date`, `expected_delivery`, `created_at`, `updated_at`.
- **Numeric ranges** → `between`/`gte`/`lte` on `grand_total`.
- **Multi-select** → `in` on `status`, `customer_id`, `currency_id`, `payment_terms_id`, `price_level_id`, `sales_rep_id`, `created_by`, `updated_by`.
- **Boolean** → `eq` on `ship_same_as_billing` or any `bool` custom field.
- **Status filters** → `eq`/`in` on `status`.
- **Custom fields** → any `cf:<key>` with the operator its declared type allows.
- **Filter × scope is ANDed** (RBAC scope clause AND user filters) — the invariant is preserved; a filter can only narrow the caller's permitted set.

### 11.5 Global search (shared engine, generic)

**Decision:** CRM has no server-side multi-field global-search operator today, so rather than build a Sales-Order-specific one, add a **generic, reusable global-search capability to the shared `query/` engine** that every list module (Sales Order, customer, lead, prospect, future modules) can opt into.

- **Engine change (shared, generic):** add an optional `Search string` field to `query.Request`, plus a small opt-in interface a resolver may implement:
  ```go
  // query/filter.go — implemented by resolvers that support a global search box.
  type SearchResolver interface {
      // SearchPredicate returns a self-contained SQL boolean expression matching the
      // search term bound at `placeholder` (e.g. "$4"), or "" if search is unsupported.
      // The engine binds the term (with wildcards) as a parameter; the fragment is
      // trusted per-entity code and must reference only that placeholder for the value.
      SearchPredicate(placeholder string) string
  }
  ```
- **Builder behavior:** when `Search` is non-empty and the resolver implements `SearchResolver`, the engine binds `'%' || term || '%'` as `$n` and **ANDs** the resolver's fragment into the `WHERE` (scope AND filters AND search — the "filter × scope = AND" invariant holds; the OR lives *inside* the resolver's fragment). Nothing client-supplied is concatenated; only the bound placeholder is referenced.
- **Sales Order's `SearchPredicate`** covers document number, reference/PO, notes, snapshot customer name (same-table), and item SKU/name (child) via a correlated `EXISTS`:
  ```sql
  (   so.sales_order_number             ILIKE $n
   OR so.sales_order_po_number          ILIKE $n
   OR so.sales_order_memo               ILIKE $n
   OR so.sales_order_notes              ILIKE $n
   OR so.sales_order_bill_customer_name ILIKE $n
   OR EXISTS (SELECT 1 FROM sales_order_item soi
               WHERE soi.sales_order_id = so.sales_order_id
                 AND (soi.sku ILIKE $n OR soi.item_name ILIKE $n))
   OR EXISTS (SELECT 1 FROM customer c
               WHERE c.customer_id = so.sales_order_customer_id
                 AND (c.customer_name ILIKE $n OR c.customer_doc_num ILIKE $n)))
  ```
  This matches the requested set — **document number, customer (name/code), reference number, notes, SKU, item name** — through one bound parameter.
- **Reuse:** because the interface lives in the shared engine, `customer`/`lead`/`prospect` resolvers can implement `SearchPredicate` too and gain the same global search for free — no per-module search logic anywhere.
- Guarded by **filter-invariant-checker**: value parameterized, scope still ANDed, and `query` stays dependency-free (the interface is *defined* in `query`; resolvers that already import `query` implement it — the engine never imports a resolver).

### 11.6 Sorting (shared engine, generic)

**Decision:** extend the shared engine generically so all modules benefit. The added columns are stable (`NOT NULL`), the change is backward-compatible, and the extra sorts are useful beyond Sales Order.

- **Engine change (shared, generic):** today `query/builder.go` hardcodes the sortable set to `created_at`, `updated_at`, `record_number`. Add an opt-in interface; resolvers that don't implement it keep exactly today's 3-field behavior (existing modules byte-identical):
  ```go
  // query/filter.go — implemented by resolvers that expose extra stable sort columns.
  type SortResolver interface {
      // SortExpr returns the SQL expression for a sortable key and whether it is allowed.
      // The expression MUST be NOT NULL (keyset correctness); the builder always appends
      // the row's unique id as the final tiebreaker for a total order.
      SortExpr(key string) (expr string, ok bool)
  }
  ```
- **Keyset stays correct:** the cursor already generalizes over `(sortExpr, id)`; the only requirement is that a registered sort column is `NOT NULL`. Every Sales Order sort column below satisfies this.
- **Sales Order sortable whitelist** (each paired with the `sales_order_id` tiebreaker):

  | Sort key | Column | Requested |
  |---|---|---|
  | `document_number` / `record_number` | `sales_order_number` | **Document Number** |
  | `order_date` | `sales_order_date` (NOT NULL DEFAULT) | **Order Date** |
  | `grand_total` | `sales_order_grand_total` (NOT NULL DEFAULT 0) | **Grand Total** |
  | `status` | `sales_order_status` (NOT NULL) — by status id/ordinal¹ | **Status** |
  | `customer_id` | `sales_order_customer_id` (NOT NULL) — by id; name-sort needs a join² | **Customer** |
  | `created_at` / `updated_at` | `sales_order_created_at` / `_updated_at` | **Created / Updated Date** |

  ¹ Status sorts by lifecycle ordinal (id); join `lkp_record_status` for name order if ever needed. ² Customer sorts by id; to sort by name, the store joins `customer` and orders on `COALESCE(customer_name,'')`.
- Applied via the **add-filter-field** skill and guarded by **filter-invariant-checker** (new sort fields permitted once NULL ordering is solved — here trivially, all `NOT NULL`). Existing modules unchanged unless they opt in by implementing `SortResolver`.

### 11.7 Response structure (exact CRM envelope)

The list returns the **same envelope as CRM `SearchRecords`**, so the frontend table/pagination/filter/search components are reused as-is:

```json
{
  "success": true,
  "scope": "team",
  "records": [ /* array of sales-order summary objects (flattened core+custom fields, like workflow.Record) */ ],
  "nextCursor": "eyJzIjoiY3JlYXRlZF9hdCIsImQiOiJkZXNjIiwidiI6…",   // null on last page
  "hasMore": true
}
```

**Mapping to the requested response fields:**
- `records` → **records** (identical).
- **page metadata** → `nextCursor` + `hasMore` + the echoed `limit` (this *is* CRM's page metadata; there is no page number in a cursor model).
- **`totalCount` / `filteredCount`** → **intentionally excluded** from the standard listing contract (no `COUNT(*)` on normal list calls), matching CRM and keeping lists O(page) at millions of rows. If a specific view ever needs a count, it can be added later as an isolated, explicitly-requested param — never on the default list path, and not consumed by the shared frontend list components.

### 11.8 Saved filters

CRM has **no saved-filter/view feature today** (no table or endpoint in the repo), so there is nothing to reuse and — per the reuse-first mandate — **no Sales-Order-specific implementation will be built**. If saved views are wanted later, they should be added once to the shared infrastructure as a generic tenant-scoped `saved_view` table (per user + resource, storing a `query.Request` JSON) that *every* list module reuses, Sales Order included. Deferred, not in this scope.

### 11.9 Indexes for listing performance

In addition to §6, add composite `(sort_col, sales_order_id)` indexes so keyset sorts are index-ordered scans, and single-column partial indexes for high-selectivity filters (all partial on `WHERE sales_order_deleted_at IS NULL` to stay small as soft-deletes accumulate):

```sql
-- Keyset sort support (sort column + id tiebreaker)
CREATE INDEX IF NOT EXISTS idx_so_orderdate_id   ON sales_order (sales_order_date, sales_order_id)        WHERE sales_order_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_so_grandtotal_id  ON sales_order (sales_order_grand_total, sales_order_id) WHERE sales_order_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_so_updated_id     ON sales_order (sales_order_updated_at, sales_order_id)  WHERE sales_order_deleted_at IS NULL;
-- Common combined view: status + recency
CREATE INDEX IF NOT EXISTS idx_so_status_created ON sales_order (sales_order_status, sales_order_created_at, sales_order_id) WHERE sales_order_deleted_at IS NULL;
-- Filter support (single-column partial)
CREATE INDEX IF NOT EXISTS idx_so_expdelivery     ON sales_order (sales_order_expected_delivery) WHERE sales_order_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_so_currency        ON sales_order (sales_order_currency)      WHERE sales_order_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_so_payment_terms   ON sales_order (sales_order_payment_terms) WHERE sales_order_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_so_price_level     ON sales_order (sales_order_price_level)   WHERE sales_order_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_so_created_by      ON sales_order (sales_order_created_by)    WHERE sales_order_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_so_updated_by      ON sales_order (sales_order_updated_by)    WHERE sales_order_deleted_at IS NULL;
-- Optional substring search (requires: CREATE EXTENSION IF NOT EXISTS pg_trgm)
-- CREATE INDEX IF NOT EXISTS idx_so_number_trgm   ON sales_order USING gin (sales_order_number gin_trgm_ops) WHERE sales_order_deleted_at IS NULL;
-- CREATE INDEX IF NOT EXISTS idx_so_billname_trgm ON sales_order USING gin (sales_order_bill_customer_name gin_trgm_ops) WHERE sales_order_deleted_at IS NULL;
```

`idx_so_customer`, `idx_so_status`, `idx_so_date`, `idx_so_sales_rep`, `idx_so_owner`, `idx_so_created`, and `idx_so_custom_gin` from §6 already cover the remaining filter/sort keys; the migration consolidates the full set (deduplicated). Prefix search (`startswith` → `col ILIKE 'q%'`) can use a `text_pattern_ops` btree without `pg_trgm`; reserve `pg_trgm` GIN for true substring (`contains`) search on names/notes.

---

## 12. Validation Rules

**Header**
- `customerUuid` required, must resolve to a live `customer` in this tenant; caller must have scope on it.
- `salesTaxPercent` 0–100; `exchangeRate` > 0.
- `paymentTermsId`/`priceLevelId`/`currencyId`/`stateId`/`countryId` — must reference live, active lookup rows.
- Money totals never negative (CHECK + service).
- `customFields` validated against `sales_order` workflow's `workflow_field_definitions` (≤15, type/required/enum/regex) — reuse `workflow.ValidateCustomFields` / `ValidateCustomFieldsPartial`.

**Lines**
- ≥1 line required to submit for approval (a `DRFT` may be empty).
- `lineNumber` unique per order (DB `uq_soi_line`); dense 1..N enforced in service.
- `quantity` ≥ 0; `unitPrice` ≥ 0; `discountPercent` 0–100; `tax_percent` 0–100.
- `inventoryItemUuid` XOR `description`: a line references a catalog item **or** is free-text (`inventory_item_id` NULL).
- Allocation `fulfilled ≤ allocated`; `allocated ≤ available` at reserve time (checked under row lock on `inventory_stock`).

**Transitions** — only moves in `allowedSOTransitions`; else 409. Cancel releases open allocations.

**Tenant/RBAC/IDOR** — every mutation checks `authz.Check(sales_order, action)` before touching the body; every single-record op runs the scope/IDOR guard and returns **404** (not 403) on denial, logging `idor_denied`. List/search compose scope into SQL (never filter-in-Go).

---

## 13. Performance Recommendations

1. **Keyset pagination only** (opaque base64 cursor via `query/`); no `OFFSET`/`COUNT(*)`. Sort restricted to `created_at`/`updated_at`/`record_number`. Backed by `idx_so_created (created_at, id)`.
2. **Partial indexes on `WHERE deleted_at IS NULL`** keep hot indexes small as soft-deleted rows accumulate (matches `customer`).
3. **Single-transaction writes.** Header + all lines + totals + history in one tx; recompute totals server-side from line rows (never trust client totals).
4. **Fetch `limit+1`** to compute `hasMore` without a count.
5. **Inventory aggregation** uses `idx_alloc_open` (partial on open statuses). If per-item allocation counts grow large (100k+ open reservations), add an optional **materialized `inventory_stock.allocated_cache`** column updated transactionally on allocation change — deferred until measured.
6. **Row-lock `inventory_stock` (`SELECT … FOR UPDATE`)** when reserving, to serialize concurrent allocations against the same item×warehouse (mirrors `Engine.Apply`'s locking).
7. **GIN index on `custom_fields`** for JSONB filters; system/core fields use btree.
8. **Line loads** hit `idx_soi_order`; the get-one endpoint issues one header query + one lines query (no N+1).

---

## 14. Future Scalability

- **Millions of orders:** per-tenant DBs bound each tenant's table size; keyset pagination + partial indexes keep reads flat. If a single tenant's `sales_order` exceeds ~50M rows, partition by `sales_order_date` range (Postgres declarative partitioning) — the schema is partition-ready (date column present, no cross-partition unique beyond `id`/`uuid`).
- **Shared inventory pays forward:** Purchase Orders (receiving → `quantity_on_hand++`), Invoices (from SO), GRN, and Manufacturing all reuse `inventory_item`/`inventory_stock`/`inventory_allocation` with no schema change. `inventory_allocation` already generalizes (add a nullable `purchase_order_id` later, or a polymorphic `source_type`/`source_id` if many demand sources appear).
- **Multi-currency** ready (`currency` + `exchange_rate` on header).
- **Fulfillment/shipment** is a natural next child table (`sales_order_fulfillment`) consuming allocations; the allocation `fulfilled_quantity` + `PART`/`FILL` statuses anticipate it.
- **Document conversion** (Quote→SO→Invoice) can reuse `sales_order_parent_id` lineage + a generic conversion service.
- **Tax engine:** `lkp_tax_rate` can grow a `tax_rate_component` child (compound taxes) or integrate an external tax API without touching order tables (rates are snapshotted per line).

---

## 15. Backend Implementation Map (mirror the CRM module)

New/changed files, following the traced `customer` architecture exactly:

| Concern | Action | Reference to mirror |
|---|---|---|
| Schema | Append tables/indexes/seeds to `database/migrations/tenant/schema.sql` via **add-migration** skill | existing `customer` block |
| RBAC | Add `ResourceInventoryItem` (+ 4 actions) to `authz/catalog.go`; `sales_order` already present | `authz/catalog.go:124` |
| Route registration | Add `GET /sales-orders` + `POST /sales-orders/search` (+ CRUD, `inventory/items`) in `main.go` via `tenantChain` | `main.go:451-476` |
| Controller | New `controllers/salesorder.go` (`SalesOrderOps`) mirroring `CRMOps` handler shapes incl. `ListRecords`/`SearchRecords`; new `controllers/inventory.go` | `controllers/crm.go` |
| Store | New `salesorder/` (or `crmstore`-adjacent) store with hand-written SQL + transactional create/update + `SearchRecords` (keyset) | `crmstore/relational_store.go` |
| Resolver (§11.3) | New `salesOrderResolver` (`query.FieldResolver`) — field mappings only, no new filter logic; reuse `query/` verbatim | `crmstore/relational_filter.go` |
| Shared engine — sorting (§11.6) | Add generic opt-in `SortResolver` interface to `query/`; existing 3-field default unchanged for modules that don't implement it | `query/builder.go`, `query/filter.go` |
| Shared engine — search (§11.5) | Add `Search` field to `query.Request` + generic opt-in `SearchResolver` interface; SO (and optionally CRM) resolvers implement `SearchPredicate` | `query/filter.go`, `query/builder.go` |
| Numbering | Go post-insert `SORD-%06d` | `crmstore/relational_store.go` `customer_doc_num` |
| Audit | Reuse `auditCRM`-style helper writing `audit_logs` + `sales_order_history` | `controllers/crm_audit.go`, `customer_history` |
| Attachments | Reuse as-is; ensure `RecordKeyForAttachment` resolves a `sales_order` UUID | `workflow/attachments.go:139` |
| Tests | Table-driven store/validator tests; filter invariant tests; RBAC drift test already expects `sales_order` | `workflow/filter_test.go`, `rbac_catalog_drift_test.go` |
| Security review | Run **tenancy-security-reviewer**, **migration-auditor**, **filter-invariant-checker** agents before merge | — |

---

## 16. Open Decisions / Notes

1. **Line naming convention:** header table `sales_order` uses full `sales_order_` prefixes (sibling of `customer`); child tables (`sales_order_item`, `sales_order_history`, `inventory_stock`, `inventory_allocation`) use the relaxed prefixed-PK + unprefixed-attribute style of `customer_history`. This split matches the existing master-vs-child convention in the schema.
2. **`ss_customer_id`** kept on `sales_order` for parity with `customer` (platform owner stamp, no FK). Confirm it should be populated from the JWT/tenant context.
3. **Inventory stock seeding:** `inventory_stock` rows are created lazily on first stock adjustment (or when an item is added to a warehouse). No seed data beyond the default warehouse.
4. **Approval gate:** Sales Order includes `PAPV`/`APPV`. Whether it reuses the customer dual-approver machinery (`crm_workflow_approver`) or a simpler single-approver flow is deferred to the implementation plan.
5. **Legacy v1 `sales_order` workflow** (JSONB, `schema.sql:1617`) is left in place but superseded; the relational module is authoritative. No data migration needed (no production records).
6. **Listing contract (§11) — DECIDED (CRM-exact).** Keyset cursor pagination + `{records, nextCursor, hasMore}`; no `COUNT(*)` on normal list calls, no offset default. `totalCount`/`filteredCount`/offset are out of the standard contract. Frontend list components are reused unchanged.
7. **Sort + global search (§11.5–11.6) — DECIDED (shared engine).** Extend the generic `query/` engine with opt-in `SortResolver` + `SearchResolver` interfaces so every module can benefit; modules that don't opt in are unchanged. No Sales-Order-specific listing/search logic. Guarded by **filter-invariant-checker**.
8. **Saved filters — DECIDED.** None in CRM today; not building a Sales-Order-specific one. If added later, build once as a generic shared `saved_view`.
