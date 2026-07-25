# Chart of Accounts Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship the Chart of Accounts master-data module — fixed category/sub-category reference tree, 127 seeded accounts, user-extensible two-level child accounts, 19 named default-account mapping slots, and a grouped reporting view.

**Architecture:** A new `chartofaccounts/` package following the `inventory` shape (tenant-global reference data, resource-level RBAC, no owner scope), wired through `controllers/` onto `/api/tenant/finance/...`. Pure logic (attribute validation, code allocation, BS/PNL derivation, masking, tree assembly) is separated from the store so it is unit-testable without a database. All list/search goes through `query/`.

**Tech Stack:** Go 1.x, pgx/v5, PostgreSQL, testify, existing `query/` filter engine, existing `secret/` AES-256-GCM cipher.

**Spec:** `docs/superpowers/specs/2026-07-25-chart-of-accounts-design.md` — read it before starting. Architecture decisions are referenced below as AD-1 … AD-11.

## Global Constraints

- Database-per-tenant. **No `tenant_id` column anywhere.** The connection is the scope.
- Migrations are idempotent and append-only: `CREATE TABLE IF NOT EXISTS`, `CREATE INDEX IF NOT EXISTS`, `INSERT ... ON CONFLICT DO NOTHING`. **Never `ALTER TABLE` to add a column. Never a down-migration.**
- Every route is behind JWT + `TenantResolver` + `authz.Check`. Permission denials call `logSecurityEvent(r, "permission_denied", ...)`.
- All responses are JSON carrying `success` and, on failure, `message`.
- `*query.InvalidFilterError` maps to **400**, never 500.
- Errors are always wrapped: `fmt.Errorf("context: %w", err)`. No `panic()` in production paths.
- `context.Context` is the first parameter of every store function.
- Files stay **under 300 lines**. Split by verb, not by layer.
- Table-driven `testify` tests for every pure function.
- Single-entity policy: no `company_id`, no `subsidiary_id`, no per-account `currency_id`.
- **CoA accounts have no user-defined custom fields.** The JSONB column is named `attributes` (AD-9), never `custom_fields`.
- Account **codes** are unique among live rows; account **names are deliberately not unique** (AD-3).

---

## File Structure

| File | Responsibility |
|---|---|
| `database/migrations/tenant/schema.sql` | Append 5 tables + 4 seed blocks |
| `authz/catalog.go` | Add `ResourceChartOfAccount` + 5 catalog entries |
| `chartofaccounts/types.go` | `Account`, `Category`, `SubCategory`, `DefaultSlot`, input structs |
| `chartofaccounts/errors.go` | `ErrNotFound`, `ClientError`, `ConflictError`, helpers |
| `chartofaccounts/attributes.go` | Fixed per-`account_type` field schema + validation (AD-9) |
| `chartofaccounts/bspnl.go` | BS/PNL derivation incl. the 9100 branch (AD-2) |
| `chartofaccounts/numbering.go` | Child suffix + next-free-int allocation (AD-4) |
| `chartofaccounts/masking.go` | Encrypt-on-write, last-4-on-read (AD-10) |
| `chartofaccounts/tree.go` | Flat rows → grouped report structure |
| `chartofaccounts/resolver.go` | `query.FieldResolver` + `SortResolver` + `SearchResolver` |
| `chartofaccounts/store.go` | Pool helpers, row scanners, shared SELECT |
| `chartofaccounts/store_get.go` | Get, list/search, categories |
| `chartofaccounts/store_create.go` | Create |
| `chartofaccounts/store_guard.go` | `BlockingSlots` — the single referential guard (AD-7) |
| `chartofaccounts/store_update.go` | Update |
| `chartofaccounts/store_delete.go` | Soft delete |
| `chartofaccounts/store_bulk.go` | Transactional bulk activate/hide |
| `chartofaccounts/store_defaults.go` | Read + repoint mapping slots |
| `chartofaccounts/store_history.go` | Append + read `coa_account_history` |
| `controllers/chartofaccounts.go` | Auth chain + CRUD handlers |
| `controllers/chartofaccounts_tree.go` | Tree + categories endpoints |
| `controllers/chartofaccounts_defaults.go` | Slot endpoints |
| `controllers/chartofaccounts_bulk.go` | Bulk endpoint |
| `controllers/chartofaccounts_audit.go` | History endpoint |
| `main.go` | Register 12 routes |

---

## Task 1: Reference tables + category/sub-category seed

**Files:**
- Modify: `database/migrations/tenant/schema.sql` (append at end of file)

**Interfaces:**
- Consumes: nothing
- Produces: tables `lkp_coa_category`, `lkp_coa_subcategory`; 9 + 17 seeded rows. Task 2's `coa_account.subcategory_id` FKs `lkp_coa_subcategory(subcategory_id)`.

- [ ] **Step 1: Append the two reference tables**

Append to the end of `database/migrations/tenant/schema.sql`:

```sql
-- ===========================================================================
-- CHART OF ACCOUNTS -- Finance section master data.
-- Spec: docs/superpowers/specs/2026-07-25-chart-of-accounts-design.md
-- FK order: lkp_coa_category -> lkp_coa_subcategory -> coa_account
--           -> coa_account_history -> coa_default_mapping
-- ===========================================================================

-- lkp_coa_category -- fixed, seeded, read-only (AD-1). 9 rows.
CREATE TABLE IF NOT EXISTS lkp_coa_category (
    category_id             SERIAL      PRIMARY KEY,
    category_code           INTEGER     NOT NULL,
    category_name           VARCHAR(60) NOT NULL,
    category_range_low      INTEGER     NOT NULL,
    category_range_high     INTEGER     NOT NULL,
    category_normal_balance VARCHAR(6)  NOT NULL,
    category_sort_order     INTEGER     NOT NULL DEFAULT 0,
    CONSTRAINT uq_coa_category_code    UNIQUE (category_code),
    CONSTRAINT chk_coa_category_balance CHECK (category_normal_balance IN ('debit','credit')),
    CONSTRAINT chk_coa_category_range   CHECK (category_range_low < category_range_high)
);

-- lkp_coa_subcategory -- fixed, seeded, read-only (AD-1). 17 rows.
CREATE TABLE IF NOT EXISTS lkp_coa_subcategory (
    subcategory_id         SERIAL      PRIMARY KEY,
    category_id            INTEGER     NOT NULL REFERENCES lkp_coa_category(category_id),
    subcategory_code       INTEGER     NOT NULL,
    subcategory_name       VARCHAR(60) NOT NULL,
    subcategory_range_low  INTEGER     NOT NULL,
    subcategory_range_high INTEGER     NOT NULL,
    subcategory_sort_order INTEGER     NOT NULL DEFAULT 0,
    CONSTRAINT uq_coa_subcategory_code  UNIQUE (subcategory_code),
    CONSTRAINT chk_coa_subcategory_range CHECK (subcategory_range_low < subcategory_range_high)
);
CREATE INDEX IF NOT EXISTS idx_coa_subcat_category ON lkp_coa_subcategory (category_id);
```

- [ ] **Step 2: Append the category seed (9 rows)**

```sql
INSERT INTO lkp_coa_category
    (category_code, category_name, category_range_low, category_range_high, category_normal_balance, category_sort_order) VALUES
    (1000,'Assets',                    1000,1999,'debit', 1),
    (2000,'Liabilities',               2000,2999,'credit',2),
    (3000,'Equity',                    3000,3999,'credit',3),
    (4000,'Revenue',                   4000,4999,'credit',4),
    (5000,'Cost of Goods Sold',        5000,5999,'debit', 5),
    (6000,'Operating Expenses',        6000,6999,'debit', 6),
    (7000,'Finance Costs',             7000,7999,'debit', 7),
    (8000,'Other Income',              8000,8999,'credit',8),
    (9000,'System & Control Accounts', 9000,9999,'debit', 9)
ON CONFLICT (category_code) DO NOTHING;
```

- [ ] **Step 3: Append the sub-category seed (17 rows)**

Resolves `category_id` by code so it never depends on serial values:

```sql
INSERT INTO lkp_coa_subcategory
    (category_id, subcategory_code, subcategory_name, subcategory_range_low, subcategory_range_high, subcategory_sort_order)
SELECT c.category_id, v.code, v.name, v.lo, v.hi, v.ord
FROM (VALUES
    (1000,1100,'Current Assets',                 1100,1199,1),
    (1000,1200,'Fixed Assets',                   1200,1299,2),
    (1000,1300,'Intangible Assets',              1300,1399,3),
    (2000,2100,'Current Liabilities',            2100,2199,1),
    (2000,2200,'Long-Term Liabilities',          2200,2299,2),
    (3000,3100,'Equity',                         3100,3199,1),
    (4000,4100,'Sales',                          4100,4199,1),
    (4000,4200,'Returns, Discounts & Allowances',4200,4299,2),
    (5000,5100,'Cost of Goods Sold',             5100,5199,1),
    (6000,6100,'Payroll',                        6100,6199,1),
    (6000,6200,'Administrative',                 6200,6299,2),
    (6000,6300,'Sales & Marketing',              6300,6399,3),
    (6000,6400,'Logistics',                      6400,6499,4),
    (6000,6500,'Depreciation',                   6500,6599,5),
    (7000,7100,'Finance Costs',                  7100,7199,1),
    (8000,8100,'Other Income',                   8100,8199,1),
    (9000,9100,'System & Control Accounts',      9100,9199,1)
) AS v(cat_code, code, name, lo, hi, ord)
JOIN lkp_coa_category c ON c.category_code = v.cat_code
ON CONFLICT (subcategory_code) DO NOTHING;
```

- [ ] **Step 4: Verify the SQL parses and is idempotent**

```bash
psql "$TEST_DATABASE_URL" -v ON_ERROR_STOP=1 --single-transaction -f database/migrations/tenant/schema.sql
```

Expected: no error. Run the identical command a **second** time — also no error (idempotency). Then:

```bash
psql "$TEST_DATABASE_URL" -tAc "SELECT (SELECT count(*) FROM lkp_coa_category), (SELECT count(*) FROM lkp_coa_subcategory)"
```

Expected: `9|17`

- [ ] **Step 5: Commit**

```bash
git add database/migrations/tenant/schema.sql
git commit -m "feat(coa): seed chart-of-accounts category and sub-category reference tables"
```

---

## Task 2: `coa_account`, history, mapping tables + 127-account seed

**Files:**
- Modify: `database/migrations/tenant/schema.sql` (append after Task 1's block)

**Interfaces:**
- Consumes: `lkp_coa_subcategory(subcategory_id, subcategory_code)` from Task 1
- Produces: tables `coa_account`, `coa_account_history`, `coa_default_mapping`; 127 seeded accounts, 19 seeded slots

- [ ] **Step 1: Append `coa_account`**

Note the composite self-FK is **inline**, not a follow-up `ALTER TABLE` — a bare `ADD CONSTRAINT` is not idempotent (AD-5):

```sql
-- coa_account -- 127 seeded rows + everything users add.
CREATE TABLE IF NOT EXISTS coa_account (
    coa_account_id             SERIAL       PRIMARY KEY,
    coa_account_uuid           UUID         NOT NULL DEFAULT gen_random_uuid(),
    coa_account_code           VARCHAR(20)  NOT NULL,
    coa_account_name           VARCHAR(150) NOT NULL,
    coa_account_description    TEXT         NOT NULL DEFAULT '',
    subcategory_id             INTEGER      NOT NULL REFERENCES lkp_coa_subcategory(subcategory_id),
    parent_id                  INTEGER          NULL,
    coa_account_depth          SMALLINT     NOT NULL DEFAULT 0,
    coa_account_bs_pnl         VARCHAR(3)   NOT NULL,
    coa_account_type           VARCHAR(20)  NOT NULL DEFAULT 'general',
    coa_account_attributes     JSONB        NOT NULL DEFAULT '{}',
    coa_account_is_postable    BOOLEAN      NOT NULL DEFAULT TRUE,
    coa_account_is_active      BOOLEAN      NOT NULL DEFAULT TRUE,
    coa_account_is_visible     BOOLEAN      NOT NULL DEFAULT TRUE,
    coa_account_is_system      BOOLEAN      NOT NULL DEFAULT FALSE,
    coa_account_created_at     TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    coa_account_created_by     INTEGER          NULL REFERENCES employee(employee_id),
    coa_account_updated_at     TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    coa_account_updated_by     INTEGER          NULL REFERENCES employee(employee_id),
    coa_account_deleted_at     TIMESTAMP        NULL,
    coa_account_deleted_by     INTEGER          NULL REFERENCES employee(employee_id),
    coa_account_record_version INTEGER      NOT NULL DEFAULT 1,

    CONSTRAINT uq_coa_account_uuid       UNIQUE (coa_account_uuid),
    -- AD-5: target of the composite self-FK below.
    CONSTRAINT uq_coa_account_id_subcat  UNIQUE (coa_account_id, subcategory_id),
    CONSTRAINT chk_coa_bs_pnl CHECK (coa_account_bs_pnl IN ('BS','PNL')),
    CONSTRAINT chk_coa_type   CHECK (coa_account_type IN
        ('general','bank','cash','credit_card','ar','ap','tax','inventory','fixed_asset')),
    -- AD-4: two-level cap. CHECK cannot subquery the parent's depth, so depth
    -- is a real column and depth 2 is unrepresentable.
    CONSTRAINT chk_coa_depth        CHECK (coa_account_depth IN (0,1)),
    CONSTRAINT chk_coa_depth_parent CHECK ((parent_id IS NULL) = (coa_account_depth = 0)),
    CONSTRAINT chk_coa_not_self     CHECK (parent_id IS NULL OR parent_id <> coa_account_id),
    -- AD-8: active implies visible.
    CONSTRAINT chk_coa_visibility CHECK (NOT (coa_account_is_active AND NOT coa_account_is_visible)),
    CONSTRAINT chk_coa_system_undeletable
        CHECK (NOT (coa_account_is_system AND coa_account_deleted_at IS NOT NULL)),
    CONSTRAINT chk_coa_soft_delete CHECK (
        (coa_account_deleted_at IS NULL AND coa_account_deleted_by IS NULL) OR
        (coa_account_deleted_at IS NOT NULL AND coa_account_deleted_by IS NOT NULL)
    ),
    -- AD-5: a child inherits its parent's sub-category, enforced by the database.
    -- MATCH SIMPLE (the default) satisfies the constraint whenever parent_id IS
    -- NULL, so top-level accounts are unaffected.
    CONSTRAINT fk_coa_parent_subcat FOREIGN KEY (parent_id, subcategory_id)
        REFERENCES coa_account (coa_account_id, subcategory_id)
);

-- AD-3: code unique among LIVE rows only. Name is deliberately NOT unique --
-- 5107 and 9104 are both "Inventory Adjustment".
CREATE UNIQUE INDEX IF NOT EXISTS uq_coa_account_code_live
    ON coa_account (coa_account_code) WHERE coa_account_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_coa_account_subcat ON coa_account (subcategory_id)
    WHERE coa_account_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_coa_account_parent ON coa_account (parent_id)
    WHERE coa_account_deleted_at IS NULL;
-- Serves the dropdown query: ?postable=true&active=true
CREATE INDEX IF NOT EXISTS idx_coa_account_dropdown ON coa_account (coa_account_code)
    WHERE coa_account_deleted_at IS NULL AND coa_account_is_active AND coa_account_is_postable;
```

- [ ] **Step 2: Append history + mapping tables**

```sql
-- coa_account_history -- append-only. coa_account_id is NULLable so a slot
-- repoint (not an account mutation) has somewhere to live.
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
    CONSTRAINT chk_coa_history_target CHECK (coa_account_id IS NOT NULL OR slot_key IS NOT NULL)
);
CREATE INDEX IF NOT EXISTS idx_coa_history_account ON coa_account_history (coa_account_id, history_at DESC);
CREATE INDEX IF NOT EXISTS idx_coa_history_slot    ON coa_account_history (slot_key, history_at DESC);

-- coa_default_mapping -- 19 named slots. The "points at a postable+active
-- account" rule is enforced in the store, not here: a FK cannot express a
-- predicate on the referenced row (AD-7).
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

- [ ] **Step 3: Append the 127-account seed**

All seeded rows are top-level (`parent_id NULL`, `depth 0`), so there is no insert-ordering problem. `created_by` is employee 1, matching `lkp_unit`/`lkp_warehouse`. Note `'Partner''s Capital'` doubles the apostrophe, and 9106 seeds inactive (AD-11):

```sql
INSERT INTO coa_account
    (coa_account_code, coa_account_name, subcategory_id, coa_account_bs_pnl,
     coa_account_type, coa_account_is_active, coa_account_is_system, coa_account_created_by)
SELECT v.code, v.name, s.subcategory_id, v.bs_pnl, v.acct_type, v.active, TRUE, 1
FROM (VALUES
    -- Current Assets (1100) -- 19
    ('1101','Cash on Hand',                1100,'BS','cash',       TRUE),
    ('1102','Petty Cash',                  1100,'BS','cash',       TRUE),
    ('1103','Bank Account - Operating',    1100,'BS','bank',       TRUE),
    ('1104','Bank Account - Payroll',      1100,'BS','bank',       TRUE),
    ('1105','Bank Account - Tax',          1100,'BS','bank',       TRUE),
    ('1110','Undeposited Funds',           1100,'BS','general',    TRUE),
    ('1120','Accounts Receivable',         1100,'BS','ar',         TRUE),
    ('1121','Allowance for Doubtful Debts',1100,'BS','general',    TRUE),
    ('1130','Employee Advances',           1100,'BS','general',    TRUE),
    ('1135','Vendor Advances',             1100,'BS','general',    TRUE),
    ('1140','Sales Tax Receivable',        1100,'BS','tax',        TRUE),
    ('1141','Sales Tax Refund Receivable', 1100,'BS','tax',        TRUE),
    ('1150','Prepaid Expenses',            1100,'BS','general',    TRUE),
    ('1160','Accrued Income',              1100,'BS','general',    TRUE),
    ('1170','Inventory - Raw Materials',   1100,'BS','inventory',  TRUE),
    ('1171','Inventory - WIP',             1100,'BS','inventory',  TRUE),
    ('1172','Inventory - Finished Goods',  1100,'BS','inventory',  TRUE),
    ('1173','Inventory - Trading Goods',   1100,'BS','inventory',  TRUE),
    ('1180','Short-term Investments',      1100,'BS','general',    TRUE),
    -- Fixed Assets (1200) -- 9
    ('1201','Land',                        1200,'BS','fixed_asset',TRUE),
    ('1202','Building',                    1200,'BS','fixed_asset',TRUE),
    ('1203','Office Equipment',            1200,'BS','fixed_asset',TRUE),
    ('1204','Computers',                   1200,'BS','fixed_asset',TRUE),
    ('1205','Furniture & Fixtures',        1200,'BS','fixed_asset',TRUE),
    ('1206','Vehicles',                    1200,'BS','fixed_asset',TRUE),
    ('1207','Plant & Machinery',           1200,'BS','fixed_asset',TRUE),
    ('1208','Leasehold Improvements',      1200,'BS','fixed_asset',TRUE),
    ('1210','Accumulated Depreciation',    1200,'BS','general',    TRUE),
    -- Intangible Assets (1300) -- 6
    ('1301','Software',                    1300,'BS','general',    TRUE),
    ('1302','ERP Development Cost',        1300,'BS','general',    TRUE),
    ('1303','Patents',                     1300,'BS','general',    TRUE),
    ('1304','Trademark',                   1300,'BS','general',    TRUE),
    ('1305','Goodwill',                    1300,'BS','general',    TRUE),
    ('1310','Accumulated Amortization',    1300,'BS','general',    TRUE),
    -- Current Liabilities (2100) -- 13
    ('2101','Accounts Payable',            2100,'BS','ap',         TRUE),
    ('2102','Credit Card Payable',         2100,'BS','credit_card',TRUE),
    ('2110','Accrued Expenses',            2100,'BS','general',    TRUE),
    ('2120','Salary Payable',              2100,'BS','general',    TRUE),
    ('2121','Bonus Payable',               2100,'BS','general',    TRUE),
    ('2122','Leave Encashment Payable',    2100,'BS','general',    TRUE),
    ('2130','Payroll Taxes Payable',       2100,'BS','general',    TRUE),
    ('2140','Sales Tax Payable',           2100,'BS','tax',        TRUE),
    ('2141','Withholding Tax Payable',     2100,'BS','tax',        TRUE),
    ('2150','Customer Advances',           2100,'BS','general',    TRUE),
    ('2160','Deferred Revenue',            2100,'BS','general',    TRUE),
    ('2170','Short-term Loan',             2100,'BS','general',    TRUE),
    ('2180','Interest Payable',            2100,'BS','general',    TRUE),
    -- Long-Term Liabilities (2200) -- 5
    ('2201','Bank Loan',                   2200,'BS','general',    TRUE),
    ('2202','Mortgage Loan',               2200,'BS','general',    TRUE),
    ('2203','Lease Liability',             2200,'BS','general',    TRUE),
    ('2204','Shareholder Loan',            2200,'BS','general',    TRUE),
    ('2205','Deferred Tax Liability',      2200,'BS','general',    TRUE),
    -- Equity (3100) -- 7
    ('3101','Capital',                     3100,'BS','general',    TRUE),
    ('3102','Partner''s Capital',          3100,'BS','general',    TRUE),
    ('3103','Share Capital',               3100,'BS','general',    TRUE),
    ('3110','Retained Earnings',           3100,'BS','general',    TRUE),
    ('3120','Current Year Earnings',       3100,'BS','general',    TRUE),
    ('3130','Additional Paid-in Capital',  3100,'BS','general',    TRUE),
    ('3140','Dividend Distribution',       3100,'BS','general',    TRUE),
    -- Sales (4100) -- 8
    ('4101','Product Sales',               4100,'PNL','general',   TRUE),
    ('4102','Service Revenue',             4100,'PNL','general',   TRUE),
    ('4103','Consulting Revenue',          4100,'PNL','general',   TRUE),
    ('4104','Subscription Revenue',        4100,'PNL','general',   TRUE),
    ('4105','Maintenance Revenue',         4100,'PNL','general',   TRUE),
    ('4106','Installation Revenue',        4100,'PNL','general',   TRUE),
    ('4107','Export Sales',                4100,'PNL','general',   TRUE),
    ('4108','Domestic Sales',              4100,'PNL','general',   TRUE),
    -- Returns, Discounts & Allowances (4200) -- 3
    ('4201','Sales Returns',               4200,'PNL','general',   TRUE),
    ('4202','Sales Discount',              4200,'PNL','general',   TRUE),
    ('4203','Sales Allowance',             4200,'PNL','general',   TRUE),
    -- Cost of Goods Sold (5100) -- 8
    ('5101','Opening Inventory',           5100,'PNL','general',   TRUE),
    ('5102','Purchases',                   5100,'PNL','general',   TRUE),
    ('5103','Direct Labor',                5100,'PNL','general',   TRUE),
    ('5104','Direct Material',             5100,'PNL','general',   TRUE),
    ('5105','Freight Inward',              5100,'PNL','general',   TRUE),
    ('5106','Manufacturing Overheads',     5100,'PNL','general',   TRUE),
    ('5107','Inventory Adjustment',        5100,'PNL','general',   TRUE),
    ('5108','Closing Inventory',           5100,'PNL','general',   TRUE),
    -- Payroll (6100) -- 5
    ('6101','Salaries',                    6100,'PNL','general',   TRUE),
    ('6102','Wages',                       6100,'PNL','general',   TRUE),
    ('6103','Payroll Taxes',               6100,'PNL','general',   TRUE),
    ('6104','Employee Benefits',           6100,'PNL','general',   TRUE),
    ('6105','Recruitment',                 6100,'PNL','general',   TRUE),
    -- Administrative (6200) -- 18
    ('6201','Rent',                        6200,'PNL','general',   TRUE),
    ('6202','Electricity',                 6200,'PNL','general',   TRUE),
    ('6203','Internet',                    6200,'PNL','general',   TRUE),
    ('6204','Telephone',                   6200,'PNL','general',   TRUE),
    ('6205','Office Supplies',             6200,'PNL','general',   TRUE),
    ('6206','Printing',                    6200,'PNL','general',   TRUE),
    ('6207','Repairs & Maintenance',       6200,'PNL','general',   TRUE),
    ('6208','Insurance',                   6200,'PNL','general',   TRUE),
    ('6209','Professional Fees',           6200,'PNL','general',   TRUE),
    ('6210','Audit Fees',                  6200,'PNL','general',   TRUE),
    ('6211','Legal Fees',                  6200,'PNL','general',   TRUE),
    ('6212','Bank Charges',                6200,'PNL','general',   TRUE),
    ('6213','Software Subscription',       6200,'PNL','general',   TRUE),
    ('6214','Travel',                      6200,'PNL','general',   TRUE),
    ('6215','Meals & Entertainment',       6200,'PNL','general',   TRUE),
    ('6216','Training',                    6200,'PNL','general',   TRUE),
    ('6217','Licenses',                    6200,'PNL','general',   TRUE),
    ('6218','Security',                    6200,'PNL','general',   TRUE),
    -- Sales & Marketing (6300) -- 5
    ('6301','Advertising',                 6300,'PNL','general',   TRUE),
    ('6302','Digital Marketing',           6300,'PNL','general',   TRUE),
    ('6303','Sales Commission',            6300,'PNL','general',   TRUE),
    ('6304','Promotional Expenses',        6300,'PNL','general',   TRUE),
    ('6305','Customer Gifts',              6300,'PNL','general',   TRUE),
    -- Logistics (6400) -- 3
    ('6401','Freight Outward',             6400,'PNL','general',   TRUE),
    ('6402','Courier Charges',             6400,'PNL','general',   TRUE),
    ('6403','Delivery Expenses',           6400,'PNL','general',   TRUE),
    -- Depreciation (6500) -- 2
    ('6501','Depreciation Expense',        6500,'PNL','general',   TRUE),
    ('6502','Amortization Expense',        6500,'PNL','general',   TRUE),
    -- Finance Costs (7100) -- 4
    ('7101','Interest Expense',            7100,'PNL','general',   TRUE),
    ('7102','Loan Processing Charges',     7100,'PNL','general',   TRUE),
    ('7103','Foreign Exchange Loss',       7100,'PNL','general',   TRUE),
    ('7104','Credit Card Charges',         7100,'PNL','general',   TRUE),
    -- Other Income (8100) -- 5
    ('8101','Interest Income',             8100,'PNL','general',   TRUE),
    ('8102','Dividend Income',             8100,'PNL','general',   TRUE),
    ('8103','Foreign Exchange Gain',       8100,'PNL','general',   TRUE),
    ('8104','Gain on Asset Sale',          8100,'PNL','general',   TRUE),
    ('8105','Miscellaneous Income',        8100,'PNL','general',   TRUE),
    -- System & Control (9100) -- 7. The ONLY sub-category mixing BS and PNL (AD-2).
    -- 9106 seeds INACTIVE: meaningless under the single-subsidiary policy.
    ('9101','Opening Balance Equity',      9100,'BS','general',    TRUE),
    ('9102','Suspense Account',            9100,'BS','general',    TRUE),
    ('9103','Rounding Adjustment',         9100,'PNL','general',   TRUE),
    ('9104','Inventory Adjustment',        9100,'PNL','general',   TRUE),
    ('9105','Exchange Rate Adjustment',    9100,'PNL','general',   TRUE),
    ('9106','Intercompany Clearing',       9100,'PNL','general',   FALSE),
    ('9107','Cash Difference',             9100,'PNL','general',   TRUE)
) AS v(code, name, subcat_code, bs_pnl, acct_type, active)
JOIN lkp_coa_subcategory s ON s.subcategory_code = v.subcat_code
ON CONFLICT DO NOTHING;
```

- [ ] **Step 4: Append the 19-slot seed**

Resolves the target account by code, so it stays independent of serial values:

```sql
INSERT INTO coa_default_mapping (slot_key, slot_label, slot_description, coa_account_id, slot_is_system, slot_sort_order)
SELECT v.key, v.label, v.descr, a.coa_account_id, TRUE, v.ord
FROM (VALUES
    ('default_ar',                  'Accounts Receivable',   'Customer balances owed to the company.',    '1120', 1),
    ('default_ap',                  'Accounts Payable',      'Balances owed to vendors.',                 '2101', 2),
    ('default_sales_revenue',       'Sales Revenue',         'Default revenue account for sales.',        '4101', 3),
    ('default_sales_discount',      'Sales Discount',        'Discounts granted on sales.',               '4202', 4),
    ('default_sales_returns',       'Sales Returns',         'Value of goods returned by customers.',     '4201', 5),
    ('default_cogs',                'Cost of Goods Sold',    'Default COGS account.',                     '5104', 6),
    ('default_inventory',           'Inventory',             'Default inventory asset account.',          '1172', 7),
    ('default_bank',                'Bank',                  'Default bank account for receipts.',        '1103', 8),
    ('default_undeposited_funds',   'Undeposited Funds',     'Holding account for uncleared receipts.',   '1110', 9),
    ('default_sales_tax_payable',   'Sales Tax Payable',     'Sales tax collected and owed.',             '2140',10),
    ('default_sales_tax_receivable','Sales Tax Receivable',  'Sales tax paid and recoverable.',           '1140',11),
    ('default_deferred_revenue',    'Deferred Revenue',      'Revenue billed but not yet earned.',        '2160',12),
    ('default_customer_advances',   'Customer Advances',     'Payments received before delivery.',        '2150',13),
    ('default_freight_out',         'Freight Outward',       'Outbound shipping cost.',                   '6401',14),
    ('default_bank_charges',        'Bank Charges',          'Bank fees.',                                '6212',15),
    ('default_fx_gain',             'Foreign Exchange Gain', 'Gain on currency conversion.',              '8103',16),
    ('default_fx_loss',             'Foreign Exchange Loss', 'Loss on currency conversion.',              '7103',17),
    ('default_rounding',            'Rounding Adjustment',   'Absorbs sub-cent rounding differences.',    '9103',18),
    ('default_suspense',            'Suspense',              'Holds entries pending correct classification.','9102',19)
) AS v(key, label, descr, acct_code, ord)
JOIN coa_account a ON a.coa_account_code = v.acct_code AND a.coa_account_deleted_at IS NULL
ON CONFLICT (slot_key) DO NOTHING;
```

- [ ] **Step 5: Verify counts and idempotency**

```bash
psql "$TEST_DATABASE_URL" -v ON_ERROR_STOP=1 --single-transaction -f database/migrations/tenant/schema.sql
```

Run it twice; both must succeed. Then:

```bash
psql "$TEST_DATABASE_URL" -tAc "SELECT (SELECT count(*) FROM coa_account), (SELECT count(*) FROM coa_default_mapping), (SELECT count(*) FROM coa_account WHERE NOT coa_account_is_active), (SELECT count(*) FROM coa_account WHERE coa_account_name='Inventory Adjustment')"
```

Expected: `127|19|1|2` — 127 accounts, 19 slots, exactly one inactive (9106), and **two** accounts named "Inventory Adjustment" (AD-3 proof).

- [ ] **Step 6: Commit**

```bash
git add database/migrations/tenant/schema.sql
git commit -m "feat(coa): add coa_account, history and default-mapping tables with 127-account seed"
```

---

## Task 3: RBAC catalog entries

**Files:**
- Modify: `authz/catalog.go`

**Interfaces:**
- Consumes: existing `Resource`, `Action`, `Permission` types
- Produces: `authz.ResourceChartOfAccount` — used by every controller in Task 12

- [ ] **Step 1: Add the resource constant**

In `authz/catalog.go`, add to the resource block (after `ResourceExpense`):

```go
	// Finance
	ResourceChartOfAccount Resource = "chart_of_account"
```

- [ ] **Step 2: Add the five catalog entries**

Add to the `catalog` slice. **No new `Action` constant is needed** — `ActionConfigure` already exists and covers repointing default slots (AD-10 removed the only reason to add one):

```go
	{ResourceChartOfAccount, ActionCreate},
	{ResourceChartOfAccount, ActionRead},
	{ResourceChartOfAccount, ActionUpdate},
	{ResourceChartOfAccount, ActionDelete},
	{ResourceChartOfAccount, ActionConfigure},
```

- [ ] **Step 3: Verify**

```bash
go build ./... && go test ./authz/... -v
```

Expected: build succeeds, all authz tests PASS.

- [ ] **Step 4: Commit**

```bash
git add authz/catalog.go
git commit -m "feat(coa): add chart_of_account resource to the permission catalog"
```

---

## Task 4: Types and errors

**Files:**
- Create: `chartofaccounts/types.go`
- Create: `chartofaccounts/errors.go`

**Interfaces:**
- Consumes: nothing
- Produces: `Account`, `Category`, `SubCategory`, `DefaultSlot`, `CreateInput`, `UpdateInput`, `BulkInput`, `HistoryEntry`; `ErrNotFound`, `ClientError`, `ConflictError`, `IsClientError`, `IsConflict`

- [ ] **Step 1: Write `types.go`**

```go
// Package chartofaccounts implements the Chart of Accounts master-data module:
// a fixed category/sub-category reference tree, 127 seeded accounts, a
// two-level user-extensible account tree, and named default-account mapping
// slots. It holds no balances -- the general ledger is a separate module.
//
// Spec: docs/superpowers/specs/2026-07-25-chart-of-accounts-design.md
package chartofaccounts

import "time"

// Account is one chart-of-accounts entry.
type Account struct {
	ID              string         `json:"id"` // uuid
	Code            string         `json:"code"`
	Name            string         `json:"name"`
	Description     string         `json:"description"`
	SubCategoryID   int            `json:"subCategoryId"`
	SubCategoryCode int            `json:"subCategoryCode"`
	SubCategoryName string         `json:"subCategoryName"`
	CategoryCode    int            `json:"categoryCode"`
	CategoryName    string         `json:"categoryName"`
	ParentID        *string        `json:"parentId,omitempty"` // uuid
	Depth           int            `json:"depth"`
	BSPNL           string         `json:"bsPnl"` // "BS" | "PNL"
	Type            string         `json:"type"`
	Attributes      map[string]any `json:"attributes"`
	IsPostable      bool           `json:"isPostable"`
	IsActive        bool           `json:"isActive"`
	IsVisible       bool           `json:"isVisible"`
	IsSystem        bool           `json:"isSystem"`
	RecordVersion   int            `json:"recordVersion"`
	CreatedAt       time.Time      `json:"createdAt"`
	UpdatedAt       time.Time      `json:"updatedAt"`
}

// Category is a fixed top-level classification (1000 Assets ... 9000 System).
type Category struct {
	ID            int    `json:"id"`
	Code          int    `json:"code"`
	Name          string `json:"name"`
	RangeLow      int    `json:"rangeLow"`
	RangeHigh     int    `json:"rangeHigh"`
	NormalBalance string `json:"normalBalance"` // "debit" | "credit"
	SortOrder     int    `json:"sortOrder"`
}

// SubCategory is a fixed second-level classification (1100 Current Assets ...).
type SubCategory struct {
	ID           int    `json:"id"`
	CategoryID   int    `json:"categoryId"`
	CategoryCode int    `json:"categoryCode"`
	Code         int    `json:"code"`
	Name         string `json:"name"`
	RangeLow     int    `json:"rangeLow"`
	RangeHigh    int    `json:"rangeHigh"`
	SortOrder    int    `json:"sortOrder"`
}

// DefaultSlot is a named mapping from a posting purpose to one account.
type DefaultSlot struct {
	Key         string    `json:"key"`
	Label       string    `json:"label"`
	Description string    `json:"description"`
	AccountID   *string   `json:"accountId,omitempty"` // uuid
	AccountCode string    `json:"accountCode,omitempty"`
	AccountName string    `json:"accountName,omitempty"`
	IsSystem    bool      `json:"isSystem"`
	SortOrder   int       `json:"sortOrder"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// CreateInput is the payload for creating an account. Code, depth and
// bs_pnl are server-assigned; bs_pnl is accepted ONLY under sub-category
// 9100, the one sub-category that mixes BS and PNL (AD-2).
type CreateInput struct {
	Name          string         `json:"name"`
	Description   string         `json:"description"`
	SubCategoryID int            `json:"subCategoryId"` // required when ParentID is empty
	ParentID      string         `json:"parentId"`      // uuid; empty means top-level
	BSPNL         string         `json:"bsPnl"`         // required only under 9100
	Type          string         `json:"type"`
	Attributes    map[string]any `json:"attributes"`
	IsPostable    *bool          `json:"isPostable"`
}

// UpdateInput is the payload for PATCHing an account. Nil fields are left
// unchanged. Code, sub-category and parent are immutable after create.
type UpdateInput struct {
	Name          *string        `json:"name"`
	Description   *string        `json:"description"`
	Type          *string        `json:"type"`
	Attributes    map[string]any `json:"attributes"`
	IsPostable    *bool          `json:"isPostable"`
	IsActive      *bool          `json:"isActive"`
	IsVisible     *bool          `json:"isVisible"`
	RecordVersion int            `json:"recordVersion"`
}

// BulkInput toggles visibility flags across many accounts in one transaction.
type BulkInput struct {
	UUIDs     []string `json:"uuids"`
	IsActive  *bool    `json:"isActive"`
	IsVisible *bool    `json:"isVisible"`
}

// BulkResult is the per-account outcome of a bulk update.
type BulkResult struct {
	UUID    string `json:"uuid"`
	OK      bool   `json:"ok"`
	Message string `json:"message,omitempty"`
}

// HistoryEntry is one audited change to an account or a default slot.
type HistoryEntry struct {
	ID        int       `json:"id"`
	AccountID *string   `json:"accountId,omitempty"`
	SlotKey   string    `json:"slotKey,omitempty"`
	Action    string    `json:"action"`
	Field     string    `json:"field"`
	OldValue  string    `json:"oldValue"`
	NewValue  string    `json:"newValue"`
	At        time.Time `json:"at"`
	By        *int      `json:"by,omitempty"`
}
```

- [ ] **Step 2: Write `errors.go`**

```go
package chartofaccounts

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
)

// ErrNotFound is returned when a uuid or slot key matches nothing live.
var ErrNotFound = errors.New("chart of accounts entry not found")

// ErrCipherUnavailable is returned when a bank account number is supplied but
// SECRET_ENCRYPTION_KEY is not configured. The store fails closed rather than
// storing the number in plaintext (AD-10), mirroring SSOOps.
var ErrCipherUnavailable = errors.New("secret encryption is not configured")

// ClientError signals a client-caused failure a controller maps to HTTP 400,
// mirroring inventory.ClientError.
type ClientError struct{ Msg string }

func (e ClientError) Error() string { return e.Msg }

// IsClientError reports whether err is a ClientError.
func IsClientError(err error) bool {
	var ce ClientError
	return errors.As(err, &ce)
}

// ConflictError signals a state clash a controller maps to HTTP 409: an
// account referenced by a default slot, a live child blocking a delete, an
// exhausted code range, or a record-version mismatch. BlockingSlots is
// populated only for the default-slot case (AD-7).
type ConflictError struct {
	Msg           string
	BlockingSlots []string
}

func (e ConflictError) Error() string { return e.Msg }

// IsConflict reports whether err is a ConflictError, returning it for its
// BlockingSlots.
func IsConflict(err error) (ConflictError, bool) {
	var ce ConflictError
	ok := errors.As(err, &ce)
	return ce, ok
}

// isUniqueViolation reports whether err is a PostgreSQL unique-constraint
// violation (23505). Kept local to avoid a store->controllers import,
// mirroring inventory.isUniqueViolation.
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
```

- [ ] **Step 3: Verify it compiles**

```bash
go build ./chartofaccounts/... && go vet ./chartofaccounts/...
```

Expected: no output (success).

- [ ] **Step 4: Commit**

```bash
git add chartofaccounts/types.go chartofaccounts/errors.go
git commit -m "feat(coa): add chart-of-accounts types and error taxonomy"
```

---

## Task 5: Account-type attribute validation

**Files:**
- Create: `chartofaccounts/attributes.go`
- Test: `chartofaccounts/attributes_test.go`

**Interfaces:**
- Consumes: `ClientError` from Task 4
- Produces: `ValidateAttributes(accountType string, attrs map[string]any) (map[string]any, error)`, `ValidAccountTypes() []string`, `BankAccountNumberKey` constant. Task 7 (`masking`) and Task 9 (`store_create`) both call `ValidateAttributes`.

- [ ] **Step 1: Write the failing test**

Create `chartofaccounts/attributes_test.go`:

```go
package chartofaccounts

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateAttributes(t *testing.T) {
	tests := []struct {
		name        string
		accountType string
		attrs       map[string]any
		wantErr     string // substring; empty means success
		wantKeys    []string
	}{
		{
			name:        "general accepts empty attributes",
			accountType: "general",
			attrs:       map[string]any{},
			wantKeys:    []string{},
		},
		{
			name:        "general rejects any key",
			accountType: "general",
			attrs:       map[string]any{"bankName": "HDFC"},
			wantErr:     "bankName",
		},
		{
			name:        "bank accepts the full set",
			accountType: "bank",
			attrs: map[string]any{
				"bankName": "HDFC", "branch": "NYC",
				"accountNumber": "1234567890124821",
				"routingNumber": "021000021", "swift": "HDFCUS33",
			},
			wantKeys: []string{"accountNumber", "bankName", "branch", "routingNumber", "swift"},
		},
		{
			name:        "bank requires bankName",
			accountType: "bank",
			attrs:       map[string]any{"accountNumber": "123456"},
			wantErr:     "bankName",
		},
		{
			name:        "bank requires accountNumber",
			accountType: "bank",
			attrs:       map[string]any{"bankName": "HDFC"},
			wantErr:     "accountNumber",
		},
		{
			name:        "bank rejects an unknown key",
			accountType: "bank",
			attrs: map[string]any{
				"bankName": "HDFC", "accountNumber": "123456", "iban": "GB33BUKB",
			},
			wantErr: "iban",
		},
		{
			name:        "bank rejects a non-string value",
			accountType: "bank",
			attrs:       map[string]any{"bankName": 42, "accountNumber": "123456"},
			wantErr:     "bankName",
		},
		{
			name:        "credit_card requires issuer and last4",
			accountType: "credit_card",
			attrs:       map[string]any{"issuer": "Amex", "last4": "1005"},
			wantKeys:    []string{"issuer", "last4"},
		},
		{
			name:        "credit_card rejects a missing last4",
			accountType: "credit_card",
			attrs:       map[string]any{"issuer": "Amex"},
			wantErr:     "last4",
		},
		{
			name:        "cash accepts an optional location",
			accountType: "cash",
			attrs:       map[string]any{"location": "Front desk"},
			wantKeys:    []string{"location"},
		},
		{
			name:        "tax accepts an optional registration number",
			accountType: "tax",
			attrs:       map[string]any{"taxRegistrationNumber": "US-123"},
			wantKeys:    []string{"taxRegistrationNumber"},
		},
		{
			name:        "unknown account type is rejected",
			accountType: "crypto_wallet",
			attrs:       map[string]any{},
			wantErr:     "crypto_wallet",
		},
		{
			name:        "nil attributes normalise to empty",
			accountType: "general",
			attrs:       nil,
			wantKeys:    []string{},
		},
		{
			name:        "blank required value is rejected",
			accountType: "bank",
			attrs:       map[string]any{"bankName": "   ", "accountNumber": "123456"},
			wantErr:     "bankName",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ValidateAttributes(tt.accountType, tt.attrs)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.True(t, IsClientError(err), "want ClientError, got %T", err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Len(t, got, len(tt.wantKeys))
			for _, k := range tt.wantKeys {
				assert.Contains(t, got, k)
			}
		})
	}
}

func TestValidAccountTypes(t *testing.T) {
	// Must match chk_coa_type in tenant/schema.sql exactly.
	assert.ElementsMatch(t, []string{
		"general", "bank", "cash", "credit_card", "ar",
		"ap", "tax", "inventory", "fixed_asset",
	}, ValidAccountTypes())
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
go test ./chartofaccounts/ -run TestValidateAttributes -v
```

Expected: FAIL — `undefined: ValidateAttributes`.

- [ ] **Step 3: Write the implementation**

Create `chartofaccounts/attributes.go`:

```go
package chartofaccounts

import (
	"fmt"
	"sort"
	"strings"
)

// BankAccountNumberKey is the one attribute encrypted at rest and never
// returned in full (AD-10). masking.go keys off this constant.
const BankAccountNumberKey = "accountNumber"

// attrField describes one allowed key in an account's attributes JSONB.
type attrField struct {
	required bool
}

// attrSchema is the FIXED, developer-defined field schema per account_type.
// This is deliberately NOT the workflow custom-fields mechanism (AD-9): there
// is no workflow_field_definitions row, no <=15 cap, and no per-tenant
// configurability. Keys absent from a type's map are rejected outright.
var attrSchema = map[string]map[string]attrField{
	"general":     {},
	"ar":          {},
	"ap":          {},
	"inventory":   {},
	"cash":        {"location": {}},
	"tax":         {"taxRegistrationNumber": {}, "jurisdiction": {}},
	"fixed_asset": {"assetTag": {}, "usefulLifeYears": {}},
	"bank": {
		"bankName":            {required: true},
		BankAccountNumberKey:  {required: true},
		"branch":              {},
		"routingNumber":       {},
		"swift":               {},
	},
	"credit_card": {
		"issuer": {required: true},
		"last4":  {required: true},
		"network": {},
	},
}

// ValidAccountTypes returns every permitted account_type, sorted. It must stay
// in sync with chk_coa_type in database/migrations/tenant/schema.sql.
func ValidAccountTypes() []string {
	out := make([]string, 0, len(attrSchema))
	for k := range attrSchema {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ValidateAttributes checks attrs against the fixed schema for accountType and
// returns the normalised map. Every value must be a non-blank string. Unknown
// keys, missing required keys, and non-string values are all ClientErrors so
// the controller renders them as 400 naming the offending field.
func ValidateAttributes(accountType string, attrs map[string]any) (map[string]any, error) {
	schema, ok := attrSchema[accountType]
	if !ok {
		return nil, ClientError{Msg: fmt.Sprintf(
			"Unknown account type %q. Valid types: %s.",
			accountType, strings.Join(ValidAccountTypes(), ", "))}
	}

	out := make(map[string]any, len(attrs))
	for k, v := range attrs {
		if _, allowed := schema[k]; !allowed {
			return nil, ClientError{Msg: fmt.Sprintf(
				"Attribute %q is not valid for a %q account.", k, accountType)}
		}
		s, isStr := v.(string)
		if !isStr {
			return nil, ClientError{Msg: fmt.Sprintf(
				"Attribute %q must be a string.", k)}
		}
		if strings.TrimSpace(s) == "" {
			// A blank value is the same as omitting the key. Required keys are
			// caught by the loop below.
			continue
		}
		out[k] = strings.TrimSpace(s)
	}

	// Deterministic ordering so the error message is stable across runs.
	required := make([]string, 0, len(schema))
	for k, f := range schema {
		if f.required {
			required = append(required, k)
		}
	}
	sort.Strings(required)
	for _, k := range required {
		if _, present := out[k]; !present {
			return nil, ClientError{Msg: fmt.Sprintf(
				"Attribute %q is required for a %q account.", k, accountType)}
		}
	}
	return out, nil
}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
go test ./chartofaccounts/ -run 'TestValidateAttributes|TestValidAccountTypes' -v
```

Expected: PASS — all 14 `TestValidateAttributes` subtests and `TestValidAccountTypes`.

- [ ] **Step 5: Commit**

```bash
git add chartofaccounts/attributes.go chartofaccounts/attributes_test.go
git commit -m "feat(coa): validate account attributes against the fixed per-type schema"
```

---

## Task 6: BS/PNL derivation

**Files:**
- Create: `chartofaccounts/bspnl.go`
- Test: `chartofaccounts/bspnl_test.go`

**Interfaces:**
- Consumes: `ClientError` from Task 4
- Produces: `DeriveBSPNL(subCategoryCode int, supplied string) (string, error)`, `MixedSubCategoryCode` constant. Task 9 (`store_create`) calls it.

- [ ] **Step 1: Write the failing test**

Create `chartofaccounts/bspnl_test.go`:

```go
package chartofaccounts

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDeriveBSPNL(t *testing.T) {
	tests := []struct {
		name     string
		subCode  int
		supplied string
		want     string
		wantErr  string
	}{
		// Derived, and anything supplied is ignored.
		{"current assets", 1100, "", "BS", ""},
		{"fixed assets", 1200, "", "BS", ""},
		{"intangible assets", 1300, "", "BS", ""},
		{"current liabilities", 2100, "", "BS", ""},
		{"long-term liabilities", 2200, "", "BS", ""},
		{"equity", 3100, "", "BS", ""},
		{"sales", 4100, "", "PNL", ""},
		{"returns and discounts", 4200, "", "PNL", ""},
		{"cogs", 5100, "", "PNL", ""},
		{"payroll", 6100, "", "PNL", ""},
		{"administrative", 6200, "", "PNL", ""},
		{"sales and marketing", 6300, "", "PNL", ""},
		{"logistics", 6400, "", "PNL", ""},
		{"depreciation", 6500, "", "PNL", ""},
		{"finance costs", 7100, "", "PNL", ""},
		{"other income", 8100, "", "PNL", ""},
		{"supplied value ignored outside 9100", 1100, "PNL", "BS", ""},

		// 9100 is the ONLY sub-category that mixes BS and PNL (AD-2).
		{"system requires an explicit value", 9100, "", "", "required"},
		{"system accepts BS", 9100, "BS", "BS", ""},
		{"system accepts PNL", 9100, "PNL", "PNL", ""},
		{"system rejects nonsense", 9100, "XX", "", "must be"},
		{"system rejects lowercase", 9100, "bs", "", "must be"},

		{"unknown sub-category", 9900, "", "", "9900"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := DeriveBSPNL(tt.subCode, tt.supplied)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.True(t, IsClientError(err), "want ClientError, got %T", err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
go test ./chartofaccounts/ -run TestDeriveBSPNL -v
```

Expected: FAIL — `undefined: DeriveBSPNL`.

- [ ] **Step 3: Write the implementation**

Create `chartofaccounts/bspnl.go`:

```go
package chartofaccounts

import "fmt"

// MixedSubCategoryCode is sub-category 9100 (System & Control Accounts) -- the
// only sub-category holding both balance-sheet accounts (9101 Opening Balance
// Equity, 9102 Suspense) and P&L accounts (9103-9107). Everywhere else BS/PNL
// follows the category, which is why the flag lives on the account (AD-2).
const MixedSubCategoryCode = 9100

// Balance-sheet vs profit-and-loss markers, matching chk_coa_bs_pnl.
const (
	BalanceSheet = "BS"
	ProfitAndLoss = "PNL"
)

// bsPnlBySubCategory maps every non-mixed sub-category to its fixed side.
var bsPnlBySubCategory = map[int]string{
	1100: BalanceSheet, 1200: BalanceSheet, 1300: BalanceSheet,
	2100: BalanceSheet, 2200: BalanceSheet,
	3100: BalanceSheet,
	4100: ProfitAndLoss, 4200: ProfitAndLoss,
	5100: ProfitAndLoss,
	6100: ProfitAndLoss, 6200: ProfitAndLoss, 6300: ProfitAndLoss,
	6400: ProfitAndLoss, 6500: ProfitAndLoss,
	7100: ProfitAndLoss,
	8100: ProfitAndLoss,
}

// DeriveBSPNL returns the BS/PNL side for an account in subCategoryCode.
//
// For every sub-category except 9100 the side is derived and any supplied
// value is ignored -- a user must not be able to file a revenue account on the
// balance sheet. Under 9100 the side is genuinely ambiguous, so supplied is
// required and must be exactly "BS" or "PNL".
func DeriveBSPNL(subCategoryCode int, supplied string) (string, error) {
	if side, ok := bsPnlBySubCategory[subCategoryCode]; ok {
		return side, nil
	}
	if subCategoryCode != MixedSubCategoryCode {
		return "", ClientError{Msg: fmt.Sprintf(
			"Unknown sub-category code %d.", subCategoryCode)}
	}
	switch supplied {
	case BalanceSheet, ProfitAndLoss:
		return supplied, nil
	case "":
		return "", ClientError{Msg: fmt.Sprintf(
			"bsPnl is required for sub-category %d (System & Control Accounts), "+
				"which contains both balance-sheet and P&L accounts.", MixedSubCategoryCode)}
	default:
		return "", ClientError{Msg: fmt.Sprintf(
			"bsPnl must be %q or %q, got %q.", BalanceSheet, ProfitAndLoss, supplied)}
	}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
go test ./chartofaccounts/ -run TestDeriveBSPNL -v
```

Expected: PASS — all 22 subtests.

- [ ] **Step 5: Commit**

```bash
git add chartofaccounts/bspnl.go chartofaccounts/bspnl_test.go
git commit -m "feat(coa): derive BS/PNL from sub-category with the 9100 mixed branch"
```

---

## Task 7: Account code allocation

**Files:**
- Create: `chartofaccounts/numbering.go`
- Test: `chartofaccounts/numbering_test.go`

**Interfaces:**
- Consumes: `ClientError`, `ConflictError` from Task 4
- Produces: `NextChildCode(parentCode string, taken []string) (string, error)`, `NextTopLevelCode(rangeLow, rangeHigh int, taken []string) (string, error)`, `MaxChildSuffix` constant. Task 9 (`store_create`) calls both.

- [ ] **Step 1: Write the failing test**

Create `chartofaccounts/numbering_test.go`:

```go
package chartofaccounts

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNextChildCode(t *testing.T) {
	tests := []struct {
		name       string
		parentCode string
		taken      []string
		want       string
		wantErr    string
	}{
		{"first child", "1103", nil, "1103.01", ""},
		{"second child", "1103", []string{"1103.01"}, "1103.02", ""},
		{"fills a gap left by a deleted child", "1103", []string{"1103.01", "1103.03"}, "1103.02", ""},
		{"ignores other parents' children", "1103", []string{"1104.01", "1105.01"}, "1103.01", ""},
		{"ignores the parent's own code", "1103", []string{"1103"}, "1103.01", ""},
		{"pads to two digits", "1103", []string{"1103.01", "1103.02", "1103.03", "1103.04",
			"1103.05", "1103.06", "1103.07", "1103.08"}, "1103.09", ""},
		{"crosses the ten boundary", "1103", func() []string {
			var s []string
			for i := 1; i <= 9; i++ {
				s = append(s, fmt.Sprintf("1103.%02d", i))
			}
			return s
		}(), "1103.10", ""},
		{"rejects a parent that is already a child", "1103.01", nil, "", "two levels"},
		{"rejects an empty parent code", "", nil, "", "parent"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NextChildCode(tt.parentCode, tt.taken)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestNextChildCodeExhausted(t *testing.T) {
	var taken []string
	for i := 1; i <= MaxChildSuffix; i++ {
		taken = append(taken, fmt.Sprintf("1103.%02d", i))
	}
	_, err := NextChildCode("1103", taken)
	require.Error(t, err)
	conflict, ok := IsConflict(err)
	require.True(t, ok, "want ConflictError, got %T", err)
	assert.Contains(t, conflict.Error(), "1103")
}

func TestNextTopLevelCode(t *testing.T) {
	tests := []struct {
		name    string
		lo, hi  int
		taken   []string
		want    string
		wantErr bool
	}{
		{"empty range starts at the low bound", 1100, 1199, nil, "1100", false},
		{"skips taken codes", 1100, 1199, []string{"1100", "1101"}, "1102", false},
		{"fills an interior gap", 1100, 1199, []string{"1100", "1102"}, "1101", false},
		{"ignores child codes in the range", 1100, 1199, []string{"1100", "1100.01"}, "1101", false},
		{"ignores codes outside the range", 1100, 1199, []string{"2100", "2101"}, "1100", false},
		{"uses the last slot", 1100, 1101, []string{"1100"}, "1101", false},
		{"exhausted range conflicts", 1100, 1101, []string{"1100", "1101"}, "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := NextTopLevelCode(tt.lo, tt.hi, tt.taken)
			if tt.wantErr {
				require.Error(t, err)
				_, ok := IsConflict(err)
				assert.True(t, ok, "want ConflictError, got %T", err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
go test ./chartofaccounts/ -run 'TestNextChildCode|TestNextTopLevelCode' -v
```

Expected: FAIL — `undefined: NextChildCode`.

- [ ] **Step 3: Write the implementation**

Create `chartofaccounts/numbering.go`:

```go
package chartofaccounts

import (
	"fmt"
	"strconv"
	"strings"
)

// MaxChildSuffix caps children per parent at 99. The suffix is zero-padded to
// two digits so codes sort lexically in the same order they sort numerically
// ("1103.09" < "1103.10"), which is what lets the report and the keyset cursor
// both order by code alone.
const MaxChildSuffix = 99

// childSeparator joins a parent code to its child suffix: 1103 -> 1103.01.
const childSeparator = "."

// NextChildCode returns the next free child code under parentCode, given every
// code currently taken anywhere in the tenant. Gaps left by deleted children
// are reused, so a tenant that repeatedly adds and removes bank accounts does
// not march toward the 99 ceiling.
//
// parentCode must itself be top-level: the tree is capped at two levels (AD-4).
func NextChildCode(parentCode string, taken []string) (string, error) {
	if strings.TrimSpace(parentCode) == "" {
		return "", ClientError{Msg: "A parent account code is required."}
	}
	if strings.Contains(parentCode, childSeparator) {
		return "", ClientError{Msg: fmt.Sprintf(
			"Account %s is already a child. The chart of accounts is limited to two levels.",
			parentCode)}
	}

	prefix := parentCode + childSeparator
	used := make(map[int]bool, len(taken))
	for _, code := range taken {
		suffix, ok := strings.CutPrefix(code, prefix)
		if !ok {
			continue
		}
		if n, err := strconv.Atoi(suffix); err == nil {
			used[n] = true
		}
	}

	for i := 1; i <= MaxChildSuffix; i++ {
		if !used[i] {
			return fmt.Sprintf("%s%s%02d", parentCode, childSeparator, i), nil
		}
	}
	return "", ConflictError{Msg: fmt.Sprintf(
		"Account %s already has the maximum of %d sub-accounts.", parentCode, MaxChildSuffix)}
}

// NextTopLevelCode returns the lowest free integer code in [rangeLow, rangeHigh],
// given every code currently taken. Child codes (those containing a separator)
// are ignored, since they never occupy an integer slot.
func NextTopLevelCode(rangeLow, rangeHigh int, taken []string) (string, error) {
	used := make(map[int]bool, len(taken))
	for _, code := range taken {
		if strings.Contains(code, childSeparator) {
			continue
		}
		if n, err := strconv.Atoi(code); err == nil {
			used[n] = true
		}
	}

	for i := rangeLow; i <= rangeHigh; i++ {
		if !used[i] {
			return strconv.Itoa(i), nil
		}
	}
	return "", ConflictError{Msg: fmt.Sprintf(
		"No account codes remain in the range %d-%d.", rangeLow, rangeHigh)}
}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
go test ./chartofaccounts/ -run 'TestNextChildCode|TestNextTopLevelCode' -v
```

Expected: PASS — all subtests including `TestNextChildCodeExhausted`.

- [ ] **Step 5: Commit**

```bash
git add chartofaccounts/numbering.go chartofaccounts/numbering_test.go
git commit -m "feat(coa): allocate account codes with gap reuse and range exhaustion guards"
```

---

## Task 8: Bank number masking

**Files:**
- Create: `chartofaccounts/masking.go`
- Test: `chartofaccounts/masking_test.go`

**Interfaces:**
- Consumes: `BankAccountNumberKey` (Task 5), `ErrCipherUnavailable` (Task 4), `secret.Cipher`
- Produces: `EncryptAttributes(c *secret.Cipher, attrs map[string]any) (map[string]any, error)`, `MaskAttributes(attrs map[string]any) map[string]any`, `Last4(s string) string`. Task 9 calls `EncryptAttributes`; Task 10's scanner calls `MaskAttributes`.

- [ ] **Step 1: Write the failing test**

Create `chartofaccounts/masking_test.go`:

```go
package chartofaccounts

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/secret"
)

func testCipher(t *testing.T) *secret.Cipher {
	t.Helper()
	key := base64.StdEncoding.EncodeToString([]byte("0123456789abcdef0123456789abcdef"))
	c, err := secret.New(key)
	require.NoError(t, err)
	return c
}

func TestLast4(t *testing.T) {
	tests := []struct{ name, in, want string }{
		{"long number", "1234567890124821", "4821"},
		{"exactly four", "4821", "4821"},
		{"three digits", "821", "821"},
		{"one digit", "8", "8"},
		{"empty", "", ""},
		{"trailing spaces trimmed", "1234567890124821  ", "4821"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, Last4(tt.in))
		})
	}
}

func TestEncryptAttributesRoundTrip(t *testing.T) {
	c := testCipher(t)
	in := map[string]any{"bankName": "HDFC", BankAccountNumberKey: "1234567890124821"}

	enc, err := EncryptAttributes(c, in)
	require.NoError(t, err)

	// Non-sensitive keys pass through untouched.
	assert.Equal(t, "HDFC", enc["bankName"])
	// The plaintext number is gone, replaced by ciphertext plus a last-4 hint.
	assert.NotEqual(t, "1234567890124821", enc[BankAccountNumberKey])
	assert.Equal(t, "4821", enc["accountNumberLast4"])

	back, err := c.Decrypt(enc[BankAccountNumberKey].(string))
	require.NoError(t, err)
	assert.Equal(t, "1234567890124821", back)
}

func TestEncryptAttributesFailsClosedWithoutCipher(t *testing.T) {
	_, err := EncryptAttributes(nil, map[string]any{BankAccountNumberKey: "1234567890124821"})
	require.ErrorIs(t, err, ErrCipherUnavailable)
}

func TestEncryptAttributesNoCipherNeededWithoutBankNumber(t *testing.T) {
	got, err := EncryptAttributes(nil, map[string]any{"bankName": "HDFC"})
	require.NoError(t, err)
	assert.Equal(t, map[string]any{"bankName": "HDFC"}, got)
}

func TestMaskAttributesRemovesCiphertext(t *testing.T) {
	masked := MaskAttributes(map[string]any{
		"bankName":             "HDFC",
		BankAccountNumberKey:   "ZW5jcnlwdGVkLWJsb2I=",
		"accountNumberLast4":   "4821",
	})
	assert.Equal(t, "HDFC", masked["bankName"])
	assert.Equal(t, "4821", masked["accountNumberLast4"])
	assert.NotContains(t, masked, BankAccountNumberKey,
		"ciphertext must never leave the store layer")
}

func TestMaskedAttributesNeverSerialiseTheNumber(t *testing.T) {
	c := testCipher(t)
	enc, err := EncryptAttributes(c, map[string]any{
		"bankName": "HDFC", BankAccountNumberKey: "1234567890124821",
	})
	require.NoError(t, err)

	blob, err := json.Marshal(Account{Attributes: MaskAttributes(enc)})
	require.NoError(t, err)
	assert.NotContains(t, string(blob), "1234567890124821")
	assert.NotContains(t, string(blob), enc[BankAccountNumberKey].(string))
	assert.Contains(t, string(blob), "4821")
}

func TestMaskAttributesNil(t *testing.T) {
	assert.Equal(t, map[string]any{}, MaskAttributes(nil))
}
```

- [ ] **Step 2: Run the test to verify it fails**

```bash
go test ./chartofaccounts/ -run 'TestLast4|TestEncryptAttributes|TestMask' -v
```

Expected: FAIL — `undefined: Last4`.

- [ ] **Step 3: Write the implementation**

Create `chartofaccounts/masking.go`:

```go
package chartofaccounts

import (
	"fmt"
	"strings"

	"stonesuite-backend/secret"
)

// accountNumberLast4Key holds the only part of a bank account number the API
// ever returns. It is written alongside the ciphertext at encrypt time so
// reads never need the cipher at all.
const accountNumberLast4Key = "accountNumberLast4"

// last4Length is how much of an account number is safe to surface.
const last4Length = 4

// Last4 returns the final four characters of s, or all of s when it is
// shorter. Surrounding whitespace is ignored.
func Last4(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= last4Length {
		return s
	}
	return s[len(s)-last4Length:]
}

// EncryptAttributes replaces a plaintext bank account number with its
// ciphertext and records a last-4 hint beside it. Every other key passes
// through untouched.
//
// It fails closed with ErrCipherUnavailable when an account number is supplied
// but no cipher is configured (no SECRET_ENCRYPTION_KEY), mirroring how SSOOps
// refuses to store client secrets in plaintext. Callers map this to 503 (AD-10).
func EncryptAttributes(c *secret.Cipher, attrs map[string]any) (map[string]any, error) {
	out := make(map[string]any, len(attrs)+1)
	for k, v := range attrs {
		out[k] = v
	}

	raw, ok := out[BankAccountNumberKey].(string)
	if !ok || raw == "" {
		return out, nil
	}
	if c == nil {
		return nil, ErrCipherUnavailable
	}

	ct, err := c.Encrypt(raw)
	if err != nil {
		return nil, fmt.Errorf("encrypt bank account number: %w", err)
	}
	out[BankAccountNumberKey] = ct
	out[accountNumberLast4Key] = Last4(raw)
	return out, nil
}

// MaskAttributes strips the encrypted account number, leaving only the last-4
// hint. Every read path runs attributes through this before they reach a
// response, so the ciphertext never leaves the store layer and there is no
// unmask path to guard (AD-10).
func MaskAttributes(attrs map[string]any) map[string]any {
	out := make(map[string]any, len(attrs))
	for k, v := range attrs {
		if k == BankAccountNumberKey {
			continue
		}
		out[k] = v
	}
	return out
}
```

- [ ] **Step 4: Run the tests to verify they pass**

```bash
go test ./chartofaccounts/ -run 'TestLast4|TestEncryptAttributes|TestMask' -v
```

Expected: PASS — every subtest, including `TestMaskedAttributesNeverSerialiseTheNumber`.

- [ ] **Step 5: Commit**

```bash
git add chartofaccounts/masking.go chartofaccounts/masking_test.go
git commit -m "feat(coa): encrypt bank account numbers and mask them to last-4 on read"
```

---

## Remaining tasks

Tasks 9–17 cover the store, tree assembly, controllers, routing, and the
database-backed suite. They are specified in the continuation document:

`docs/superpowers/plans/2026-07-25-chart-of-accounts-part-2.md`

| Task | Deliverable |
|---|---|
| 9 | `resolver.go` + `store.go` — field whitelist, shared SELECT, row scanners |
| 10 | `store_get.go` — Get, Search/List, Categories |
| 11 | `tree.go` — flat rows → BS/PNL > category > sub-category > account > children |
| 12 | `store_create.go` — allocation, validation, encryption, history |
| 13 | `store_guard.go` + `store_update.go` — the AD-7 guard and its first caller |
| 14 | `store_delete.go` + `store_bulk.go` — the guard's other three callers |
| 15 | `store_defaults.go` + `store_history.go` — slots and audit trail |
| 16 | `controllers/*` + `main.go` — 12 routes behind the full security chain |
| 17 | `-tags dbtest` suite — seed counts, idempotency, every constraint |
