# Chart of Accounts Module — Backend Design Spec

**Date:** 2026-07-25
**Status:** Approved, not yet implemented. Branch `feat/chart-of-accounts`.
**Scope:** New Chart of Accounts master-data module — the first module of the Finance section. Ships the fixed category/sub-category reference tree, 127 seeded accounts, user-extensible child accounts, named default-account mapping slots, and a grouped reporting view. **General ledger, journal entries, posting, balances, and trial balance are explicitly out of scope** and become separate specs.

---

## 1. Overview & Goals

Add the **Chart of Accounts (CoA)** — the tenant's canonical list of financial accounts, organised into a fixed hierarchy of categories and sub-categories, and the target of the named "default account" slots that future transaction posting will resolve against.

The module is **master data, not a document**. It has no workflow, no approval chain, no owner, and no state machine. Its closest existing sibling in this codebase is `inventory` (and the `lkp_*` tables): tenant-global reference data guarded by resource-level RBAC with no per-record ownership scope.

**Key finding that shaped this design:** unlike Purchase Order, Refund, and Item Receipt, there is **no pre-existing scaffolding for this module**. A repo-wide search for `chart_of_account`, `gl_account`, `account_code`, and `ledger` found only the inventory slab ledger and the payment/credit-memo application ledgers — all unrelated. Every table, permission, and route in this spec is new. There is nothing to reconcile with and nothing half-built to finish.

| Asset searched for | Result |
|---|---|
| `chart_of_account` / `gl_account` tables | None |
| `authz.ResourceChartOfAccount` | Not in catalog |
| `lkp_record_type` entry for accounts | None |
| Any journal / posting engine | None — the whole GL layer is unbuilt |
| `secret/` field encryption | **Exists** (`secret.Cipher`, `SECRET_ENCRYPTION_KEY`) — reused here |
| `query/` filter engine | **Exists** — reused for list/search |

**Non-negotiable constraints (CLAUDE.md):** database-per-tenant, no `tenant_id` columns; idempotent append-only migrations, never `ALTER TABLE` to add columns and never a down-migration; the mandatory `/api/tenant/` security chain (JWT + `TenantResolver` + `authz.Check`) with `permission_denied` logging; all list/search through `query/`; files ≤300 lines; table-driven `testify` tests for all pure functions.

**Single-entity policy (stated 2026-07-25):** every tenant is one subsidiary, one company, one country, default USA. No `company_id`, `subsidiary_id`, or per-account `currency_id` appears anywhere in this design. The database-per-tenant boundary already *is* the company boundary.

---

## 2. Architecture Decisions

**AD-1 — Categories and sub-categories are fixed seeded reference tables, not user data.** Nine categories and seventeen sub-categories, seeded read-only, exposed through a single `GET .../categories` endpoint. They are tables rather than Go constants so that `coa_account.subcategory_id` can carry a real foreign key. No endpoint creates, updates, or deletes them.

**AD-2 — `bs_pnl` lives on the account, not the category.** The obvious modelling is one BS/PNL flag per category, and it is wrong for this dataset: category 9000 (System & Control) contains both balance-sheet accounts (9101 Opening Balance Equity, 9102 Suspense) and P&L accounts (9103–9107). The flag is therefore per-account, **derived from the sub-category and read-only on write**, with exactly one exception — under sub-category 9100 it is a required field on create, because that is the only sub-category that genuinely mixes both.

**AD-3 — Account codes are unique; account names are not.** The seed data contains two accounts named "Inventory Adjustment": 5107 under Cost of Goods Sold and 9104 under System & Control. A uniqueness constraint on name is the obvious thing to add and it would fail the seed on first run. Uniqueness is enforced on `coa_account_code` only, and only among live (non-soft-deleted) rows, so a code frees up after deletion.

**AD-4 — The tree is two levels deep, enforced by a `depth` column.** Users create children under seeded accounts (the "HDFC USA under 1103 Bank Account - Operating" case). Nothing requires depth 3, and `1103.01.01` would complicate code generation, lexical sorting, and the report renderer for no consumer. A `CHECK` constraint cannot express "my parent must itself have no parent" because it cannot run a subquery, so the cap is carried by a real `coa_account_depth SMALLINT` column with `CHECK (depth IN (0,1))`. The application sets `depth = parent.depth + 1`; depth 2 is unrepresentable. Relaxing this later means widening one `CHECK`.

**AD-5 — A child always shares its parent's sub-category, enforced by a composite foreign key.** Same subquery limitation as AD-4, solved declaratively: `UNIQUE (coa_account_id, subcategory_id)` on `coa_account`, then a self-referencing composite FK `(parent_id, subcategory_id) REFERENCES coa_account (coa_account_id, subcategory_id)`. A child under 1103 cannot be filed under Fixed Assets, and the database — not the application — is what guarantees it.

**AD-6 — `is_postable` is never automatic.** The tempting rule is "an account becomes a non-postable header as soon as it gains a child." Follow it through: `default_bank` points at 1103, a user adds a child under 1103, 1103 silently flips to non-postable, and a default slot now references an account that cannot be posted to. Nothing errors until a transaction fails months later. `is_postable` is therefore an **explicit user toggle only**; adding a child leaves the parent postable. `is_postable = false` excludes an account from every transaction dropdown while leaving it visible in the CoA report.

**AD-7 — One referential guard protects accounts referenced by default slots, and all four retiring mutations call it.** Deactivating, hiding, soft-deleting, or un-posting an account that a default slot points at would leave the slot dangling. A single `blockingSlots(ctx, pool, accountID)` helper is called by all four paths and returns `409` naming the slots. The user repoints the slot first, then retires the account. This is one function with four callers rather than four independent checks, precisely because the four-independent-checks version is how three of them end up missing it.

**AD-8 — Visibility is two flags, not one.** `is_active` controls whether the account may be used on new transactions; `is_visible` controls whether it appears in the CoA report at all. This separates "retired but still needed in historical reporting" from "hidden entirely". The combination `is_active = true AND is_visible = false` is meaningless and rejected by a `CHECK`.

**AD-9 — Account-type-specific fields are a fixed developer-defined JSONB schema, and are NOT the custom-fields mechanism.** Each account carries an `account_type` (`general|bank|cash|credit_card|ar|ap|tax|inventory|fixed_asset`) and a `coa_account_attributes JSONB` validated in Go against a fixed per-type field list. This is deliberately **not** CLAUDE.md's `custom_fields` mechanism — there is no `workflow_field_definitions` row, no ≤15 user-defined cap, and no per-tenant configurability. The column is named `attributes` rather than `custom_fields` to keep the distinction visible at the schema level. **CoA accounts have no user-defined custom fields.** Adding them later is a separate column and a separate decision.

**AD-10 — Bank account numbers are encrypted at rest and never returned in full.** Full numbers are encrypted through the existing `secret.Cipher` and the API returns only `accountNumberLast4`. There is **no unmask endpoint and no `read_sensitive` permission**, because there is no consumer for the full value — ACH/NACHA payment-file generation does not exist. Storing it encrypted preserves the option; exposing it over an API today would buy a permission, an audit path, and a leak vector for nothing. Revisit when payment files are built.

**AD-11 — Seed data lives in `tenant/schema.sql` alongside the `lkp_*` tables.** 127 accounts, 9 categories, 17 sub-categories, and 19 default slots as idempotent `INSERT ... ON CONFLICT DO NOTHING`. This matches how `lkp_unit`, `lkp_warehouse`, and `lkp_tax_rate` already seed. All 127 seeded accounts are top-level (`parent_id IS NULL`, `depth = 0`), so there is no insert-ordering problem. Default-slot seeds resolve their target by code subquery, which keeps them idempotent and independent of serial values.

---

## 3. Reference Data

### 3.1 Categories (9, fixed)

| Code | Name | Range | Normal balance |
|---|---|---|---|
| 1000 | Assets | 1000–1999 | debit |
| 2000 | Liabilities | 2000–2999 | credit |
| 3000 | Equity | 3000–3999 | credit |
| 4000 | Revenue | 4000–4999 | credit |
| 5000 | Cost of Goods Sold | 5000–5999 | debit |
| 6000 | Operating Expenses | 6000–6999 | debit |
| 7000 | Finance Costs | 7000–7999 | debit |
| 8000 | Other Income | 8000–8999 | credit |
| 9000 | System & Control Accounts | 9000–9999 | debit |

**Reconciliation note.** The source material contained two conflicting groupings: a range table reading `6000-7999 Operating Expenses` and `8000-8999 Other Income & Expenses`, and an account list treating `7000 - Finance Costs` and `8000 - Other Income` as separate top-level classifications. **The account list wins** — it is the more specific of the two and is what the 127 rows are actually filed under.

### 3.2 Sub-categories (17, fixed)

| Category | Sub-category | Code | Range |
|---|---|---|---|
| 1000 Assets | Current Assets | 1100 | 1100–1199 |
| 1000 Assets | Fixed Assets | 1200 | 1200–1299 |
| 1000 Assets | Intangible Assets | 1300 | 1300–1399 |
| 2000 Liabilities | Current Liabilities | 2100 | 2100–2199 |
| 2000 Liabilities | Long-Term Liabilities | 2200 | 2200–2299 |
| 3000 Equity | Equity | 3100 | 3100–3199 |
| 4000 Revenue | Sales | 4100 | 4100–4199 |
| 4000 Revenue | Returns, Discounts & Allowances | 4200 | 4200–4299 |
| 5000 COGS | Cost of Goods Sold | 5100 | 5100–5199 |
| 6000 OpEx | Payroll | 6100 | 6100–6199 |
| 6000 OpEx | Administrative | 6200 | 6200–6299 |
| 6000 OpEx | Sales & Marketing | 6300 | 6300–6399 |
| 6000 OpEx | Logistics | 6400 | 6400–6499 |
| 6000 OpEx | Depreciation | 6500 | 6500–6599 |
| 7000 Finance Costs | Finance Costs | 7100 | 7100–7199 |
| 8000 Other Income | Other Income | 8100 | 8100–8199 |
| 9000 System & Control | System & Control Accounts | 9100 | 9100–9199 |

Only the three Assets sub-category codes were given explicitly in the source (1100/1200/1300). The remaining fourteen are derived from the account codes filed under them.

### 3.3 Accounts (127, seeded)

Counts per sub-category, summing to 127:

| Sub-category | Count | Codes |
|---|---|---|
| Current Assets (1100) | 19 | 1101–1105, 1110, 1120–1121, 1130, 1135, 1140–1141, 1150, 1160, 1170–1173, 1180 |
| Fixed Assets (1200) | 9 | 1201–1208, 1210 |
| Intangible Assets (1300) | 6 | 1301–1305, 1310 |
| Current Liabilities (2100) | 13 | 2101–2102, 2110, 2120–2122, 2130, 2140–2141, 2150, 2160, 2170, 2180 |
| Long-Term Liabilities (2200) | 5 | 2201–2205 |
| Equity (3100) | 7 | 3101–3103, 3110, 3120, 3130, 3140 |
| Sales (4100) | 8 | 4101–4108 |
| Returns, Discounts & Allowances (4200) | 3 | 4201–4203 |
| Cost of Goods Sold (5100) | 8 | 5101–5108 |
| Payroll (6100) | 5 | 6101–6105 |
| Administrative (6200) | 18 | 6201–6218 |
| Sales & Marketing (6300) | 5 | 6301–6305 |
| Logistics (6400) | 3 | 6401–6403 |
| Depreciation (6500) | 2 | 6501–6502 |
| Finance Costs (7100) | 4 | 7101–7104 |
| Other Income (8100) | 5 | 8101–8105 |
| System & Control (9100) | 7 | 9101–9107 |
| **Total** | **127** | |

`bs_pnl` follows the category for every account except sub-category 9100, which splits: 9101 and 9102 are `BS`, 9103–9107 are `PNL`.

**Two seed adjustments from the source material:**

1. **Four tax accounts Americanized** to match the single-country-USA policy, keeping codes, category, sub-category, and BS/PNL unchanged:

   | Code | Source name | Seeded name |
   |---|---|---|
   | 1140 | GST/VAT Input | Sales Tax Receivable |
   | 1141 | GST Refund Receivable | Sales Tax Refund Receivable |
   | 2140 | GST/VAT Payable | Sales Tax Payable |
   | 2141 | TDS Payable | Withholding Tax Payable |

2. **9106 Intercompany Clearing is seeded `is_active = false`.** It is meaningless under the single-subsidiary policy but is retained for completeness of the fixed list; inactive keeps it out of every dropdown.

All 127 seeded rows are `is_system = true`, `is_postable = true`, `is_visible = true`, `depth = 0`, `parent_id = NULL`, and `is_active = true` **except 9106**, which is seeded inactive per adjustment 2 above. `account_type` is `'general'` except where the account is obviously typed (1101–1102 `cash`; 1103–1105 `bank`; 1120 `ar`; 2101 `ap`; 2102 `credit_card`; 1140–1141, 2140–2141 `tax`; 1170–1173 `inventory`; 1201–1208 `fixed_asset`).

### 3.4 Default mapping slots (19, seeded)

| Slot key | Points at |
|---|---|
| `default_ar` | 1120 Accounts Receivable |
| `default_ap` | 2101 Accounts Payable |
| `default_sales_revenue` | 4101 Product Sales |
| `default_sales_discount` | 4202 Sales Discount |
| `default_sales_returns` | 4201 Sales Returns |
| `default_cogs` | 5104 Direct Material |
| `default_inventory` | 1172 Inventory - Finished Goods |
| `default_bank` | 1103 Bank Account - Operating |
| `default_undeposited_funds` | 1110 Undeposited Funds |
| `default_sales_tax_payable` | 2140 Sales Tax Payable |
| `default_sales_tax_receivable` | 1140 Sales Tax Receivable |
| `default_deferred_revenue` | 2160 Deferred Revenue |
| `default_customer_advances` | 2150 Customer Advances |
| `default_freight_out` | 6401 Freight Outward |
| `default_bank_charges` | 6212 Bank Charges |
| `default_fx_gain` | 8103 Foreign Exchange Gain |
| `default_fx_loss` | 7103 Foreign Exchange Loss |
| `default_rounding` | 9103 Rounding Adjustment |
| `default_suspense` | 9102 Suspense Account |

A slot may only point at an account that is `is_postable AND is_active AND deleted_at IS NULL`. This is **enforced in the store layer, not by a database constraint** — Postgres cannot express "the referenced row satisfies a predicate" in a foreign key. The AD-7 guard is the other half of the same invariant: the store refuses to point a slot at a disqualified account, and refuses to disqualify an account a slot points at.

---

## 4. Schema

All in `database/migrations/tenant/schema.sql`, appended in FK order: `lkp_coa_category` → `lkp_coa_subcategory` → `coa_account` → `coa_account_history` → `coa_default_mapping`.

```sql
CREATE TABLE IF NOT EXISTS lkp_coa_category (
    category_id         SERIAL      PRIMARY KEY,
    category_code       INTEGER     NOT NULL,
    category_name       VARCHAR(60) NOT NULL,
    category_range_low  INTEGER     NOT NULL,
    category_range_high INTEGER     NOT NULL,
    category_normal_balance VARCHAR(6) NOT NULL,
    category_sort_order INTEGER     NOT NULL DEFAULT 0,
    CONSTRAINT uq_coa_category_code UNIQUE (category_code),
    CONSTRAINT chk_coa_category_balance CHECK (category_normal_balance IN ('debit','credit')),
    CONSTRAINT chk_coa_category_range   CHECK (category_range_low < category_range_high)
);

CREATE TABLE IF NOT EXISTS lkp_coa_subcategory (
    subcategory_id         SERIAL      PRIMARY KEY,
    category_id            INTEGER     NOT NULL REFERENCES lkp_coa_category(category_id),
    subcategory_code       INTEGER     NOT NULL,
    subcategory_name       VARCHAR(60) NOT NULL,
    subcategory_range_low  INTEGER     NOT NULL,
    subcategory_range_high INTEGER     NOT NULL,
    subcategory_sort_order INTEGER     NOT NULL DEFAULT 0,
    CONSTRAINT uq_coa_subcategory_code UNIQUE (subcategory_code),
    CONSTRAINT chk_coa_subcategory_range CHECK (subcategory_range_low < subcategory_range_high)
);

CREATE TABLE IF NOT EXISTS coa_account (
    coa_account_id          SERIAL       PRIMARY KEY,
    coa_account_uuid        UUID         NOT NULL DEFAULT gen_random_uuid(),
    coa_account_code        VARCHAR(20)  NOT NULL,
    coa_account_name        VARCHAR(150) NOT NULL,
    coa_account_description TEXT         NOT NULL DEFAULT '',
    subcategory_id          INTEGER      NOT NULL REFERENCES lkp_coa_subcategory(subcategory_id),
    parent_id               INTEGER          NULL,
    coa_account_depth       SMALLINT     NOT NULL DEFAULT 0,
    coa_account_bs_pnl      VARCHAR(3)   NOT NULL,
    coa_account_type        VARCHAR(20)  NOT NULL DEFAULT 'general',
    coa_account_attributes  JSONB        NOT NULL DEFAULT '{}',
    coa_account_is_postable BOOLEAN      NOT NULL DEFAULT TRUE,
    coa_account_is_active   BOOLEAN      NOT NULL DEFAULT TRUE,
    coa_account_is_visible  BOOLEAN      NOT NULL DEFAULT TRUE,
    coa_account_is_system   BOOLEAN      NOT NULL DEFAULT FALSE,
    coa_account_created_at  TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    coa_account_created_by  INTEGER          NULL REFERENCES employee(employee_id),
    coa_account_updated_at  TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    coa_account_updated_by  INTEGER          NULL REFERENCES employee(employee_id),
    coa_account_deleted_at  TIMESTAMP        NULL,
    coa_account_deleted_by  INTEGER          NULL REFERENCES employee(employee_id),
    coa_account_record_version INTEGER   NOT NULL DEFAULT 1,

    CONSTRAINT uq_coa_account_uuid UNIQUE (coa_account_uuid),
    -- AD-5: target of the composite self-FK below.
    CONSTRAINT uq_coa_account_id_subcat UNIQUE (coa_account_id, subcategory_id),
    CONSTRAINT chk_coa_bs_pnl    CHECK (coa_account_bs_pnl IN ('BS','PNL')),
    CONSTRAINT chk_coa_type      CHECK (coa_account_type IN
        ('general','bank','cash','credit_card','ar','ap','tax','inventory','fixed_asset')),
    -- AD-4: two-level cap, since CHECK cannot subquery the parent's depth.
    CONSTRAINT chk_coa_depth     CHECK (coa_account_depth IN (0,1)),
    CONSTRAINT chk_coa_depth_parent CHECK ((parent_id IS NULL) = (coa_account_depth = 0)),
    CONSTRAINT chk_coa_not_self  CHECK (parent_id IS NULL OR parent_id <> coa_account_id),
    -- AD-8: active implies visible.
    CONSTRAINT chk_coa_visibility CHECK (NOT (coa_account_is_active AND NOT coa_account_is_visible)),
    -- Seeded rows are undeletable.
    CONSTRAINT chk_coa_system_undeletable CHECK (NOT (coa_account_is_system AND coa_account_deleted_at IS NOT NULL)),
    CONSTRAINT chk_coa_soft_delete CHECK (
        (coa_account_deleted_at IS NULL AND coa_account_deleted_by IS NULL) OR
        (coa_account_deleted_at IS NOT NULL AND coa_account_deleted_by IS NOT NULL)
    ),
    -- AD-5: a child inherits its parent's sub-category, enforced by the database.
    -- Declared inline rather than as a follow-up ALTER TABLE: a bare
    -- "ALTER TABLE ... ADD CONSTRAINT" is not idempotent and would error on the
    -- second application of schema.sql. Postgres permits a self-referencing
    -- composite FK inline, targeting uq_coa_account_id_subcat above.
    -- MATCH SIMPLE (the default) means the constraint is satisfied whenever
    -- parent_id IS NULL, so top-level accounts are unaffected.
    CONSTRAINT fk_coa_parent_subcat FOREIGN KEY (parent_id, subcategory_id)
        REFERENCES coa_account (coa_account_id, subcategory_id)
);

-- AD-3: code unique among live rows only; name deliberately not unique.
CREATE UNIQUE INDEX IF NOT EXISTS uq_coa_account_code_live
    ON coa_account (coa_account_code) WHERE coa_account_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_coa_account_subcat ON coa_account (subcategory_id)
    WHERE coa_account_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_coa_account_parent ON coa_account (parent_id)
    WHERE coa_account_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_coa_account_dropdown
    ON coa_account (coa_account_code)
    WHERE coa_account_deleted_at IS NULL AND coa_account_is_active AND coa_account_is_postable;

CREATE TABLE IF NOT EXISTS coa_account_history (
    coa_account_history_id SERIAL      PRIMARY KEY,
    coa_account_id         INTEGER         NULL REFERENCES coa_account(coa_account_id),
    slot_key               VARCHAR(50)     NULL,
    history_action         VARCHAR(20) NOT NULL,
    history_field          VARCHAR(60) NOT NULL DEFAULT '',
    history_old_value      TEXT        NOT NULL DEFAULT '',
    history_new_value      TEXT        NOT NULL DEFAULT '',
    history_at             TIMESTAMP   NOT NULL DEFAULT CURRENT_TIMESTAMP,
    history_by             INTEGER         NULL REFERENCES employee(employee_id),
    CONSTRAINT chk_coa_history_action CHECK (history_action IN
        ('create','update','delete','activate','deactivate','show','hide','repoint_slot')),
    -- Every row describes either an account or a slot.
    CONSTRAINT chk_coa_history_target CHECK (coa_account_id IS NOT NULL OR slot_key IS NOT NULL)
);
CREATE INDEX IF NOT EXISTS idx_coa_history_account ON coa_account_history (coa_account_id, history_at DESC);

CREATE TABLE IF NOT EXISTS coa_default_mapping (
    slot_key         VARCHAR(50)  PRIMARY KEY,
    slot_label       VARCHAR(100) NOT NULL,
    slot_description TEXT         NOT NULL DEFAULT '',
    coa_account_id   INTEGER          NULL REFERENCES coa_account(coa_account_id),
    slot_is_system   BOOLEAN      NOT NULL DEFAULT TRUE,
    slot_sort_order  INTEGER      NOT NULL DEFAULT 0,
    slot_updated_at  TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    slot_updated_by  INTEGER          NULL REFERENCES employee(employee_id)
);
CREATE INDEX IF NOT EXISTS idx_coa_slot_account ON coa_default_mapping (coa_account_id);
```

Note the history table permits a NULL `coa_account_id` so that a slot repoint — which is not an account mutation — has somewhere to live.

---

## 5. Package Layout

```
chartofaccounts/
  types.go            Account, Category, SubCategory, DefaultSlot, Create/Update/Bulk inputs
  attributes.go       fixed per-account_type field schema + JSONB validation (AD-9)
  attributes_test.go
  numbering.go        child suffix allocation + next-free-int in range (AD-4)
  numbering_test.go
  bspnl.go            bs_pnl derivation incl. the 9100 branch (AD-2)
  bspnl_test.go
  masking.go          encrypt-on-write, last-4-on-read for bank numbers (AD-10)
  masking_test.go
  tree.go             flat rows -> BS/PNL > category > sub-category > account > children
  tree_test.go
  resolver.go         query.FieldResolver whitelist
  store.go            pool wrapper + shared row scanners
  store_create.go     store_get.go      store_update.go
  store_delete.go     store_bulk.go     store_defaults.go
  store_guard.go      blockingSlots — the single referential guard (AD-7)
controllers/
  chartofaccounts.go           auth chain + CRUD
  chartofaccounts_tree.go      grouped report endpoint
  chartofaccounts_defaults.go  mapping slots
  chartofaccounts_bulk.go      bulk enable/disable
  chartofaccounts_audit.go     history endpoint
```

Every file stays under the 300-line cap; `store.go` is split by verb from the outset rather than growing and being split later.

---

## 6. API Surface

All routes are `/api/tenant/finance/...`, behind the mandatory JWT + `TenantResolver` + `authz.Check` chain.

| Method | Route | Permission | Notes |
|---|---|---|---|
| `GET` | `/finance/accounts` | `read` | flat list, cursor-paginated; `?postable=&active=&visible=&subcategoryId=` |
| `POST` | `/finance/accounts/search` | `read` | filter + sort + paginate via `query/` |
| `GET` | `/finance/accounts/tree` | `read` | the report screen; `?includeInactive=&includeHidden=` |
| `GET` | `/finance/accounts/categories` | `read` | the fixed 9 + 17 reference tree |
| `POST` | `/finance/accounts` | `create` | |
| `GET` | `/finance/accounts/{uuid}` | `read` | |
| `PATCH` | `/finance/accounts/{uuid}` | `update` | honours `recordVersion` |
| `DELETE` | `/finance/accounts/{uuid}` | `delete` | soft delete |
| `PATCH` | `/finance/accounts/bulk` | `update` | transactional; per-uuid results |
| `GET` | `/finance/accounts/{uuid}/history` | `read` | |
| `GET` | `/finance/account-defaults` | `read` | all 19 slots with resolved accounts |
| `PATCH` | `/finance/account-defaults/{slotKey}` | `configure` | repoint a slot |

**`GET /finance/accounts?postable=true&active=true` is the single call every transaction dropdown uses** across orders, invoices, payments, and bills. Header accounts are excluded by construction rather than by each caller remembering to filter — this is what AD-6's `is_postable` flag exists to serve.

**RBAC.** One new resource, `ResourceChartOfAccount = "chart_of_account"`, with five catalog entries: `create`, `read`, `update`, `delete`, `configure`. **No new `Action` constant is required** — `ActionConfigure` already exists in `authz/catalog.go` and covers repointing slots, and dropping the unmask path (AD-10) removed the only reason to add one.

**Scope.** CoA is tenant-global reference data with no owner, exactly like `inventory_item`. There is no `own`-vs-`all` scope filtering and no `recordInScope` call, because there is no owner column to scope against. This is a deliberate deviation from the document-module skeleton and is called out here so `module-drift-checker` and `tenancy-security-reviewer` read it as intent, not omission.

---

## 7. Validation & Error Handling

### 7.1 Create

- **Code is allocated by the server, never supplied.** A child gets `parent_code + '.' + %02d` using the next free suffix (01–99). A top-level account gets the next free integer inside its sub-category's range. Both return `409` when exhausted.
- **`bs_pnl` is derived** from the sub-category and ignored if supplied — except under sub-category 9100, where it is required and must be `BS` or `PNL` (AD-2).
- **`subcategory_id` is required for a top-level account** and **inherited for a child**; supplying one that differs from the parent's is `400` (the composite FK would reject it anyway, per AD-5).
- **`attributes` is validated** against the fixed schema for the given `account_type`. Unknown key, wrong type, or missing required field → `400` naming the field. Bank accounts require `bankName` and `accountNumber`; `accountNumber` is encrypted on the way in and never echoed back (AD-10).
- **`depth`** is computed, never supplied.

### 7.2 The referential guard (AD-7)

`blockingSlots(ctx, pool, accountID) ([]string, error)` is called by **deactivate, hide, soft-delete, and un-post**. Non-empty result → `409`:

```json
{
  "success": false,
  "message": "Account 1120 Accounts Receivable is in use as a default account.",
  "blockingSlots": ["default_ar"]
}
```

### 7.3 Status codes

| Code | Cause |
|---|---|
| `400` | attribute validation failure; `*query.InvalidFilterError`; `bs_pnl` missing under 9100; `bs_pnl` invalid; sub-category mismatch on child create |
| `403` | RBAC denial (logged via `logSecurityEvent` with `permission_denied`) |
| `404` | unknown uuid or slot key |
| `409` | account referenced by a default slot; account has live children; `is_system` delete or code change; `recordVersion` mismatch; code range exhausted; slot repointed at a non-postable/inactive account |
| `500` | wrapped internal errors only |

All responses carry `success` and `message` per CLAUDE.md. `*query.InvalidFilterError` maps to `400`, never `500`.

### 7.4 Sorting and pagination

Keyset pagination through `query/`, default page 25, `MaxLimit` 100. Sortable fields are `coa_account_code` (default), `created_at`, and `updated_at`.

**Deviation noted deliberately:** CLAUDE.md restricts sortable fields to `created_at`, `updated_at`, and `record_number`. CoA has no `record_number`; `coa_account_code` is its equivalent and satisfies the same underlying requirement — stable, non-null, and unique among live rows — so keyset cursors remain correct. Recorded here so `filter-invariant-checker` reads it as intent. Filter ⨯ scope remains ANDed; field keys stay a `query.FieldResolver` whitelist; all values stay parameterised.

### 7.5 Bulk PATCH

`PATCH /finance/accounts/bulk` takes `{"uuids": [...], "isActive": bool?, "isVisible": bool?}`, runs inside one transaction, applies the AD-7 guard per account, and returns a per-uuid outcome array. Any single failure rolls the whole transaction back — partial application of a visibility change across an account list is worse than none.

### 7.6 Audit

Every create, update, delete, activate/deactivate, show/hide, and slot repoint writes a `coa_account_history` row with actor and before/after values. Permission denials go through `logSecurityEvent(r, "permission_denied", ...)`. Bank account numbers are never written to history or to any log — only the fact that the field changed.

---

## 8. Testing

**Pure functions — table-driven `testify`, no database:**

- `numbering`: suffix allocation, reuse of gaps left by deleted children, the 99-child ceiling, next-free-int in range, range exhaustion, boundary codes at each range edge.
- `attributes`: every one of the nine account types, unknown keys, missing required fields, wrong types, empty attributes on `general`.
- `bspnl`: derivation for all 17 sub-categories, plus the 9100 branch requiring an explicit value and rejecting anything other than `BS`/`PNL`.
- `masking`: encrypt/decrypt round-trip, last-4 extraction, inputs shorter than four characters, empty input, and that the plaintext never appears in the marshalled struct.
- `tree`: ordering by sort order then code, `is_visible` filtering, `includeInactive` toggling, children nested under the right parent, orphan rows not silently dropped.

**Database-backed — `-tags dbtest`, skipping cleanly without `TEST_DATABASE_URL`:**

- Seed lands exactly 9 categories, 17 sub-categories, 127 accounts, 19 slots.
- Re-applying `schema.sql` is idempotent — counts unchanged, no duplicate-key error.
- Each constraint in §4 rejects what it should: depth 2, cross-sub-category child, self-parent, `active AND NOT visible`, soft-deleting an `is_system` row, duplicate live code — and **accepts duplicate names** (5107/9104), which is the AD-3 regression test.
- The AD-7 guard blocks all four mutation paths for an account behind `default_ar`, and permits them once the slot is repointed.
- Bulk PATCH rolls back completely when one account in the batch is blocked.
- A slot cannot be repointed at a non-postable, inactive, or soft-deleted account.

Per the schema-verification note in project memory: run DB tests against a **fresh database per package** with `--single-transaction`, or constraint failures from a previous package's residue will look like real bugs.

---

## 9. Out of Scope

Deliberately excluded, each its own future spec:

- **General ledger and journal entries.** No `journal_entry`, no `journal_line`, no posting engine. The default mapping slots exist so that when posting is built, it has somewhere to resolve accounts from — but nothing writes to them yet.
- **Balances, trial balance, P&L and Balance Sheet rollups.** The tree endpoint returns structure only, no amounts. No balance fields or placeholders are added to the response, because a field that always returns zero is worse than an absent one.
- **Period close, fiscal calendars, opening balances.** 9101 Opening Balance Equity is seeded but unused.
- **Payment-file generation (ACH/NACHA)** and therefore any unmasking of bank account numbers (AD-10).
- **User-defined custom fields on accounts** (AD-9).
- **Multi-currency, multi-entity, consolidation** — excluded by the single-entity policy, not merely deferred.
