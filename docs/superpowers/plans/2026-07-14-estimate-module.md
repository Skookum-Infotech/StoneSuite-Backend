# Estimate Module Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the `estimate` Go package + `controllers/estimate.go` + schema migration, giving StoneSuite a production-grade Estimate module (header + line items + status workflow + configuration-driven approval + revision history), fully mirroring the existing `salesorder` module's conventions.

**Architecture:** A dedicated v2 relational module — hybrid PK, employee-audited, soft-deleted, `record_version`-guarded — built as an almost line-for-line structural mirror of `salesorder/` (which this plan reads from directly), minus the SO-specific inventory-allocation/fulfillment/payment-due-date logic (Estimates don't reserve stock or track AR due dates), plus the AD-10-style approval gate. Package: `estimate/`. Controller: `controllers/estimate.go`. Routes registered in `main.go` alongside the Sales Order/Invoice blocks.

**Tech Stack:** Go, `pgx/v5`, PostgreSQL (per-tenant DB), the shared `query/` filter-and-pagination engine, stdlib `testing` (table-driven, no testify — matches this codebase).

**Spec:** `docs/superpowers/specs/2026-07-14-estimates-quotes-module-design.md` — this plan implements Estimate only (§3, §5.1-§5.4, §5.10 estimate rows, §7 Estimate transitions, §8, §10 Estimate rows, §11, §12, §13 estimate rows). The Quote module and both conversions (§9) are a separate plan (`docs/superpowers/plans/2026-07-14-quote-module.md`, built after this one, since Quote's Estimate→Quote conversion depends on this module existing).

## Global Constraints

- Database-per-tenant; no `tenant_id` column anywhere.
- Hybrid PK (`SERIAL` internal + `UUID` external), `employee(employee_id)`-based audit columns (`{x}_created_by`/`_updated_by`), paired soft delete (`{x}_deleted_at`/`_deleted_by` both-null-or-both-set, CHECK-enforced), `{x}_record_version INTEGER NOT NULL DEFAULT 1` bumped on every UPDATE.
- Idempotent, append-only migrations only: `CREATE TABLE IF NOT EXISTS`, `CREATE INDEX IF NOT EXISTS`. **Never `ALTER TABLE ADD COLUMN` on a table that already exists in this codebase.**
- Mandatory security chain on every `/api/tenant/` route: `RequireAuth → per-tenant rate limit → TenantResolver → RBAC check (authz.Check) → scope filter → IDOR guard (404 on denial) → security logging (logSecurityEvent)`.
- All list/search goes through `query/`: whitelisted `FieldResolver`, parameterized values, keyset pagination (`EffLimit+1` fetch, opaque cursor), filter × scope ANDed (never OR).
- Money: `DECIMAL(15,2)`; quantity `DECIMAL(14,3)`; percent `DECIMAL(6,4)`; exchange rate `DECIMAL(18,6)`. All money rounded via `round2` (`math.Round(x*100)/100`).
- No panics; every error wrapped with `%w` and context; `context.Context` is always the first parameter; no testify — plain stdlib table-driven tests.
- `authz.ResourceEstimate` and all 5 actions (`create/read/update/delete/transition`) are **already seeded** in `authz/catalog.go:36,120-124` — this plan makes **zero** changes to `authz/catalog.go`.
- `lkp_record_type` row `ESTM` (id 4) and `lkp_record_status` rows for `record_type=4` (`DRFT, PAPV, APPV, SENT, CANC, RJCT, EXPR`) are **already seeded** in `database/migrations/tenant/schema.sql:694,730-731` — this plan does not touch those seed rows, only adds new tables that reference them.

---

### Task 1: Schema migration — `estimate` table set

**Files:**
- Modify: `database/migrations/tenant/schema.sql` (append after the `sales_order` block, i.e. after the existing `sales_order_history`/`inventory_allocation` indexes around line 2690 — append at end of file if that's simpler; either position is valid since these are brand-new tables with no ordering dependency on anything after `lkp_record_type`/`lkp_record_status`/`customer`/`employee`/`inventory_item`/`lkp_unit`/`lkp_tax_rate`/`lkp_payment_terms`/`lkp_price_level`/`lkp_currency`/`lkp_state`/`lkp_country`, all of which already exist earlier in the file)

**Interfaces:**
- Produces: tables `estimate`, `estimate_item`, `estimate_history`, `estimate_approver`, `estimate_approval` with the exact column names used by every later task in this plan (`estimate_id`, `estimate_uuid`, `estimate_number`, `estimate_status`, `estimate_customer_id`, etc. — see DDL below, copied verbatim from spec §5.1-§5.4).

- [ ] **Step 1: Append the DDL**

Add this block to the end of `database/migrations/tenant/schema.sql`:

```sql
-- ── Estimate module (docs/superpowers/specs/2026-07-14-estimates-quotes-module-design.md) ──

CREATE TABLE IF NOT EXISTS estimate (
    estimate_id                  SERIAL        PRIMARY KEY,
    estimate_uuid                UUID          NOT NULL DEFAULT gen_random_uuid(),
    ss_customer_id                INTEGER          NULL,
    estimate_number               VARCHAR(20)      NULL,

    record_type                   INTEGER       NOT NULL REFERENCES lkp_record_type(record_type_id),
    estimate_status                INTEGER       NOT NULL REFERENCES lkp_record_status(record_status_id),

    estimate_approval_status       VARCHAR(10)   NOT NULL DEFAULT 'none',
    estimate_approved_by           INTEGER           NULL REFERENCES employee(employee_id),

    estimate_customer_id           INTEGER       NOT NULL REFERENCES customer(customer_id),
    estimate_po_number             VARCHAR(50)   NOT NULL DEFAULT '',
    estimate_reference_number      VARCHAR(50)   NOT NULL DEFAULT '',
    estimate_date                  DATE          NOT NULL DEFAULT CURRENT_DATE,
    estimate_valid_until           DATE              NULL,
    estimate_sales_tax_percent     DECIMAL(6,4)  NOT NULL DEFAULT 0,
    estimate_memo                  TEXT          NOT NULL DEFAULT '',
    estimate_notes                 TEXT          NOT NULL DEFAULT '',
    estimate_internal_notes        TEXT          NOT NULL DEFAULT '',
    estimate_terms_conditions      TEXT          NOT NULL DEFAULT '',

    estimate_sales_rep_id          INTEGER           NULL REFERENCES employee(employee_id),
    estimate_owner_id              INTEGER           NULL REFERENCES employee(employee_id),

    estimate_payment_terms         INTEGER           NULL REFERENCES lkp_payment_terms(payment_terms_id),
    estimate_price_level           INTEGER           NULL REFERENCES lkp_price_level(price_level_id),
    estimate_currency              INTEGER           NULL REFERENCES lkp_currency(currency_id),
    estimate_exchange_rate         DECIMAL(18,6) NOT NULL DEFAULT 1,

    estimate_subtotal              DECIMAL(15,2) NOT NULL DEFAULT 0,
    estimate_discount_total        DECIMAL(15,2) NOT NULL DEFAULT 0,
    estimate_tax_total             DECIMAL(15,2) NOT NULL DEFAULT 0,
    estimate_shipping_charge       DECIMAL(15,2) NOT NULL DEFAULT 0,
    estimate_adjustment            DECIMAL(15,2) NOT NULL DEFAULT 0,
    estimate_grand_total           DECIMAL(15,2) NOT NULL DEFAULT 0,

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

CREATE TABLE IF NOT EXISTS estimate_item (
    estimate_item_id          SERIAL        PRIMARY KEY,
    estimate_item_uuid        UUID          NOT NULL DEFAULT gen_random_uuid(),
    estimate_id                INTEGER       NOT NULL REFERENCES estimate(estimate_id) ON DELETE CASCADE,
    line_number                 INTEGER      NOT NULL,
    inventory_item_id           INTEGER          NULL REFERENCES inventory_item(inventory_item_id),

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

CREATE TABLE IF NOT EXISTS estimate_history (
    estimate_history_id       SERIAL       PRIMARY KEY,
    estimate_id                 INTEGER      NOT NULL REFERENCES estimate(estimate_id) ON DELETE CASCADE,
    from_status_id               INTEGER          NULL REFERENCES lkp_record_status(record_status_id),
    to_status_id                  INTEGER          NULL REFERENCES lkp_record_status(record_status_id),
    action                        VARCHAR(32)  NOT NULL DEFAULT 'transition',
    actor_employee_id              INTEGER          NULL REFERENCES employee(employee_id),
    snapshot                       JSONB        NOT NULL DEFAULT '{}',
    at                             TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS estimate_approver (
    estimate_approver_id    SERIAL      PRIMARY KEY,
    record_type_id          INTEGER     NOT NULL REFERENCES lkp_record_type(record_type_id),
    record_status_id        INTEGER     NOT NULL REFERENCES lkp_record_status(record_status_id),
    approver_employee_id    INTEGER     NOT NULL REFERENCES employee(employee_id),
    is_active                BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at                TIMESTAMP   NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_by                INTEGER         NULL REFERENCES employee(employee_id),
    CONSTRAINT uq_estimate_approver UNIQUE (record_type_id, record_status_id, approver_employee_id)
);

CREATE TABLE IF NOT EXISTS estimate_approval (
    estimate_approval_id    SERIAL      PRIMARY KEY,
    estimate_id              INTEGER     NOT NULL REFERENCES estimate(estimate_id) ON DELETE CASCADE,
    record_status_id         INTEGER     NOT NULL REFERENCES lkp_record_status(record_status_id),
    approver_employee_id     INTEGER     NOT NULL REFERENCES employee(employee_id),
    approved_at               TIMESTAMP   NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT uq_estimate_approval UNIQUE (estimate_id, record_status_id, approver_employee_id)
);

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
```

- [ ] **Step 2: Verify the migration applies cleanly**

Run (requires a scratch/dev Postgres reachable at `$TEST_DATABASE_URL`; if unavailable, skip to Step 3 — Task 9's `dbtest`-gated tests will catch any SQL error the first time they run):

```bash
psql "$TEST_DATABASE_URL" -f database/migrations/tenant/schema.sql
```

Expected: exits 0. `NOTICE` lines about pre-existing objects are fine (idempotent `IF NOT EXISTS`); any `ERROR` line is a failure to fix before continuing.

- [ ] **Step 3: Confirm the repo still builds** (the file is `//go:embed`'d by `database/tenant_migrations.go:14`, so a missing/renamed file would break the build, though SQL syntax errors won't surface until Step 2's exec)

Run: `go build ./...`
Expected: exits 0, no output.

- [ ] **Step 4: Commit**

```bash
git add database/migrations/tenant/schema.sql
git commit -m "feat(estimate): add estimate schema (header, items, history, approval)"
```

---

### Task 2: `estimate/types.go` — domain types

**Files:**
- Create: `estimate/types.go`

**Interfaces:**
- Produces: `AddressInput`, `LineInput`, `estimateFields`, `CreateEstimateInput`, `UpdateEstimateInput`, `CustomerRef`, `Line`, `Estimate`, `Page` — the exact types every later task in this package imports.

- [ ] **Step 1: Write the file**

```go
package estimate

import "time"

// AddressInput is a billing or shipping snapshot block on create/update. All
// fields are optional; Create fills gaps from the referenced customer's
// matching address (spec AD-4) when the caller does not override them.
type AddressInput struct {
	CustomerName string `json:"customerName"`
	Attention    string `json:"attention"`
	AddrLine1    string `json:"addrLine1"`
	AddrLine2    string `json:"addrLine2"`
	SuiteUnit    string `json:"suiteUnit"`
	City         string `json:"city"`
	StateID      *int   `json:"stateId"`
	CountryID    *int   `json:"countryId"`
	Zip          string `json:"zip"`
	Phone        string `json:"phone"`
	Fax          string `json:"fax"`
	Email        string `json:"email"`
}

// LineInput is one estimated line on create/update. InventoryItemUUID selects
// a catalog item (the server snapshots its sku/name/description/unit/price/
// tax); omit it for a free-text line, in which case Description is required.
type LineInput struct {
	LineNumber        int     `json:"lineNumber"`
	InventoryItemUUID string  `json:"inventoryItemUuid"`
	Description       string  `json:"description"`
	Quantity          float64 `json:"quantity"`
	UnitPrice         float64 `json:"unitPrice"`
	DiscountPercent   float64 `json:"discountPercent"`
	TaxRateID         *int    `json:"taxRateId"`
}

// estimateFields is the header payload shared by create and update (everything
// except the customer, which is fixed at creation and never changes).
type estimateFields struct {
	PONumber           string         `json:"poNumber"`
	ReferenceNumber    string         `json:"referenceNumber"`
	EstimateDate       string         `json:"estimateDate"` // "yyyy-mm-dd"
	ValidUntil         string         `json:"validUntil"`   // "yyyy-mm-dd"
	PaymentTermsID     *int           `json:"paymentTermsId"`
	PriceLevelID       *int           `json:"priceLevelId"`
	CurrencyID         *int           `json:"currencyId"`
	SalesRepEmployeeID *int           `json:"salesRepEmployeeId"`
	OwnerEmployeeID    *int           `json:"ownerEmployeeId"`
	SalesTaxPercent    float64        `json:"salesTaxPercent"`
	Memo               string         `json:"memo"`
	Notes              string         `json:"notes"`
	InternalNotes      string         `json:"internalNotes"`
	TermsConditions    string         `json:"termsConditions"`
	ShipSameAsBilling  bool           `json:"shipSameAsBilling"`
	Billing            AddressInput   `json:"billing"`
	Shipping           AddressInput   `json:"shipping"`
	ShippingCharge     float64        `json:"shippingCharge"`
	Adjustment         float64        `json:"adjustment"`
	CustomFields       map[string]any `json:"customFields"`
	Items              []LineInput    `json:"items"`
}

// CreateEstimateInput is the create-request payload (spec §10).
type CreateEstimateInput struct {
	CustomerUUID string `json:"customerUuid"`
	estimateFields
}

// UpdateEstimateInput mirrors CreateEstimateInput minus the customer (an
// estimate's customer is fixed after creation).
type UpdateEstimateInput struct {
	estimateFields
}

// CustomerRef is the light customer reference on an Estimate response.
type CustomerRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Line is one estimated line in the API response — the frozen snapshot values
// (spec AD-4), not live inventory_item data.
type Line struct {
	ID              string  `json:"id"`
	LineNumber      int     `json:"lineNumber"`
	InventoryItemID *string `json:"inventoryItemId,omitempty"`
	SKU             string  `json:"sku"`
	ItemName        string  `json:"itemName"`
	Description     string  `json:"description"`
	UnitCode        string  `json:"unitCode"`
	Quantity        float64 `json:"quantity"`
	UnitPrice       float64 `json:"unitPrice"`
	DiscountPercent float64 `json:"discountPercent"`
	TaxPercent      float64 `json:"taxPercent"`
	LineSubtotal    float64 `json:"lineSubtotal"`
	LineDiscount    float64 `json:"lineDiscount"`
	LineTax         float64 `json:"lineTax"`
	LineTotal       float64 `json:"lineTotal"`
}

// Estimate is the full API response for an estimate header (+ lines, when
// loaded by Get). OwnerUserID backs the controller's IDOR scope check and is
// never serialized.
type Estimate struct {
	ID              string      `json:"id"`
	Number          string      `json:"estimateNumber"`
	Status          string      `json:"status"`         // human label, e.g. "Draft"
	StatusCode      string      `json:"statusCode"`     // lkp_record_status code, e.g. "DRFT"
	ApprovalStatus  string      `json:"approvalStatus"` // none | pending | approved
	Customer        CustomerRef `json:"customer"`
	OwnerUserID     string      `json:"-"`
	EstimateDate    string      `json:"estimateDate"`
	ValidUntil      string      `json:"validUntil,omitempty"`
	PONumber        string      `json:"poNumber,omitempty"`
	ReferenceNumber string      `json:"referenceNumber,omitempty"`
	Memo            string      `json:"memo,omitempty"`
	Notes           string      `json:"notes,omitempty"`
	InternalNotes   string      `json:"internalNotes,omitempty"`
	TermsConditions string      `json:"termsConditions,omitempty"`

	PaymentTermsID     *int    `json:"paymentTermsId"`
	PriceLevelID       *int    `json:"priceLevelId"`
	CurrencyID         *int    `json:"currencyId"`
	SalesRepEmployeeID *int    `json:"salesRepEmployeeId"`
	OwnerEmployeeID    *int    `json:"ownerEmployeeId"`
	SalesTaxPercent    float64 `json:"salesTaxPercent"`

	ShipSameAsBilling bool         `json:"shipSameAsBilling"`
	Billing           AddressInput `json:"billing"`
	Shipping          AddressInput `json:"shipping"`

	CustomFields map[string]any `json:"customFields,omitempty"`

	Subtotal       float64   `json:"subtotal"`
	DiscountTotal  float64   `json:"discountTotal"`
	TaxTotal       float64   `json:"taxTotal"`
	ShippingCharge float64   `json:"shippingCharge"`
	Adjustment     float64   `json:"adjustment"`
	GrandTotal     float64   `json:"grandTotal"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
	Items          []Line    `json:"items,omitempty"`
}

// Page is one page of a keyset-paginated estimate search. List rows omit
// Items (search selects header columns only, to avoid an N+1 line-item join)
// — only Get loads the full estimate with lines.
type Page struct {
	Records    []Estimate
	NextCursor string
	HasMore    bool
}
```

- [ ] **Step 2: Verify it compiles**

Run: `go build ./estimate/...`
Expected: exits 0 (no other files reference these types yet, so this only checks the file itself parses and type-checks).

- [ ] **Step 3: Commit**

```bash
git add estimate/types.go
git commit -m "feat(estimate): add estimate package domain types"
```

---

### Task 3: `estimate/calc.go` — money calculation (TDD)

**Files:**
- Create: `estimate/calc.go`
- Test: `estimate/calc_test.go`

**Interfaces:**
- Consumes: nothing (pure functions).
- Produces: `round2(x float64) float64`, `LineInput{Quantity, UnitPrice, DiscountPercent, TaxPercent float64}`, `LineMoney{Subtotal, Discount, Tax, Total float64}`, `ComputeLine(in LineInput) LineMoney`, `HeaderMoney{Subtotal, DiscountTotal, TaxTotal, GrandTotal float64}`, `ComputeHeader(lines []LineMoney, shipping, adjustment float64) HeaderMoney`. Later tasks (`store.go`, `store_create.go`) call `ComputeLine`/`ComputeHeader` directly.

> Note: this task's `LineInput` is a different type than `types.go`'s `LineInput` (the API request line shape) — this mirrors `salesorder`'s exact naming, where `calc.go`'s `LineInput` (quantity/price/discount/tax only) and `types.go`'s `LineInput2` (full API line) coexist as distinct types in the same package. Since this plan already used `LineInput` in `types.go` for the API shape, this task's calc-only type is named `CalcLineInput` instead, to avoid a name collision within the same package — **this is a deliberate, documented deviation from `salesorder`'s naming** (which avoids the collision by naming the calc type `LineInput` and the API type `LineInput2` instead — an inconsistency this plan does not repeat).

- [ ] **Step 1: Write the failing test**

```go
// estimate/calc_test.go
package estimate

import "testing"

func TestComputeLine(t *testing.T) {
	tests := []struct {
		name string
		in   CalcLineInput
		want LineMoney
	}{
		{
			name: "no discount no tax",
			in:   CalcLineInput{Quantity: 2, UnitPrice: 10, DiscountPercent: 0, TaxPercent: 0},
			want: LineMoney{Subtotal: 20, Discount: 0, Tax: 0, Total: 20},
		},
		{
			name: "discount and tax",
			in:   CalcLineInput{Quantity: 3, UnitPrice: 100, DiscountPercent: 10, TaxPercent: 8.25},
			// subtotal=300, discount=30, taxable=270, tax=22.28 (270*0.0825=22.275 -> round 22.28), total=292.28
			want: LineMoney{Subtotal: 300, Discount: 30, Tax: 22.28, Total: 292.28},
		},
		{
			name: "fractional quantity",
			in:   CalcLineInput{Quantity: 2.5, UnitPrice: 19.99, DiscountPercent: 5, TaxPercent: 0},
			// subtotal=49.975 -> round 49.98(?) — round2(2.5*19.99)=round2(49.975)=49.98
			want: LineMoney{Subtotal: 49.98, Discount: 2.5, Tax: 0, Total: 47.48},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ComputeLine(tt.in); got != tt.want {
				t.Fatalf("ComputeLine(%+v) = %+v, want %+v", tt.in, got, tt.want)
			}
		})
	}
}

func TestComputeHeader(t *testing.T) {
	tests := []struct {
		name                 string
		lines                []LineMoney
		shipping, adjustment float64
		want                 HeaderMoney
	}{
		{
			name:  "single line no shipping no adjustment",
			lines: []LineMoney{{Subtotal: 100, Discount: 10, Tax: 7.2, Total: 97.2}},
			want:  HeaderMoney{Subtotal: 100, DiscountTotal: 10, TaxTotal: 7.2, GrandTotal: 97.2},
		},
		{
			name: "multiple lines with shipping and adjustment",
			lines: []LineMoney{
				{Subtotal: 100, Discount: 10, Tax: 7.2, Total: 97.2},
				{Subtotal: 50, Discount: 0, Tax: 4.13, Total: 54.13},
			},
			shipping:   15,
			adjustment: -5,
			// subtotal=150, discount=10, tax=11.33, grand=150-10+11.33+15-5=161.33
			want: HeaderMoney{Subtotal: 150, DiscountTotal: 10, TaxTotal: 11.33, GrandTotal: 161.33},
		},
		{
			name:  "no lines",
			lines: nil,
			want:  HeaderMoney{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ComputeHeader(tt.lines, tt.shipping, tt.adjustment); got != tt.want {
				t.Fatalf("ComputeHeader(%+v, %v, %v) = %+v, want %+v", tt.lines, tt.shipping, tt.adjustment, got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run the test to verify it fails**

Run: `go test ./estimate/... -run TestComputeLine -v`
Expected: FAIL — `CalcLineInput`, `LineMoney`, `ComputeLine` undefined (package doesn't compile yet).

- [ ] **Step 3: Write the implementation**

```go
// estimate/calc.go
package estimate

import "math"

func round2(x float64) float64 { return math.Round(x*100) / 100 }

// CalcLineInput holds the raw per-line quantities and rates used to compute
// line money (kept distinct from types.go's LineInput, which is the full API
// request line shape — see Task 3's note on this naming).
type CalcLineInput struct {
	Quantity, UnitPrice, DiscountPercent, TaxPercent float64
}

// LineMoney holds a line's computed subtotal, discount, tax, and total (2-dp rounded).
type LineMoney struct{ Subtotal, Discount, Tax, Total float64 }

// ComputeLine derives a line's stored money (spec §8).
func ComputeLine(in CalcLineInput) LineMoney {
	sub := round2(in.Quantity * in.UnitPrice)
	disc := round2(sub * in.DiscountPercent / 100)
	tax := round2((sub - disc) * in.TaxPercent / 100)
	return LineMoney{Subtotal: sub, Discount: disc, Tax: tax, Total: round2(sub - disc + tax)}
}

// HeaderMoney holds an estimate's computed subtotal, discount total, tax total, and grand total.
type HeaderMoney struct{ Subtotal, DiscountTotal, TaxTotal, GrandTotal float64 }

// ComputeHeader sums line money and applies shipping + adjustment (spec §8).
func ComputeHeader(lines []LineMoney, shipping, adjustment float64) HeaderMoney {
	var h HeaderMoney
	for _, l := range lines {
		h.Subtotal += l.Subtotal
		h.DiscountTotal += l.Discount
		h.TaxTotal += l.Tax
	}
	h.Subtotal = round2(h.Subtotal)
	h.DiscountTotal = round2(h.DiscountTotal)
	h.TaxTotal = round2(h.TaxTotal)
	h.GrandTotal = round2(h.Subtotal - h.DiscountTotal + h.TaxTotal + shipping + adjustment)
	return h
}
```

- [ ] **Step 4: Run the tests to verify they pass**

Run: `go test ./estimate/... -run 'TestComputeLine|TestComputeHeader' -v`
Expected: PASS (both tests, all subtests).

- [ ] **Step 5: Commit**

```bash
git add estimate/calc.go estimate/calc_test.go
git commit -m "feat(estimate): add line/header money calculation"
```

---

### Task 4: `estimate/numbering.go` — document numbering (TDD)

**Files:**
- Create: `estimate/numbering.go`
- Test: `estimate/numbering_test.go`

**Interfaces:**
- Produces: `FormatNumber(serialID int64) string` — used by `store_create.go` (Task 8).

- [ ] **Step 1: Write the failing test**

```go
// estimate/numbering_test.go
package estimate

import "testing"

func TestFormatNumber(t *testing.T) {
	tests := []struct {
		name     string
		serialID int64
		want     string
	}{
		{"single digit", 1, "ESTM-000001"},
		{"three digits", 123, "ESTM-000123"},
		{"six digits exact", 654321, "ESTM-654321"},
		{"seven digits not truncated", 1234567, "ESTM-1234567"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FormatNumber(tt.serialID); got != tt.want {
				t.Fatalf("FormatNumber(%d) = %q, want %q", tt.serialID, got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./estimate/... -run TestFormatNumber -v`
Expected: FAIL — `FormatNumber` undefined.

- [ ] **Step 3: Write the implementation**

```go
// estimate/numbering.go
package estimate

import "fmt"

// numberPrefix is the ESTM record-type code (lkp_record_type.record_type_code).
const numberPrefix = "ESTM"

// FormatNumber renders the human-readable document number from the row's
// serial PK, zero-padded to 6 digits (spec AD-11): ESTM-000001.
func FormatNumber(serialID int64) string {
	return fmt.Sprintf("%s-%06d", numberPrefix, serialID)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./estimate/... -run TestFormatNumber -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add estimate/numbering.go estimate/numbering_test.go
git commit -m "feat(estimate): add document numbering"
```

---

### Task 5: `estimate/transitions.go` — status transition map (TDD)

**Files:**
- Create: `estimate/transitions.go`
- Test: `estimate/transitions_test.go`

**Interfaces:**
- Produces: `ErrInvalidTransition`, `CanTransition(fromCode, toCode string) bool`, `ValidateTransition(fromCode, toCode string) error` — used by `store_transition.go` (Task 10).

- [ ] **Step 1: Write the failing test**

```go
// estimate/transitions_test.go
package estimate

import (
	"errors"
	"testing"
)

func TestCanTransition(t *testing.T) {
	ok := [][2]string{
		{"DRFT", "PAPV"}, {"DRFT", "CANC"},
		{"PAPV", "APPV"}, {"PAPV", "DRFT"}, {"PAPV", "CANC"},
		{"APPV", "SENT"}, {"APPV", "CANC"},
		{"SENT", "RJCT"}, {"SENT", "EXPR"}, {"SENT", "CANC"},
	}
	for _, pair := range ok {
		if !CanTransition(pair[0], pair[1]) {
			t.Errorf("CanTransition(%q, %q) = false, want true", pair[0], pair[1])
		}
	}

	bad := [][2]string{
		{"DRFT", "APPV"},   // must go through PAPV
		{"DRFT", "SENT"},   // can't skip straight to sent
		{"RJCT", "DRFT"},   // terminal
		{"EXPR", "SENT"},   // terminal
		{"CANC", "DRFT"},   // terminal
		{"SENT", "APPV"},   // can't go backward
		{"APPV", "DRFT"},   // no backward path from APPV
	}
	for _, pair := range bad {
		if CanTransition(pair[0], pair[1]) {
			t.Errorf("CanTransition(%q, %q) = true, want false", pair[0], pair[1])
		}
	}
}

func TestValidateTransition(t *testing.T) {
	if err := ValidateTransition("DRFT", "PAPV"); err != nil {
		t.Fatalf("ValidateTransition(DRFT, PAPV) = %v, want nil", err)
	}
	err := ValidateTransition("DRFT", "APPV")
	if !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("ValidateTransition(DRFT, APPV) = %v, want ErrInvalidTransition", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./estimate/... -run 'TestCanTransition|TestValidateTransition' -v`
Expected: FAIL — undefined symbols.

- [ ] **Step 3: Write the implementation**

```go
// estimate/transitions.go
package estimate

import "errors"

// ErrInvalidTransition is returned when a status change is not permitted.
var ErrInvalidTransition = errors.New("invalid estimate status transition")

// allowedTransitions maps a status code to the set of codes reachable from it
// (spec §7). Terminal states (RJCT, EXPR, CANC) map to an empty set. There is
// no "Accepted" status: acceptance is expressed by converting the estimate
// into a quote (spec §9.1), which does not require a status change here.
var allowedTransitions = map[string]map[string]bool{
	"DRFT": {"PAPV": true, "CANC": true},
	"PAPV": {"APPV": true, "DRFT": true, "CANC": true},
	"APPV": {"SENT": true, "CANC": true},
	"SENT": {"RJCT": true, "EXPR": true, "CANC": true},
	"RJCT": {},
	"EXPR": {},
	"CANC": {},
}

// CanTransition reports whether moving fromCode->toCode is allowed.
func CanTransition(fromCode, toCode string) bool {
	return allowedTransitions[fromCode][toCode]
}

// ValidateTransition returns ErrInvalidTransition when the move is not allowed.
func ValidateTransition(fromCode, toCode string) error {
	if !CanTransition(fromCode, toCode) {
		return ErrInvalidTransition
	}
	return nil
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./estimate/... -run 'TestCanTransition|TestValidateTransition' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add estimate/transitions.go estimate/transitions_test.go
git commit -m "feat(estimate): add status transition map"
```

---

### Task 6: `estimate/resolver.go` — query engine field/sort/search resolver (TDD)

**Files:**
- Create: `estimate/resolver.go`
- Test: `estimate/resolver_test.go`

**Interfaces:**
- Consumes: `query.FieldResolver`, `query.SortResolver`, `query.SearchResolver` interfaces (`query/filter.go:103-108`, `query/search.go:11-13`), `query.DataType` + constants (`query/filter.go:21-30`).
- Produces: `resolver{}` implementing all three interfaces — used by `store_search.go` (Task 12).

- [ ] **Step 1: Write the failing test**

```go
// estimate/resolver_test.go
package estimate

import (
	"testing"

	"stonesuite-backend/query"
)

func TestResolver_Resolve(t *testing.T) {
	r := resolver{}

	tests := []struct {
		key     string
		wantOK  bool
		wantDT  query.DataType
	}{
		{"id", true, query.TypeString},
		{"document_number", true, query.TypeString},
		{"customer_id", true, query.TypeString},
		{"status", true, query.TypeString},
		{"grand_total", true, query.TypeNumber},
		{"estimate_date", true, query.TypeDate},
		{"valid_until", true, query.TypeDate},
		{"cf:budget", true, query.TypeString},
		{"cf:Invalid-Key", false, ""},   // fails validCustomKey regex
		{"nonexistent_field", false, ""},
		{"'; DROP TABLE estimate; --", false, ""}, // injection attempt must not resolve
	}
	for _, tt := range tests {
		_, dt, ok := r.Resolve(tt.key)
		if ok != tt.wantOK {
			t.Errorf("Resolve(%q) ok = %v, want %v", tt.key, ok, tt.wantOK)
			continue
		}
		if ok && dt != tt.wantDT {
			t.Errorf("Resolve(%q) dt = %v, want %v", tt.key, dt, tt.wantDT)
		}
	}
}

func TestResolver_SortExpr(t *testing.T) {
	r := resolver{}
	if _, _, ok := r.SortExpr("grand_total"); !ok {
		t.Error("SortExpr(grand_total) not found, want found")
	}
	if _, _, ok := r.SortExpr("estimate_internal_notes"); ok {
		t.Error("SortExpr(estimate_internal_notes) found, want not found (not in sort whitelist)")
	}
}

func TestResolver_SearchPredicate(t *testing.T) {
	r := resolver{}
	pred := r.SearchPredicate("$1")
	if pred == "" {
		t.Fatal("SearchPredicate returned empty string")
	}
	// Must reference the placeholder, not interpolate a literal value.
	if want := "$1"; !contains(pred, want) {
		t.Errorf("SearchPredicate result %q does not reference placeholder %q", pred, want)
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && (func() bool {
		for i := 0; i+len(substr) <= len(s); i++ {
			if s[i:i+len(substr)] == substr {
				return true
			}
		}
		return false
	})()
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./estimate/... -run TestResolver -v`
Expected: FAIL — `resolver` undefined.

- [ ] **Step 3: Write the implementation**

```go
// estimate/resolver.go
package estimate

import (
	"regexp"
	"strings"

	"stonesuite-backend/query"
)

// validCustomKey mirrors salesorder.validCustomKey / crmstore.validCustomKey
// so JSONB custom keys are safe to interpolate.
var validCustomKey = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// resolver implements query.FieldResolver + SortResolver + SearchResolver for
// estimate (spec §11). Table alias `est` matches estimateSelect (Task 7).
type resolver struct{}

type resolved struct {
	expr string
	dt   query.DataType
}

// systemFields is the filter whitelist (spec §11 table).
var systemFields = map[string]resolved{
	"id":               {"est.estimate_uuid::text", query.TypeString},
	"document_number":  {"COALESCE(est.estimate_number,'')", query.TypeString},
	"record_number":    {"COALESCE(est.estimate_number,'')", query.TypeString},
	"customer_id":      {"est.estimate_customer_id::text", query.TypeString},
	"status":           {"est.estimate_status::text", query.TypeString},
	"sales_rep_id":     {"est.estimate_sales_rep_id::text", query.TypeString},
	"owner_id":         {"est.estimate_owner_id::text", query.TypeString},
	"estimate_date":    {"est.estimate_date", query.TypeDate},
	"valid_until":      {"est.estimate_valid_until", query.TypeDate},
	"currency_id":      {"est.estimate_currency::text", query.TypeString},
	"payment_terms_id": {"est.estimate_payment_terms::text", query.TypeString},
	"price_level_id":   {"est.estimate_price_level::text", query.TypeString},
	"grand_total":      {"est.estimate_grand_total", query.TypeNumber},
	"po_number":        {"est.estimate_po_number", query.TypeString},
	"created_by":       {"est.estimate_created_by::text", query.TypeString},
	"updated_by":       {"est.estimate_updated_by::text", query.TypeString},
	"created_at":       {"est.estimate_created_at", query.TypeDate},
	"updated_at":       {"est.estimate_updated_at", query.TypeDate},
}

func (resolver) Resolve(key string) (string, query.DataType, bool) {
	if s, ok := systemFields[key]; ok {
		return s.expr, s.dt, true
	}
	if k, ok := strings.CutPrefix(key, "cf:"); ok && validCustomKey.MatchString(k) {
		return "est.estimate_custom_fields->>'" + k + "'", query.TypeString, true
	}
	return "", "", false
}

// sortableFields is the stable (NOT NULL) sort whitelist beyond the engine's
// built-in created_at/updated_at/record_number (spec §11). valid_until is
// excluded since it is nullable (breaks keyset-cursor comparison).
var sortableFields = map[string]resolved{
	"document_number": {"est.estimate_number", query.TypeString},
	"record_number":   {"est.estimate_number", query.TypeString},
	"estimate_date":   {"est.estimate_date", query.TypeDate},
	"grand_total":     {"est.estimate_grand_total", query.TypeNumber},
	"status":          {"est.estimate_status", query.TypeNumber},
	"customer_id":     {"est.estimate_customer_id", query.TypeNumber},
}

func (resolver) SortExpr(key string) (string, query.DataType, bool) {
	if s, ok := sortableFields[key]; ok {
		return s.expr, s.dt, true
	}
	return "", "", false
}

// SearchPredicate powers the list's global-search box: document number,
// PO/reference, notes, snapshot customer name (same-table), item SKU/name
// (child, correlated EXISTS), and the current customer's name/code (spec §11).
func (resolver) SearchPredicate(ph string) string {
	return "(" +
		"est.estimate_number ILIKE '%'||" + ph + "||'%'" +
		" OR est.estimate_po_number ILIKE '%'||" + ph + "||'%'" +
		" OR est.estimate_memo ILIKE '%'||" + ph + "||'%'" +
		" OR est.estimate_notes ILIKE '%'||" + ph + "||'%'" +
		" OR est.estimate_bill_customer_name ILIKE '%'||" + ph + "||'%'" +
		" OR EXISTS (SELECT 1 FROM estimate_item ei WHERE ei.estimate_id = est.estimate_id" +
		"   AND (ei.sku ILIKE '%'||" + ph + "||'%' OR ei.item_name ILIKE '%'||" + ph + "||'%'))" +
		" OR EXISTS (SELECT 1 FROM customer c WHERE c.customer_id = est.estimate_customer_id" +
		"   AND (c.customer_name ILIKE '%'||" + ph + "||'%' OR c.customer_doc_num ILIKE '%'||" + ph + "||'%'))" +
		")"
}

var _ query.FieldResolver = resolver{}
var _ query.SortResolver = resolver{}
var _ query.SearchResolver = resolver{}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./estimate/... -run TestResolver -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add estimate/resolver.go estimate/resolver_test.go
git commit -m "feat(estimate): add query engine field/sort/search resolver"
```

---

### Task 7: `estimate/store.go` — shared helpers, `Get`, scan/select plumbing

**Files:**
- Create: `estimate/store.go`

**Interfaces:**
- Consumes: `workflow.Querier` (`workflow/store.go:16-22`).
- Produces: `ErrNotFound`, `ClientError`, `IsClientError`, `colVal`, `buildInsert`, `buildUpdateSet`, `nullableInt`, `nullableDate`, `orNow`, `isForeignKeyViolation`, `estmRecordTypeCode`, `draftStatusCode`, `recordTypeIDByCode`, `statusIDByCode`, `customerSnapshot`, `customerSnapshotByInternalID`, `itemSnapshot`, `resolveInventoryItem`, `taxPercentForRate`, `overrideAddress`, `addrColVals`, `estimateSelect`, `itemSelect`, `scanEstimate`, `scanLine`, `loadLines`, `Get` — used by every remaining task in this package.

- [ ] **Step 1: Write the file**

```go
// estimate/store.go
package estimate

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/workflow"
)

// ErrNotFound is returned when an estimate uuid matches nothing live.
var ErrNotFound = errors.New("estimate not found")

// ClientError signals a client-caused failure (validation, bad input, an
// illegal transition) that a controller maps to HTTP 400/409, mirroring
// salesorder.ClientError.
type ClientError struct{ Msg string }

func (e ClientError) Error() string { return e.Msg }

// IsClientError reports whether err is a ClientError.
func IsClientError(err error) bool {
	var ce ClientError
	return errors.As(err, &ce)
}

// estmRecordTypeCode is the lkp_record_type code for Estimate (spec §1).
const estmRecordTypeCode = "ESTM"

// draftStatusCode is the status every new estimate starts at (spec §7).
const draftStatusCode = "DRFT"

// nullableInt converts a non-positive id to SQL NULL.
func nullableInt(v int) any {
	if v <= 0 {
		return nil
	}
	return v
}

// nullableDate returns the given "yyyy-mm-dd" string as a nullable date arg.
func nullableDate(d string) any {
	if d == "" {
		return nil
	}
	return d
}

// orNow returns the given "yyyy-mm-dd" date string, or today when blank.
func orNow(d string) string {
	if d == "" {
		return "now"
	}
	return d
}

// isForeignKeyViolation reports whether err is a PostgreSQL FK-constraint
// violation (code 23503) — an invalid caller-supplied reference id.
func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

// colVal pairs a column name with its bind value (and an optional type cast
// suffix, e.g. "::date") so an INSERT/UPDATE's column list and argument list
// are always built from the same slice.
type colVal struct {
	col  string
	val  any
	cast string
}

// buildInsert renders an INSERT ... VALUES (...) RETURNING statement from
// column/value pairs, numbering placeholders by position.
func buildInsert(table string, cv []colVal, returning string) (string, []any) {
	cols := make([]string, len(cv))
	phs := make([]string, len(cv))
	args := make([]any, len(cv))
	for i, c := range cv {
		cols[i] = c.col
		args[i] = c.val
		phs[i] = fmt.Sprintf("$%d%s", i+1, c.cast)
	}
	sql := "INSERT INTO " + table + " (" + strings.Join(cols, ", ") + ") VALUES (" + strings.Join(phs, ", ") + ")"
	if returning != "" {
		sql += " RETURNING " + returning
	}
	return sql, args
}

// buildUpdateSet renders an "UPDATE ... SET col=$n, ... WHERE <where>"
// statement. leadingArgs are bound first; cv's placeholders continue after.
func buildUpdateSet(table string, leadingArgs []any, cv []colVal, extraSets []string, where string) (string, []any) {
	sets := make([]string, 0, len(cv)+len(extraSets))
	args := make([]any, 0, len(leadingArgs)+len(cv))
	args = append(args, leadingArgs...)
	for _, c := range cv {
		args = append(args, c.val)
		sets = append(sets, fmt.Sprintf("%s = $%d%s", c.col, len(args), c.cast))
	}
	sets = append(sets, extraSets...)
	sql := "UPDATE " + table + " SET " + strings.Join(sets, ", ") + " WHERE " + where
	return sql, args
}

// recordTypeIDByCode resolves a lkp_record_type code to its internal id.
func recordTypeIDByCode(ctx context.Context, q workflow.Querier, code string) (int, error) {
	var id int
	err := q.QueryRow(ctx,
		`SELECT record_type_id FROM lkp_record_type WHERE record_type_code = $1`, code).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("record type %q: %w", code, err)
	}
	return id, nil
}

// statusIDByCode resolves a lkp_record_status code (scoped to a record type)
// to its internal id.
func statusIDByCode(ctx context.Context, q workflow.Querier, recordTypeID int, code string) (int, error) {
	var id int
	err := q.QueryRow(ctx, `
		SELECT record_status_id FROM lkp_record_status
		WHERE record_status_record_type = $1 AND record_status_code = $2`, recordTypeID, code).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("status %q: %w", code, err)
	}
	return id, nil
}

// customerSnapshot loads a customer's internal id, name, and default
// billing/shipping address blocks for the create-time snapshot (spec AD-4).
func customerSnapshot(ctx context.Context, q workflow.Querier, customerUUID string) (id int, name string, billing, shipping AddressInput, err error) {
	var (
		billLine1, billLine2, billSuite, billCity string
		billState, billCountry                    *int
		billZip                                   string
		shipLine1, shipLine2, shipSuite, shipCity string
		shipState, shipCountry                    *int
		shipZip                                   string
	)
	err = q.QueryRow(ctx, `
		SELECT customer_id, customer_name,
		       customer_bill_addr_line1, customer_bill_addr_line2, customer_bill_addr_suitenum,
		       customer_bill_addr_city, customer_bill_addr_state, customer_bill_addr_zip, customer_bill_addr_country,
		       customer_ship_addr_line1, customer_ship_addr_line2, customer_ship_addr_suitenum,
		       customer_ship_addr_city, customer_ship_addr_state, customer_ship_addr_zip, customer_ship_addr_country
		FROM customer WHERE customer_uuid = $1 AND customer_deleted_at IS NULL`, customerUUID).Scan(
		&id, &name,
		&billLine1, &billLine2, &billSuite, &billCity, &billState, &billZip, &billCountry,
		&shipLine1, &shipLine2, &shipSuite, &shipCity, &shipState, &shipZip, &shipCountry,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", AddressInput{}, AddressInput{}, ClientError{Msg: "Unknown customer."}
	}
	if err != nil {
		return 0, "", AddressInput{}, AddressInput{}, fmt.Errorf("load customer snapshot: %w", err)
	}
	billing = AddressInput{
		CustomerName: name, AddrLine1: billLine1, AddrLine2: billLine2, SuiteUnit: billSuite,
		City: billCity, StateID: billState, Zip: billZip, CountryID: billCountry,
	}
	shipping = AddressInput{
		CustomerName: name, AddrLine1: shipLine1, AddrLine2: shipLine2, SuiteUnit: shipSuite,
		City: shipCity, StateID: shipState, Zip: shipZip, CountryID: shipCountry,
	}
	return id, name, billing, shipping, nil
}

// customerSnapshotByInternalID resolves an internal customer id back to its
// uuid, then delegates to customerSnapshot (used by Update, which only has
// the internal id on hand from the estimate row).
func customerSnapshotByInternalID(ctx context.Context, q workflow.Querier, custInternalID int) (id int, name string, billing, shipping AddressInput, err error) {
	var uuid string
	if err := q.QueryRow(ctx, `SELECT customer_uuid FROM customer WHERE customer_id = $1`, custInternalID).Scan(&uuid); err != nil {
		return 0, "", AddressInput{}, AddressInput{}, fmt.Errorf("resolve customer uuid: %w", err)
	}
	return customerSnapshot(ctx, q, uuid)
}

// overrideAddress layers a caller-supplied partial address over a default
// (e.g. the customer's snapshot), preferring the override for any non-empty field.
func overrideAddress(def, override AddressInput) AddressInput {
	out := def
	if override.CustomerName != "" {
		out.CustomerName = override.CustomerName
	}
	if override.Attention != "" {
		out.Attention = override.Attention
	}
	if override.AddrLine1 != "" {
		out.AddrLine1 = override.AddrLine1
	}
	if override.AddrLine2 != "" {
		out.AddrLine2 = override.AddrLine2
	}
	if override.SuiteUnit != "" {
		out.SuiteUnit = override.SuiteUnit
	}
	if override.City != "" {
		out.City = override.City
	}
	if override.StateID != nil {
		out.StateID = override.StateID
	}
	if override.Zip != "" {
		out.Zip = override.Zip
	}
	if override.CountryID != nil {
		out.CountryID = override.CountryID
	}
	if override.Phone != "" {
		out.Phone = override.Phone
	}
	if override.Fax != "" {
		out.Fax = override.Fax
	}
	if override.Email != "" {
		out.Email = override.Email
	}
	return out
}

// addrColVals returns the 12 (column, value) pairs for a billing/shipping
// address block, in the exact column order the schema declares (state before
// zip). prefix is "estimate_bill" or "estimate_ship".
func addrColVals(prefix string, a AddressInput) []colVal {
	return []colVal{
		{prefix + "_customer_name", a.CustomerName, ""},
		{prefix + "_attention", a.Attention, ""},
		{prefix + "_addr_line1", a.AddrLine1, ""},
		{prefix + "_addr_line2", a.AddrLine2, ""},
		{prefix + "_addr_suitenum", a.SuiteUnit, ""},
		{prefix + "_addr_city", a.City, ""},
		{prefix + "_addr_state", a.StateID, ""},
		{prefix + "_addr_zip", a.Zip, ""},
		{prefix + "_addr_country", a.CountryID, ""},
		{prefix + "_phone", a.Phone, ""},
		{prefix + "_fax", a.Fax, ""},
		{prefix + "_email", a.Email, ""},
	}
}

// itemSnapshot is what a line needs from its catalog item at add time.
type itemSnapshot struct {
	internalID int
	sku        string
	name       string
	desc       string
	unitID     *int
	unitCode   string
	unitPrice  float64
	taxRateID  *int
}

// resolveInventoryItem loads a catalog item's snapshot fields by its external
// uuid. Returns ClientError when the uuid does not resolve to a live item.
func resolveInventoryItem(ctx context.Context, q workflow.Querier, uuid string) (*itemSnapshot, error) {
	var s itemSnapshot
	err := q.QueryRow(ctx, `
		SELECT ii.inventory_item_id, ii.inventory_item_sku, ii.inventory_item_name, ii.inventory_item_description,
		       ii.inventory_item_unit_id, COALESCE(u.unit_code,''), ii.inventory_item_unit_price, ii.inventory_item_tax_rate_id
		FROM inventory_item ii
		LEFT JOIN lkp_unit u ON u.unit_id = ii.inventory_item_unit_id
		WHERE ii.inventory_item_uuid = $1 AND ii.inventory_item_deleted_at IS NULL`, uuid).Scan(
		&s.internalID, &s.sku, &s.name, &s.desc, &s.unitID, &s.unitCode, &s.unitPrice, &s.taxRateID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ClientError{Msg: "Unknown inventory item: " + uuid}
	}
	if err != nil {
		return nil, fmt.Errorf("load inventory item: %w", err)
	}
	return &s, nil
}

// taxPercentForRate loads a named tax rate's percent by internal id.
func taxPercentForRate(ctx context.Context, q workflow.Querier, taxRateID int) (float64, error) {
	var pct float64
	if err := q.QueryRow(ctx,
		`SELECT tax_rate_percent FROM lkp_tax_rate WHERE tax_rate_id = $1`, taxRateID).Scan(&pct); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, ClientError{Msg: "Unknown tax rate."}
		}
		return 0, fmt.Errorf("load tax rate: %w", err)
	}
	return pct, nil
}

// estimateSelect is the base SELECT shared by Get and Search. Column order
// must match scanEstimate's Scan(...) arg order exactly.
const estimateSelect = `
	SELECT est.estimate_uuid, COALESCE(est.estimate_number,''),
	       rs.record_status_name, rs.record_status_code,
	       est.estimate_approval_status,
	       c.customer_uuid, c.customer_name,
	       COALESCE(ou.id::text,''),
	       to_char(est.estimate_date,'YYYY-MM-DD'),
	       COALESCE(to_char(est.estimate_valid_until,'YYYY-MM-DD'),''),
	       est.estimate_po_number, est.estimate_reference_number, est.estimate_memo,
	       est.estimate_notes, est.estimate_internal_notes, est.estimate_terms_conditions,
	       est.estimate_payment_terms, est.estimate_price_level, est.estimate_currency,
	       est.estimate_sales_rep_id, est.estimate_owner_id, est.estimate_sales_tax_percent,
	       est.estimate_ship_same_as_bill,
	       est.estimate_bill_customer_name, est.estimate_bill_attention,
	       est.estimate_bill_addr_line1, est.estimate_bill_addr_line2, est.estimate_bill_addr_suitenum,
	       est.estimate_bill_addr_city, est.estimate_bill_addr_state, est.estimate_bill_addr_zip,
	       est.estimate_bill_addr_country, est.estimate_bill_phone, est.estimate_bill_fax, est.estimate_bill_email,
	       est.estimate_ship_customer_name, est.estimate_ship_attention,
	       est.estimate_ship_addr_line1, est.estimate_ship_addr_line2, est.estimate_ship_addr_suitenum,
	       est.estimate_ship_addr_city, est.estimate_ship_addr_state, est.estimate_ship_addr_zip,
	       est.estimate_ship_addr_country, est.estimate_ship_phone, est.estimate_ship_fax, est.estimate_ship_email,
	       est.estimate_custom_fields,
	       est.estimate_subtotal, est.estimate_discount_total, est.estimate_tax_total,
	       est.estimate_shipping_charge, est.estimate_adjustment, est.estimate_grand_total,
	       est.estimate_created_at, est.estimate_updated_at
	FROM estimate est
	JOIN lkp_record_status rs ON rs.record_status_id = est.estimate_status
	JOIN customer c ON c.customer_id = est.estimate_customer_id
	LEFT JOIN employee oe ON oe.employee_id = est.estimate_owner_id
	LEFT JOIN users ou ON ou.id = oe.employee_user_id`

func scanEstimate(row pgx.Row) (*Estimate, error) {
	var e Estimate
	var customRaw []byte
	if err := row.Scan(
		&e.ID, &e.Number, &e.Status, &e.StatusCode, &e.ApprovalStatus,
		&e.Customer.ID, &e.Customer.Name, &e.OwnerUserID,
		&e.EstimateDate, &e.ValidUntil,
		&e.PONumber, &e.ReferenceNumber, &e.Memo,
		&e.Notes, &e.InternalNotes, &e.TermsConditions,
		&e.PaymentTermsID, &e.PriceLevelID, &e.CurrencyID,
		&e.SalesRepEmployeeID, &e.OwnerEmployeeID, &e.SalesTaxPercent,
		&e.ShipSameAsBilling,
		&e.Billing.CustomerName, &e.Billing.Attention,
		&e.Billing.AddrLine1, &e.Billing.AddrLine2, &e.Billing.SuiteUnit,
		&e.Billing.City, &e.Billing.StateID, &e.Billing.Zip,
		&e.Billing.CountryID, &e.Billing.Phone, &e.Billing.Fax, &e.Billing.Email,
		&e.Shipping.CustomerName, &e.Shipping.Attention,
		&e.Shipping.AddrLine1, &e.Shipping.AddrLine2, &e.Shipping.SuiteUnit,
		&e.Shipping.City, &e.Shipping.StateID, &e.Shipping.Zip,
		&e.Shipping.CountryID, &e.Shipping.Phone, &e.Shipping.Fax, &e.Shipping.Email,
		&customRaw,
		&e.Subtotal, &e.DiscountTotal, &e.TaxTotal,
		&e.ShippingCharge, &e.Adjustment, &e.GrandTotal,
		&e.CreatedAt, &e.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if len(customRaw) > 0 {
		_ = jsonUnmarshal(customRaw, &e.CustomFields)
	}
	return &e, nil
}

// itemSelect is the base SELECT for an estimate's live lines. Column order
// must match scanLine's Scan(...) arg order exactly.
const itemSelect = `
	SELECT ei.estimate_item_uuid, ei.line_number,
	       ii.inventory_item_uuid,
	       ei.sku, ei.item_name, ei.description, COALESCE(ei.unit_code,''),
	       ei.quantity, ei.unit_price, ei.discount_percent, ei.tax_percent,
	       ei.line_subtotal, ei.line_discount, ei.line_tax, ei.line_total
	FROM estimate_item ei
	LEFT JOIN inventory_item ii ON ii.inventory_item_id = ei.inventory_item_id
	WHERE ei.estimate_id = $1 AND ei.item_deleted_at IS NULL
	ORDER BY ei.line_number`

func scanLine(row pgx.Rows) (Line, error) {
	var l Line
	err := row.Scan(
		&l.ID, &l.LineNumber, &l.InventoryItemID,
		&l.SKU, &l.ItemName, &l.Description, &l.UnitCode,
		&l.Quantity, &l.UnitPrice, &l.DiscountPercent, &l.TaxPercent,
		&l.LineSubtotal, &l.LineDiscount, &l.LineTax, &l.LineTotal,
	)
	return l, err
}

// loadLines fetches an estimate's live lines by its external uuid.
func loadLines(ctx context.Context, q workflow.Querier, uuid string) ([]Line, error) {
	var internalID int
	if err := q.QueryRow(ctx,
		`SELECT estimate_id FROM estimate WHERE estimate_uuid = $1`, uuid).Scan(&internalID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("resolve estimate id: %w", err)
	}
	rows, err := q.Query(ctx, itemSelect, internalID)
	if err != nil {
		return nil, fmt.Errorf("load estimate items: %w", err)
	}
	defer rows.Close()
	out := []Line{}
	for rows.Next() {
		l, err := scanLine(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// Get loads a single live estimate by its external uuid, including its lines.
func Get(ctx context.Context, pool *pgxpool.Pool, uuid string) (*Estimate, error) {
	e, err := scanEstimate(pool.QueryRow(ctx, estimateSelect+`
		WHERE est.estimate_uuid = $1 AND est.estimate_deleted_at IS NULL`, uuid))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get estimate: %w", err)
	}
	items, err := loadLines(ctx, pool, uuid)
	if err != nil {
		return nil, err
	}
	e.Items = items
	return e, nil
}
```

- [ ] **Step 2: Add the small `jsonUnmarshal` wrapper this file references**

`scanEstimate` calls `jsonUnmarshal` rather than `encoding/json.Unmarshal` directly only to keep `store.go`'s import list minimal until later tasks need more of `encoding/json` — add the import and use it directly instead, which is simpler. Replace the `scanEstimate` line:

```go
	if len(customRaw) > 0 {
		_ = jsonUnmarshal(customRaw, &e.CustomFields)
	}
```

with:

```go
	if len(customRaw) > 0 {
		_ = json.Unmarshal(customRaw, &e.CustomFields)
	}
```

and add `"encoding/json"` to the import block (alphabetically after `"context"`):

```go
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/workflow"
)
```

- [ ] **Step 3: Verify it compiles**

Run: `go build ./estimate/...`
Expected: exits 0. (`Get`/`loadLines` reference `estimate`/`estimate_item` tables that now exist per Task 1; this only type-checks the Go code, it does not run against a database yet.)

- [ ] **Step 4: Commit**

```bash
git add estimate/store.go
git commit -m "feat(estimate): add store helpers, Get, and scan/select plumbing"
```

---

### Task 8: `estimate/store_create.go` — `Create` (+ `resolveLines`, `insertLines`, `writeHistory`)

**Files:**
- Create: `estimate/store_create.go`
- Test: `estimate/store_test.go` (new file; `//go:build dbtest`-gated)

**Interfaces:**
- Consumes: everything from Task 7 (`store.go`) plus `ComputeLine`/`ComputeHeader` (Task 3).
- Produces: `resolvedLine`, `resolveLines`, `insertLines`, `writeHistory`, `Create(ctx, pool, in CreateEstimateInput, actorEmployeeID int) (*Estimate, error)` — `writeHistory` and `insertLines` are reused by Tasks 9/10/11; `Create` is called by `controllers/estimate.go` (Task 13).

- [ ] **Step 1: Write the failing test**

```go
// estimate/store_test.go
//go:build dbtest

package estimate

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping DB-backed test")
	}
	pool, err := pgxpool.New(context.Background(), dsn)
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// seedCustomerAndItem inserts a minimal live customer + inventory_item,
// mirroring invoice/store_test.go's helper of the same name.
func seedCustomerAndItem(t *testing.T, pool *pgxpool.Pool) (custUUID, itemUUID string) {
	t.Helper()
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())

	var custTypeID int
	if err := pool.QueryRow(ctx, `SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'CUST'`).Scan(&custTypeID); err != nil {
		t.Fatalf("resolve CUST record type: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO customer (record_type, customer_name, customer_created_by)
		VALUES ($1, $2, 1) RETURNING customer_uuid`,
		custTypeID, "Test Customer "+suffix).Scan(&custUUID); err != nil {
		t.Fatalf("seed customer: %v", err)
	}

	if err := pool.QueryRow(ctx, `
		INSERT INTO inventory_item (inventory_item_sku, inventory_item_name, inventory_item_unit_price, inventory_item_created_by)
		VALUES ($1, $2, 25.00, 1) RETURNING inventory_item_uuid`,
		"SKU-"+suffix, "Test Item "+suffix).Scan(&itemUUID); err != nil {
		t.Fatalf("seed inventory item: %v", err)
	}
	return custUUID, itemUUID
}

func TestCreate_SnapshotsAndTotals(t *testing.T) {
	pool := testPool(t)
	custUUID, itemUUID := seedCustomerAndItem(t, pool)

	in := CreateEstimateInput{
		CustomerUUID: custUUID,
		estimateFields: estimateFields{
			SalesTaxPercent: 8,
			Items: []LineInput{
				{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 2, DiscountPercent: 0},
			},
		},
	}
	got, err := Create(context.Background(), pool, in, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got.Number == "" || got.Number[:5] != "ESTM-" {
		t.Errorf("Number = %q, want ESTM- prefix", got.Number)
	}
	if got.StatusCode != "DRFT" {
		t.Errorf("StatusCode = %q, want DRFT", got.StatusCode)
	}
	if len(got.Items) != 1 {
		t.Fatalf("len(Items) = %d, want 1", len(got.Items))
	}
	// unit price snapshotted from inventory_item (25.00) since request left UnitPrice at 0.
	if got.Items[0].UnitPrice != 25 {
		t.Errorf("Items[0].UnitPrice = %v, want 25", got.Items[0].UnitPrice)
	}
	// subtotal = 2*25=50, tax = 50*0.08=4, grand = 54
	if got.GrandTotal != 54 {
		t.Errorf("GrandTotal = %v, want 54", got.GrandTotal)
	}
}

func TestCreate_RequiresCustomer(t *testing.T) {
	pool := testPool(t)
	_, err := Create(context.Background(), pool, CreateEstimateInput{}, 1)
	if !IsClientError(err) {
		t.Fatalf("Create with no customer = %v, want ClientError", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `TEST_DATABASE_URL=<your test db dsn> go test ./estimate/... -tags dbtest -run TestCreate -v`
Expected: FAIL to compile — `resolveLines`, `Create`, `estimateFields` (the embedded field access) undefined in `store_create.go` context. (If `TEST_DATABASE_URL` is unset, this step instead reports `SKIP`, which is expected in a local dev environment without Postgres — proceed to Step 3 regardless and confirm later via CI or a real DB.)

- [ ] **Step 3: Write the implementation**

```go
// estimate/store_create.go
package estimate

import (
	"context"
	"fmt"
	"strings"

	"stonesuite-backend/workflow"
)

// resolvedLine is a line after catalog/free-text resolution, ready to price
// and insert.
type resolvedLine struct {
	lineNumber      int
	inventoryItemID *int // internal FK, nil for free-text
	sku, name, desc string
	unitID          *int
	unitCode        string
	quantity        float64
	unitPrice       float64
	discountPercent float64
	taxRateID       *int
	taxPercent      float64
	money           LineMoney
}

// resolveLines validates and resolves every input line against the catalog
// (or free text), computing each line's stored money (spec §8). headerTax is
// the header's default tax percent, used when a line has no tax rate.
func resolveLines(ctx context.Context, q workflow.Querier, items []LineInput, headerTax float64) ([]resolvedLine, error) {
	if len(items) == 0 {
		return nil, ClientError{Msg: "At least one line item is required."}
	}
	out := make([]resolvedLine, 0, len(items))
	seenLine := map[int]bool{}
	for _, in := range items {
		if in.LineNumber <= 0 {
			return nil, ClientError{Msg: "Each line item needs a positive line number."}
		}
		if seenLine[in.LineNumber] {
			return nil, ClientError{Msg: fmt.Sprintf("Duplicate line number %d.", in.LineNumber)}
		}
		seenLine[in.LineNumber] = true
		if in.Quantity <= 0 {
			return nil, ClientError{Msg: fmt.Sprintf("Line %d: quantity must be greater than zero.", in.LineNumber)}
		}
		if in.UnitPrice < 0 {
			return nil, ClientError{Msg: fmt.Sprintf("Line %d: unit price cannot be negative.", in.LineNumber)}
		}

		rl := resolvedLine{
			lineNumber:      in.LineNumber,
			quantity:        in.Quantity,
			unitPrice:       in.UnitPrice,
			discountPercent: in.DiscountPercent,
			taxRateID:       in.TaxRateID,
		}

		if in.InventoryItemUUID != "" {
			item, err := resolveInventoryItem(ctx, q, in.InventoryItemUUID)
			if err != nil {
				return nil, err
			}
			id := item.internalID
			rl.inventoryItemID = &id
			rl.sku, rl.name, rl.desc = item.sku, item.name, item.desc
			rl.unitID, rl.unitCode = item.unitID, item.unitCode
			if rl.unitPrice == 0 {
				rl.unitPrice = item.unitPrice
			}
			if rl.taxRateID == nil {
				rl.taxRateID = item.taxRateID
			}
		} else if strings.TrimSpace(in.Description) == "" {
			return nil, ClientError{Msg: fmt.Sprintf("Line %d: either an inventory item or a description is required.", in.LineNumber)}
		} else {
			rl.desc = in.Description
		}

		if rl.taxRateID != nil {
			pct, err := taxPercentForRate(ctx, q, *rl.taxRateID)
			if err != nil {
				return nil, err
			}
			rl.taxPercent = pct
		} else {
			rl.taxPercent = headerTax
		}

		rl.money = ComputeLine(CalcLineInput{
			Quantity: rl.quantity, UnitPrice: rl.unitPrice,
			DiscountPercent: rl.discountPercent, TaxPercent: rl.taxPercent,
		})
		out = append(out, rl)
	}
	return out, nil
}

// insertLines bulk-inserts resolved lines as estimate_item rows.
func insertLines(ctx context.Context, tx pgx.Tx, estimateInternalID int, lines []resolvedLine, actorEmployeeID int) error {
	for _, l := range lines {
		_, err := tx.Exec(ctx, `
			INSERT INTO estimate_item (
				estimate_id, line_number, inventory_item_id,
				item_name, sku, description, unit_id, unit_code,
				quantity, unit_price, discount_percent, tax_rate_id, tax_percent,
				line_subtotal, line_discount, line_tax, line_total,
				item_created_by
			) VALUES ($1,$2,$3, $4,$5,$6,$7,$8, $9,$10,$11,$12,$13, $14,$15,$16,$17, $18)`,
			estimateInternalID, l.lineNumber, l.inventoryItemID,
			l.name, l.sku, l.desc, l.unitID, l.unitCode,
			l.quantity, l.unitPrice, l.discountPercent, l.taxRateID, l.taxPercent,
			l.money.Subtotal, l.money.Discount, l.money.Tax, l.money.Total,
			nullableInt(actorEmployeeID),
		)
		if err != nil {
			if isForeignKeyViolation(err) {
				return ClientError{Msg: fmt.Sprintf("Line %d: an invalid unit or tax rate was referenced.", l.lineNumber)}
			}
			return fmt.Errorf("insert estimate item: %w", err)
		}
	}
	return nil
}

// writeHistory records one estimate_history row inside the caller's transaction.
func writeHistory(ctx context.Context, tx pgx.Tx, estimateInternalID int, action string, fromStatusID, toStatusID *int, actorEmployeeID int) {
	_, _ = tx.Exec(ctx, `
		INSERT INTO estimate_history (estimate_id, from_status_id, to_status_id, action, actor_employee_id)
		VALUES ($1,$2,$3,$4,$5)`,
		estimateInternalID, fromStatusID, toStatusID, action, nullableInt(actorEmployeeID))
}

// Create inserts a new estimate (header + lines) inside one transaction:
// snapshots billing/shipping from the customer (unless overridden), resolves
// and prices every line, computes header totals, assigns the estimate
// number, and starts the estimate at DRFT (spec §5.1, AD-4, AD-7, AD-11).
func Create(ctx context.Context, pool *pgxpool.Pool, in CreateEstimateInput, actorEmployeeID int) (*Estimate, error) {
	if strings.TrimSpace(in.CustomerUUID) == "" {
		return nil, ClientError{Msg: "A customer is required."}
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin create estimate: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	custInternalID, custName, defBilling, defShipping, err := customerSnapshot(ctx, tx, in.CustomerUUID)
	if err != nil {
		return nil, err
	}
	billing := overrideAddress(defBilling, in.Billing)
	var shipping AddressInput
	if in.ShipSameAsBilling {
		shipping = billing
	} else {
		shipping = overrideAddress(defShipping, in.Shipping)
	}

	lines, err := resolveLines(ctx, tx, in.Items, in.SalesTaxPercent)
	if err != nil {
		return nil, err
	}
	lineMoney := make([]LineMoney, len(lines))
	for i, l := range lines {
		lineMoney[i] = l.money
	}
	header := ComputeHeader(lineMoney, in.ShippingCharge, in.Adjustment)

	recordTypeID, err := recordTypeIDByCode(ctx, tx, estmRecordTypeCode)
	if err != nil {
		return nil, fmt.Errorf("resolve ESTM record type: %w", err)
	}
	draftStatusID, err := statusIDByCode(ctx, tx, recordTypeID, draftStatusCode)
	if err != nil {
		return nil, fmt.Errorf("resolve DRFT status: %w", err)
	}

	ownerEmployeeID := actorEmployeeID
	if in.OwnerEmployeeID != nil && *in.OwnerEmployeeID > 0 {
		ownerEmployeeID = *in.OwnerEmployeeID
	}
	custom := in.CustomFields
	if custom == nil {
		custom = map[string]any{}
	}

	cv := []colVal{
		{"record_type", recordTypeID, ""},
		{"estimate_status", draftStatusID, ""},
		{"estimate_customer_id", custInternalID, ""},
		{"estimate_po_number", in.PONumber, ""},
		{"estimate_reference_number", in.ReferenceNumber, ""},
		{"estimate_date", orNow(in.EstimateDate), "::date"},
		{"estimate_valid_until", nullableDate(in.ValidUntil), "::date"},
		{"estimate_sales_tax_percent", in.SalesTaxPercent, ""},
		{"estimate_memo", in.Memo, ""},
		{"estimate_notes", in.Notes, ""},
		{"estimate_internal_notes", in.InternalNotes, ""},
		{"estimate_terms_conditions", in.TermsConditions, ""},
		{"estimate_sales_rep_id", in.SalesRepEmployeeID, ""},
		{"estimate_owner_id", nullableInt(ownerEmployeeID), ""},
		{"estimate_payment_terms", in.PaymentTermsID, ""},
		{"estimate_price_level", in.PriceLevelID, ""},
		{"estimate_currency", in.CurrencyID, ""},
		{"estimate_subtotal", header.Subtotal, ""},
		{"estimate_discount_total", header.DiscountTotal, ""},
		{"estimate_tax_total", header.TaxTotal, ""},
		{"estimate_shipping_charge", in.ShippingCharge, ""},
		{"estimate_adjustment", in.Adjustment, ""},
		{"estimate_grand_total", header.GrandTotal, ""},
		{"estimate_ship_same_as_bill", in.ShipSameAsBilling, ""},
		{"estimate_custom_fields", custom, ""},
		{"estimate_created_by", nullableInt(actorEmployeeID), ""},
	}
	cv = append(cv, addrColVals("estimate_bill", billing)...)
	cv = append(cv, addrColVals("estimate_ship", shipping)...)

	insertSQL, insertArgs := buildInsert("estimate", cv, "estimate_id, estimate_uuid")
	var internalID int
	var newUUID string
	err = tx.QueryRow(ctx, insertSQL, insertArgs...).Scan(&internalID, &newUUID)
	if err != nil {
		if isForeignKeyViolation(err) {
			return nil, ClientError{Msg: "One of the referenced ids (payment terms, price level, currency, state, or country) does not exist."}
		}
		return nil, fmt.Errorf("insert estimate: %w", err)
	}

	number := FormatNumber(int64(internalID))
	if _, err := tx.Exec(ctx,
		`UPDATE estimate SET estimate_number = $1 WHERE estimate_id = $2`, number, internalID); err != nil {
		return nil, fmt.Errorf("set estimate number: %w", err)
	}

	if err := insertLines(ctx, tx, internalID, lines, actorEmployeeID); err != nil {
		return nil, err
	}

	writeHistory(ctx, tx, internalID, "create", nil, &draftStatusID, actorEmployeeID)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit create estimate: %w", err)
	}
	_ = custName
	return Get(ctx, pool, newUUID)
}
```

- [ ] **Step 4: Add the missing `pgx` import**

`insertLines`/`writeHistory` reference `pgx.Tx`. Add `"github.com/jackc/pgx/v5"` to `store_create.go`'s import block:

```go
import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"stonesuite-backend/workflow"
)
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `TEST_DATABASE_URL=<your test db dsn> go test ./estimate/... -tags dbtest -run TestCreate -v`
Expected: PASS (both `TestCreate_SnapshotsAndTotals` and `TestCreate_RequiresCustomer`), or `SKIP` if `TEST_DATABASE_URL` is unset — in that case also run `go build -tags dbtest ./estimate/...` and expect exit 0, confirming the file at least compiles correctly under the build tag.

- [ ] **Step 6: Commit**

```bash
git add estimate/store_create.go estimate/store_test.go
git commit -m "feat(estimate): add Create with line resolution and snapshot pricing"
```

---

### Task 9: `estimate/store_update.go` — `Update`, `SoftDelete`

**Files:**
- Create: `estimate/store_update.go`
- Modify: `estimate/store_test.go` (add tests)

**Interfaces:**
- Consumes: Tasks 3, 7, 8.
- Produces: `Update(ctx, pool, uuid string, in UpdateEstimateInput, actorEmployeeID int) (*Estimate, error)`, `SoftDelete(ctx, pool, uuid string, actorEmployeeID int) error` — called by `controllers/estimate.go` (Task 13).

- [ ] **Step 1: Write the failing test** (append to `estimate/store_test.go`)

```go
func TestUpdate_RecomputesTotalsAndBumpsVersion(t *testing.T) {
	pool := testPool(t)
	custUUID, itemUUID := seedCustomerAndItem(t, pool)

	created, err := Create(context.Background(), pool, CreateEstimateInput{
		CustomerUUID: custUUID,
		estimateFields: estimateFields{
			SalesTaxPercent: 0,
			Items:           []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}},
		},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	updated, err := Update(context.Background(), pool, created.ID, UpdateEstimateInput{
		estimateFields: estimateFields{
			SalesTaxPercent: 0,
			Items:           []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 3}},
		},
	}, 1)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	// 3 * 25 = 75
	if updated.GrandTotal != 75 {
		t.Errorf("GrandTotal after update = %v, want 75", updated.GrandTotal)
	}
	if len(updated.Items) != 1 || updated.Items[0].Quantity != 3 {
		t.Fatalf("Items after update = %+v, want single line qty 3", updated.Items)
	}
}

func TestSoftDelete_ThenGetReturnsNotFound(t *testing.T) {
	pool := testPool(t)
	custUUID, itemUUID := seedCustomerAndItem(t, pool)

	created, err := Create(context.Background(), pool, CreateEstimateInput{
		CustomerUUID:   custUUID,
		estimateFields: estimateFields{Items: []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}}},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := SoftDelete(context.Background(), pool, created.ID, 1); err != nil {
		t.Fatalf("SoftDelete: %v", err)
	}
	if _, err := Get(context.Background(), pool, created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after delete = %v, want ErrNotFound", err)
	}
}
```

Add `"errors"` to `store_test.go`'s import block if not already present.

- [ ] **Step 2: Run test to verify it fails**

Run: `TEST_DATABASE_URL=<your test db dsn> go test ./estimate/... -tags dbtest -run 'TestUpdate|TestSoftDelete' -v`
Expected: FAIL to compile — `Update`, `SoftDelete` undefined.

- [ ] **Step 3: Write the implementation**

```go
// estimate/store_update.go
package estimate

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Update replaces a live estimate's header fields and lines (recomputing
// totals) inside one transaction. Rejected once the estimate has reached a
// terminal status (RJCT/EXPR/CANC) — a rejected, expired, or cancelled
// estimate is immutable.
func Update(ctx context.Context, pool *pgxpool.Pool, uuid string, in UpdateEstimateInput, actorEmployeeID int) (*Estimate, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin update estimate: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var internalID, custInternalID int
	var statusCode string
	err = tx.QueryRow(ctx, `
		SELECT est.estimate_id, est.estimate_customer_id, rs.record_status_code
		FROM estimate est JOIN lkp_record_status rs ON rs.record_status_id = est.estimate_status
		WHERE est.estimate_uuid = $1 AND est.estimate_deleted_at IS NULL`, uuid,
	).Scan(&internalID, &custInternalID, &statusCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load estimate for update: %w", err)
	}
	if statusCode == "RJCT" || statusCode == "EXPR" || statusCode == "CANC" {
		return nil, ClientError{Msg: "A rejected, expired, or cancelled estimate cannot be edited."}
	}

	_, custName, defBilling, defShipping, err := customerSnapshotByInternalID(ctx, tx, custInternalID)
	if err != nil {
		return nil, err
	}
	billing := overrideAddress(defBilling, in.Billing)
	var shipping AddressInput
	if in.ShipSameAsBilling {
		shipping = billing
	} else {
		shipping = overrideAddress(defShipping, in.Shipping)
	}

	lines, err := resolveLines(ctx, tx, in.Items, in.SalesTaxPercent)
	if err != nil {
		return nil, err
	}
	lineMoney := make([]LineMoney, len(lines))
	for i, l := range lines {
		lineMoney[i] = l.money
	}
	header := ComputeHeader(lineMoney, in.ShippingCharge, in.Adjustment)

	cv := []colVal{
		{"estimate_po_number", in.PONumber, ""},
		{"estimate_reference_number", in.ReferenceNumber, ""},
		{"estimate_date", orNow(in.EstimateDate), "::date"},
		{"estimate_valid_until", nullableDate(in.ValidUntil), "::date"},
		{"estimate_sales_tax_percent", in.SalesTaxPercent, ""},
		{"estimate_memo", in.Memo, ""},
		{"estimate_notes", in.Notes, ""},
		{"estimate_internal_notes", in.InternalNotes, ""},
		{"estimate_terms_conditions", in.TermsConditions, ""},
		{"estimate_sales_rep_id", in.SalesRepEmployeeID, ""},
		{"estimate_owner_id", in.OwnerEmployeeID, ""},
		{"estimate_payment_terms", in.PaymentTermsID, ""},
		{"estimate_price_level", in.PriceLevelID, ""},
		{"estimate_currency", in.CurrencyID, ""},
		{"estimate_subtotal", header.Subtotal, ""},
		{"estimate_discount_total", header.DiscountTotal, ""},
		{"estimate_tax_total", header.TaxTotal, ""},
		{"estimate_shipping_charge", in.ShippingCharge, ""},
		{"estimate_adjustment", in.Adjustment, ""},
		{"estimate_grand_total", header.GrandTotal, ""},
		{"estimate_ship_same_as_bill", in.ShipSameAsBilling, ""},
		{"estimate_custom_fields", in.CustomFields, ""},
	}
	cv = append(cv, addrColVals("estimate_bill", billing)...)
	cv = append(cv, addrColVals("estimate_ship", shipping)...)
	cv = append(cv, colVal{"estimate_updated_by", nullableInt(actorEmployeeID), ""})

	updateSQL, updateArgs := buildUpdateSet("estimate", []any{uuid}, cv,
		[]string{"estimate_updated_at = NOW()", "estimate_record_version = estimate_record_version + 1"},
		"estimate_uuid = $1 AND estimate_deleted_at IS NULL")
	_, err = tx.Exec(ctx, updateSQL, updateArgs...)
	if err != nil {
		if isForeignKeyViolation(err) {
			return nil, ClientError{Msg: "One of the referenced ids (payment terms, price level, currency, state, or country) does not exist."}
		}
		return nil, fmt.Errorf("update estimate: %w", err)
	}

	if _, err := tx.Exec(ctx,
		`UPDATE estimate_item SET item_deleted_at = NOW() WHERE estimate_id = $1 AND item_deleted_at IS NULL`,
		internalID); err != nil {
		return nil, fmt.Errorf("clear previous estimate items: %w", err)
	}
	if err := insertLines(ctx, tx, internalID, lines, actorEmployeeID); err != nil {
		return nil, err
	}

	writeHistory(ctx, tx, internalID, "update", nil, nil, actorEmployeeID)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit update estimate: %w", err)
	}
	_ = custName
	return Get(ctx, pool, uuid)
}

// SoftDelete marks a live estimate deleted.
func SoftDelete(ctx context.Context, pool *pgxpool.Pool, uuid string, actorEmployeeID int) error {
	tag, err := pool.Exec(ctx, `
		UPDATE estimate
		SET estimate_deleted_at = NOW(), estimate_deleted_by = $2
		WHERE estimate_uuid = $1 AND estimate_deleted_at IS NULL`,
		uuid, nullableInt(actorEmployeeID))
	if err != nil {
		return fmt.Errorf("delete estimate: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `TEST_DATABASE_URL=<your test db dsn> go test ./estimate/... -tags dbtest -run 'TestUpdate|TestSoftDelete' -v`
Expected: PASS, or `SKIP` if no test DB — in that case run `go build -tags dbtest ./estimate/...` and expect exit 0.

- [ ] **Step 5: Commit**

```bash
git add estimate/store_update.go estimate/store_test.go
git commit -m "feat(estimate): add Update and SoftDelete"
```

---

### Task 10: `estimate/store_transition.go` — `Transition`

**Files:**
- Create: `estimate/store_transition.go`
- Modify: `estimate/store_test.go` (add tests)

**Interfaces:**
- Consumes: Tasks 5, 7, 8 (`writeHistory`), Task 11 (`activeApproverCount`, written next — see note below on task ordering).
- Produces: `Transition(ctx, pool, uuid, toStatusCode string, actorEmployeeID int) (*Estimate, error)` — called by `controllers/estimate.go`.

> **Ordering note:** `Transition` calls `activeApproverCount`, which Task 11 (`approval.go`) defines. Write this task's code now (it references `activeApproverCount` by name), but do not expect it to compile until Task 11 lands — Steps 2 and 4 below both account for this by running `go vet`/tests only after Task 11 as well. If executing tasks strictly in order, mark Step 2 as "expected to fail to compile due to `activeApproverCount` also being undefined" (in addition to `Transition` itself), and defer full green-test confirmation to immediately after Task 11's Step 4.

- [ ] **Step 1: Write the failing test** (append to `estimate/store_test.go`)

```go
func TestTransition_DraftToPendingApproval(t *testing.T) {
	pool := testPool(t)
	custUUID, itemUUID := seedCustomerAndItem(t, pool)

	created, err := Create(context.Background(), pool, CreateEstimateInput{
		CustomerUUID:   custUUID,
		estimateFields: estimateFields{Items: []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}}},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	updated, err := Transition(context.Background(), pool, created.ID, "PAPV", 1)
	if err != nil {
		t.Fatalf("Transition: %v", err)
	}
	if updated.StatusCode != "PAPV" {
		t.Errorf("StatusCode = %q, want PAPV", updated.StatusCode)
	}
}

func TestTransition_RejectsIllegalMove(t *testing.T) {
	pool := testPool(t)
	custUUID, itemUUID := seedCustomerAndItem(t, pool)

	created, err := Create(context.Background(), pool, CreateEstimateInput{
		CustomerUUID:   custUUID,
		estimateFields: estimateFields{Items: []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}}},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := Transition(context.Background(), pool, created.ID, "APPV", 1); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("Transition DRFT->APPV = %v, want ErrInvalidTransition", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `TEST_DATABASE_URL=<your test db dsn> go test ./estimate/... -tags dbtest -run TestTransition -v`
Expected: FAIL to compile (per the ordering note above — `Transition`/`activeApproverCount` undefined until this task + Task 11 both land).

- [ ] **Step 3: Write the implementation**

```go
// estimate/store_transition.go
package estimate

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Transition moves a live estimate to toStatusCode, validating the move
// against the static transition map (spec §7), row-locking the estimate to
// serialize concurrent transitions, and writing a history row.
func Transition(ctx context.Context, pool *pgxpool.Pool, uuid, toStatusCode string, actorEmployeeID int) (*Estimate, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transition: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var internalID, curStatusID int
	var curStatusCode, approvalStatus string
	err = tx.QueryRow(ctx, `
		SELECT est.estimate_id, est.estimate_status, rs.record_status_code, est.estimate_approval_status
		FROM estimate est JOIN lkp_record_status rs ON rs.record_status_id = est.estimate_status
		WHERE est.estimate_uuid = $1 AND est.estimate_deleted_at IS NULL
		FOR UPDATE OF est`, uuid,
	).Scan(&internalID, &curStatusID, &curStatusCode, &approvalStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load estimate for transition: %w", err)
	}
	if toStatusCode == "CONV" {
		return nil, ClientError{Msg: "CONV is not a valid manual transition target."}
	}
	if err := ValidateTransition(curStatusCode, toStatusCode); err != nil {
		return nil, err
	}

	recordTypeID, err := recordTypeIDByCode(ctx, tx, estmRecordTypeCode)
	if err != nil {
		return nil, fmt.Errorf("resolve ESTM record type: %w", err)
	}
	toStatusID, err := statusIDByCode(ctx, tx, recordTypeID, toStatusCode)
	if err != nil {
		return nil, ClientError{Msg: "Unknown target status."}
	}

	// AD-8 approval gate: an estimate may not leave a status that has
	// configured approvers until it has been approved.
	requiredHere, err := activeApproverCount(ctx, tx, recordTypeID, curStatusID)
	if err != nil {
		return nil, err
	}
	if requiredHere > 0 && approvalStatus != approvalApproved {
		return nil, ErrApprovalRequired
	}
	targetApprovers, err := activeApproverCount(ctx, tx, recordTypeID, toStatusID)
	if err != nil {
		return nil, err
	}
	newApprovalStatus := approvalNone
	if targetApprovers > 0 {
		newApprovalStatus = approvalPending
	}

	if _, err := tx.Exec(ctx, `
		UPDATE estimate SET
			estimate_status = $2, estimate_approval_status = $4, estimate_approved_by = NULL,
			estimate_updated_at = NOW(),
			estimate_updated_by = $3, estimate_record_version = estimate_record_version + 1
		WHERE estimate_id = $1`, internalID, toStatusID, nullableInt(actorEmployeeID), newApprovalStatus); err != nil {
		return nil, fmt.Errorf("transition estimate: %w", err)
	}

	writeHistory(ctx, tx, internalID, "transition", &curStatusID, &toStatusID, actorEmployeeID)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit transition: %w", err)
	}
	return Get(ctx, pool, uuid)
}
```

- [ ] **Step 4: Confirm it compiles once Task 11 lands, then run the tests**

Run (after Task 11 is complete): `TEST_DATABASE_URL=<your test db dsn> go test ./estimate/... -tags dbtest -run TestTransition -v`
Expected: PASS, or `SKIP` if no test DB — in that case run `go build -tags dbtest ./estimate/...` and expect exit 0.

- [ ] **Step 5: Commit**

```bash
git add estimate/store_transition.go estimate/store_test.go
git commit -m "feat(estimate): add Transition with approval gate"
```

---

### Task 11: `estimate/approval.go` — `Approve` (AD-8)

**Files:**
- Create: `estimate/approval.go`
- Modify: `estimate/store_test.go` (add tests)

**Interfaces:**
- Consumes: Tasks 7, 8 (`writeHistory`).
- Produces: `ErrNotApprover`, `ErrApprovalRequired`, `ErrApprovalNotRequired`, `approvalNone`/`approvalPending`/`approvalApproved` constants, `activeApproverCount`, `signOffCount`, `isConfiguredApprover`, `Approve(ctx, pool, uuid string, approverEmployeeID int) (*Estimate, error)` — `activeApproverCount` is consumed by Task 10's `Transition`; `Approve` is called by `controllers/estimate.go`.

- [ ] **Step 1: Write the failing test** (append to `estimate/store_test.go`)

```go
func TestApprove_RequiresConfiguredApprover(t *testing.T) {
	pool := testPool(t)
	custUUID, itemUUID := seedCustomerAndItem(t, pool)

	created, err := Create(context.Background(), pool, CreateEstimateInput{
		CustomerUUID:   custUUID,
		estimateFields: estimateFields{Items: []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}}},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := Transition(context.Background(), pool, created.ID, "PAPV", 1); err != nil {
		t.Fatalf("Transition to PAPV: %v", err)
	}
	// No estimate_approver rows configured for (ESTM, PAPV) in this test DB by
	// default, so Approve should report the status doesn't require approval.
	if _, err := Approve(context.Background(), pool, created.ID, 1); !errors.Is(err, ErrApprovalNotRequired) {
		t.Fatalf("Approve with no configured approvers = %v, want ErrApprovalNotRequired", err)
	}
}

func TestApprove_SignOffFlipsApprovalStatus(t *testing.T) {
	pool := testPool(t)
	custUUID, itemUUID := seedCustomerAndItem(t, pool)
	ctx := context.Background()

	created, err := Create(ctx, pool, CreateEstimateInput{
		CustomerUUID:   custUUID,
		estimateFields: estimateFields{Items: []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}}},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := Transition(ctx, pool, created.ID, "PAPV", 1); err != nil {
		t.Fatalf("Transition to PAPV: %v", err)
	}

	var recordTypeID, papvStatusID int
	if err := pool.QueryRow(ctx, `SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'ESTM'`).Scan(&recordTypeID); err != nil {
		t.Fatalf("resolve ESTM record type: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT record_status_id FROM lkp_record_status WHERE record_status_record_type = $1 AND record_status_code = 'PAPV'`, recordTypeID).Scan(&papvStatusID); err != nil {
		t.Fatalf("resolve PAPV status: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO estimate_approver (record_type_id, record_status_id, approver_employee_id)
		VALUES ($1, $2, 1) ON CONFLICT DO NOTHING`, recordTypeID, papvStatusID); err != nil {
		t.Fatalf("seed estimate_approver: %v", err)
	}

	approved, err := Approve(ctx, pool, created.ID, 1)
	if err != nil {
		t.Fatalf("Approve: %v", err)
	}
	if approved.ApprovalStatus != "approved" {
		t.Errorf("ApprovalStatus = %q, want approved", approved.ApprovalStatus)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `TEST_DATABASE_URL=<your test db dsn> go test ./estimate/... -tags dbtest -run TestApprove -v`
Expected: FAIL to compile — `Approve`, `ErrApprovalNotRequired` undefined. (This also unblocks Task 10's `Transition`, which references `activeApproverCount` defined here.)

- [ ] **Step 3: Write the implementation**

```go
// estimate/approval.go
package estimate

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/workflow"
)

// Approval status values stored in estimate.estimate_approval_status (AD-8).
const (
	approvalNone     = "none"     // no approvers configured for the current status
	approvalPending  = "pending"  // gated: awaiting the required sign-offs
	approvalApproved = "approved" // enough configured approvers have signed off
)

// ErrNotApprover is returned when a caller who is not a configured approver
// for the estimate's current status tries to approve it (AD-8). Maps to 403.
var ErrNotApprover = errors.New("you are not a configured approver for this estimate's current status")

// ErrApprovalRequired is returned when an estimate is asked to leave a status
// that still requires approval sign-off (AD-8). Maps to HTTP 409.
var ErrApprovalRequired = errors.New("this estimate must be approved before it can leave its current status")

// ErrApprovalNotRequired is returned when an approval is submitted for an
// estimate whose current status has no configured approvers (AD-8). Maps to 409.
var ErrApprovalNotRequired = errors.New("this estimate's current status does not require approval")

// activeApproverCount returns how many active approvers are configured for
// the ESTM record type at a status. Zero means no approval gate there (AD-8).
func activeApproverCount(ctx context.Context, q workflow.Querier, recordTypeID, statusID int) (int, error) {
	var n int
	if err := q.QueryRow(ctx, `
		SELECT COUNT(*) FROM estimate_approver
		WHERE record_type_id = $1 AND record_status_id = $2 AND is_active`,
		recordTypeID, statusID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count estimate approvers: %w", err)
	}
	return n, nil
}

// signOffCount returns how many distinct approvers have signed off on an
// estimate at a status.
func signOffCount(ctx context.Context, q workflow.Querier, estimateInternalID, statusID int) (int, error) {
	var n int
	if err := q.QueryRow(ctx, `
		SELECT COUNT(*) FROM estimate_approval
		WHERE estimate_id = $1 AND record_status_id = $2`,
		estimateInternalID, statusID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count estimate approvals: %w", err)
	}
	return n, nil
}

// isConfiguredApprover reports whether an employee is an active configured
// approver for the ESTM record type at a status.
func isConfiguredApprover(ctx context.Context, q workflow.Querier, recordTypeID, statusID, employeeID int) (bool, error) {
	var exists bool
	if err := q.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM estimate_approver
			WHERE record_type_id = $1 AND record_status_id = $2 AND approver_employee_id = $3 AND is_active)`,
		recordTypeID, statusID, employeeID).Scan(&exists); err != nil {
		return false, fmt.Errorf("check estimate approver: %w", err)
	}
	return exists, nil
}

// Approve records one approver's sign-off on an estimate at its current
// status (AD-8). Requires the caller to be a configured approver for that
// status, is idempotent per (estimate, status, approver), and flips the
// header's approval_status to 'approved' once the sign-off count reaches the
// configured approver count.
func Approve(ctx context.Context, pool *pgxpool.Pool, uuid string, approverEmployeeID int) (*Estimate, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin approve estimate: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var internalID, curStatusID int
	err = tx.QueryRow(ctx, `
		SELECT estimate_id, estimate_status FROM estimate
		WHERE estimate_uuid = $1 AND estimate_deleted_at IS NULL
		FOR UPDATE`, uuid).Scan(&internalID, &curStatusID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load estimate for approval: %w", err)
	}

	recordTypeID, err := recordTypeIDByCode(ctx, tx, estmRecordTypeCode)
	if err != nil {
		return nil, fmt.Errorf("resolve ESTM record type: %w", err)
	}

	required, err := activeApproverCount(ctx, tx, recordTypeID, curStatusID)
	if err != nil {
		return nil, err
	}
	if required == 0 {
		return nil, ErrApprovalNotRequired
	}

	ok, err := isConfiguredApprover(ctx, tx, recordTypeID, curStatusID, approverEmployeeID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotApprover
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO estimate_approval (estimate_id, record_status_id, approver_employee_id)
		VALUES ($1, $2, $3)
		ON CONFLICT (estimate_id, record_status_id, approver_employee_id) DO NOTHING`,
		internalID, curStatusID, approverEmployeeID); err != nil {
		return nil, fmt.Errorf("record estimate approval: %w", err)
	}

	approved, err := signOffCount(ctx, tx, internalID, curStatusID)
	if err != nil {
		return nil, err
	}
	newStatus := approvalPending
	var approvedBy any
	if approved >= required {
		newStatus = approvalApproved
		approvedBy = approverEmployeeID
	}
	if _, err := tx.Exec(ctx, `
		UPDATE estimate SET
			estimate_approval_status = $2, estimate_approved_by = $3, estimate_updated_at = NOW()
		WHERE estimate_id = $1`, internalID, newStatus, approvedBy); err != nil {
		return nil, fmt.Errorf("update estimate approval status: %w", err)
	}

	writeHistory(ctx, tx, internalID, "approve", &curStatusID, &curStatusID, approverEmployeeID)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit approve estimate: %w", err)
	}
	return Get(ctx, pool, uuid)
}
```

- [ ] **Step 4: Run all store tests to verify they pass** (this is the first point where the whole package, including Task 10's `Transition`, compiles together)

Run: `TEST_DATABASE_URL=<your test db dsn> go test ./estimate/... -tags dbtest -v`
Expected: PASS for every test in the package (Tasks 8-11's tests), or `SKIP` if no test DB — in that case run `go build -tags dbtest ./estimate/...` and `go vet -tags dbtest ./estimate/...`, expecting exit 0 for both.

- [ ] **Step 5: Commit**

```bash
git add estimate/approval.go estimate/store_test.go
git commit -m "feat(estimate): add Approve with configuration-driven approval gate"
```

---

### Task 12: `estimate/store_search.go` — `Search` (query engine integration)

**Files:**
- Create: `estimate/store_search.go`
- Modify: `estimate/store_test.go` (add test)

**Interfaces:**
- Consumes: Task 6 (`resolver{}`), `query.Build`, `query.NextCursor`, `query.Request`, `query.Built` (`query/filter.go`, `query/builder.go`, `query/cursor.go`).
- Produces: `Search(ctx, pool, scope, actorIdentityID string, req query.Request) (Page, error)`, `employeeIDByIdentity`, `sortValue` — `Search` is called by `controllers/estimate.go`.

- [ ] **Step 1: Write the failing test** (append to `estimate/store_test.go`)

```go
func TestSearch_ReturnsCreatedEstimate(t *testing.T) {
	pool := testPool(t)
	custUUID, itemUUID := seedCustomerAndItem(t, pool)
	ctx := context.Background()

	created, err := Create(ctx, pool, CreateEstimateInput{
		CustomerUUID:   custUUID,
		estimateFields: estimateFields{Items: []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}}},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	page, err := Search(ctx, pool, "all", "", query.Request{Search: created.Number})
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	found := false
	for _, r := range page.Records {
		if r.ID == created.ID {
			found = true
		}
	}
	if !found {
		t.Errorf("Search(%q) did not include the created estimate", created.Number)
	}
}
```

Add `"stonesuite-backend/query"` to `store_test.go`'s import block.

- [ ] **Step 2: Run test to verify it fails**

Run: `TEST_DATABASE_URL=<your test db dsn> go test ./estimate/... -tags dbtest -run TestSearch -v`
Expected: FAIL to compile — `Search` undefined.

- [ ] **Step 3: Write the implementation**

```go
// estimate/store_search.go
package estimate

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/query"
)

// employeeIDByIdentity resolves a control-plane identity to a tenant
// employee_id, mirroring salesorder.employeeIDByIdentity.
func employeeIDByIdentity(ctx context.Context, pool *pgxpool.Pool, identityID string) (int, bool) {
	if identityID == "" {
		return 0, false
	}
	var id int
	err := pool.QueryRow(ctx, `
		SELECT e.employee_id FROM employee e
		JOIN users u ON u.id = e.employee_user_id
		WHERE u.identity_id = $1 AND e.employee_deleted_at IS NULL`, identityID).Scan(&id)
	if err != nil {
		return 0, false
	}
	return id, true
}

// Search lists estimates under the caller's RBAC scope with filter/sort/global
// search + keyset pagination. List rows omit line items to avoid an N+1 join.
func Search(ctx context.Context, pool *pgxpool.Pool, scope, actorIdentityID string, req query.Request) (Page, error) {
	where := []string{"est.estimate_deleted_at IS NULL"}
	var args []any
	nextIdx := 1
	if scope == "own" || scope == "team" {
		empID, found := employeeIDByIdentity(ctx, pool, actorIdentityID)
		if !found {
			return Page{}, nil
		}
		where = append(where, fmt.Sprintf("est.estimate_owner_id = $%d", nextIdx))
		args = append(args, empID)
		nextIdx++
	}

	built, err := query.Build(req, resolver{}, nextIdx)
	if err != nil {
		return Page{}, err
	}
	if built.Where != "" {
		where = append(where, built.Where)
	}
	if built.Keyset != "" {
		where = append(where, built.Keyset)
	}
	args = append(args, built.Args...)

	q := estimateSelect + " WHERE " + strings.Join(where, " AND ") +
		" ORDER BY " + built.OrderBy + " LIMIT " + strconv.Itoa(built.EffLimit+1)

	rows, err := pool.Query(ctx, q, args...)
	if err != nil {
		return Page{}, fmt.Errorf("search estimates: %w", err)
	}
	defer rows.Close()
	out := []Estimate{}
	for rows.Next() {
		e, err := scanEstimate(rows)
		if err != nil {
			return Page{}, err
		}
		out = append(out, *e)
	}
	if err := rows.Err(); err != nil {
		return Page{}, fmt.Errorf("search estimates: %w", err)
	}

	page := Page{Records: out}
	if len(out) > built.EffLimit {
		page.HasMore = true
		last := out[built.EffLimit-1]
		page.Records = out[:built.EffLimit]
		page.NextCursor = query.NextCursor(last.ID, built.Sort, sortValue(last, built.Sort.Field))
	}
	return page, nil
}

// sortValue reads the effective sort field's value from an estimate to mint
// the next cursor.
func sortValue(e Estimate, field string) any {
	switch field {
	case "updated_at":
		return e.UpdatedAt
	case "grand_total":
		return e.GrandTotal
	case "estimate_date":
		return e.EstimateDate
	case "document_number", "record_number":
		return e.Number
	default: // created_at (default)
		return e.CreatedAt
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `TEST_DATABASE_URL=<your test db dsn> go test ./estimate/... -tags dbtest -v`
Expected: PASS for every test in the package, or `SKIP` if no test DB — in that case run `go build -tags dbtest ./estimate/...` and expect exit 0.

- [ ] **Step 5: Commit**

```bash
git add estimate/store_search.go estimate/store_test.go
git commit -m "feat(estimate): add Search with keyset pagination"
```

---

### Task 13: `controllers/estimate.go` — HTTP handlers (RBAC, IDOR, error mapping)

**Files:**
- Create: `controllers/estimate.go`

**Interfaces:**
- Consumes: `middleware.GetUserFromContext`, `tenancy.PoolFromContext`, `authz.Check`, `authz.ResourceEstimate` (already seeded), `recordInScope` (`controllers/scope.go`, generic/reusable as-is), `logSecurityEvent` (`controllers/security_log.go`, generic/reusable as-is), `resolveEmployeeID` (`controllers/crm_admin.go`, generic/reusable as-is), `writeJSON`/`fail` (`controllers/tenant.go`, generic/reusable as-is), every `estimate.*` function from Tasks 7-12.
- Produces: `EstimateOps`, `NewEstimateOps()`, `estimateFail` — consumed by `main.go` (Task 15) and Task 14 (`controllers/estimate_audit.go`).

- [ ] **Step 1: Write the file**

```go
// controllers/estimate.go
package controllers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/estimate"
	"stonesuite-backend/middleware"
	"stonesuite-backend/query"
	"stonesuite-backend/tenancy"
)

// EstimateOps handles the Estimate endpoints: a dedicated relational module
// (header + line items), a sibling of the Sales Order/Invoice modules — not
// served through the generic /api/tenant/crm/{workflowKey} JSONB router
// (spec AD-1). Mirrors SalesOrderOps' auth/IDOR/error-mapping conventions.
//
// Routes:
//
//	GET    /api/tenant/estimates                    — unfiltered list (cursor-paginated)
//	POST   /api/tenant/estimates/search              — filter + sort + search + pagination
//	POST   /api/tenant/estimates                     — create
//	GET    /api/tenant/estimates/{uuid}              — get (+ items)
//	PATCH  /api/tenant/estimates/{uuid}               — update
//	DELETE /api/tenant/estimates/{uuid}               — soft delete
//	POST   /api/tenant/estimates/{uuid}/transition    — status change
//	POST   /api/tenant/estimates/{uuid}/approve       — approval sign-off
//	GET    /api/tenant/estimates/{uuid}/audit         — audit trail
type EstimateOps struct{}

// NewEstimateOps constructs the handler group.
func NewEstimateOps() *EstimateOps { return &EstimateOps{} }

// authEstimate resolves JWT + tenant pool + the estimate:<action> RBAC grant
// for requests with no specific record yet (list/search/create).
func (h *EstimateOps) authEstimate(w http.ResponseWriter, r *http.Request, action authz.Action) (*pgxpool.Pool, string, authz.Scope, bool) {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return nil, "", "", false
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return nil, "", "", false
	}
	decision, err := authz.Check(r.Context(), pool, payload.ID, authz.ResourceEstimate, action)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, "", "", false
	}
	if !decision.Allowed {
		fail(w, http.StatusForbidden, "You do not have permission to "+string(action)+" estimates.")
		return nil, "", "", false
	}
	return pool, payload.ID, decision.Scope, true
}

// authEstimateByUUID resolves auth for a single-record action, then enforces
// the row-level IDOR guard. Denial returns 404 (not 403) so callers cannot
// enumerate ids outside their scope.
func (h *EstimateOps) authEstimateByUUID(w http.ResponseWriter, r *http.Request, uuid string, action authz.Action) (*pgxpool.Pool, string, *estimate.Estimate, bool) {
	pool, identityID, scope, ok := h.authEstimate(w, r, action)
	if !ok {
		return nil, "", nil, false
	}
	est, err := estimate.Get(r.Context(), pool, uuid)
	if errors.Is(err, estimate.ErrNotFound) {
		fail(w, http.StatusNotFound, "Estimate not found.")
		return nil, "", nil, false
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load estimate.")
		return nil, "", nil, false
	}
	if scope != authz.ScopeAll {
		allowed, aerr := recordInScope(r.Context(), pool, scope, identityID, est.OwnerUserID, "")
		if aerr != nil {
			fail(w, http.StatusInternalServerError, "Permission check failed.")
			return nil, "", nil, false
		}
		if !allowed {
			logSecurityEvent(r, "idor_denied",
				"identity", identityID, "record", uuid, "resource", "estimate",
				"action", string(action), "scope", string(scope))
			fail(w, http.StatusNotFound, "Estimate not found.")
			return nil, "", nil, false
		}
	}
	return pool, identityID, est, true
}

// estimateFail maps a store error to an HTTP response.
func estimateFail(w http.ResponseWriter, err error, serverMsg string) {
	switch {
	case errors.Is(err, estimate.ErrNotFound):
		fail(w, http.StatusNotFound, "Estimate not found.")
	case errors.Is(err, estimate.ErrInvalidTransition),
		errors.Is(err, estimate.ErrApprovalRequired),
		errors.Is(err, estimate.ErrApprovalNotRequired):
		fail(w, http.StatusConflict, err.Error())
	case errors.Is(err, estimate.ErrNotApprover):
		fail(w, http.StatusForbidden, err.Error())
	case estimate.IsClientError(err):
		fail(w, http.StatusBadRequest, err.Error())
	default:
		var ife *query.InvalidFilterError
		if errors.As(err, &ife) {
			fail(w, http.StatusBadRequest, ife.Error())
			return
		}
		fail(w, http.StatusInternalServerError, serverMsg)
	}
}

// ---- list / search / create --------------------------------------------------

// List GET /api/tenant/estimates
func (h *EstimateOps) List(w http.ResponseWriter, r *http.Request) {
	pool, identityID, scope, ok := h.authEstimate(w, r, authz.ActionRead)
	if !ok {
		return
	}
	req := query.Request{Cursor: r.URL.Query().Get("cursor"), Search: r.URL.Query().Get("search")}
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil {
		req.Limit = n
	}
	h.search(w, r, pool, identityID, scope, req)
}

// Search POST /api/tenant/estimates/search
func (h *EstimateOps) Search(w http.ResponseWriter, r *http.Request) {
	pool, identityID, scope, ok := h.authEstimate(w, r, authz.ActionRead)
	if !ok {
		return
	}
	var req query.Request
	if r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			fail(w, http.StatusBadRequest, "Invalid request body.")
			return
		}
	}
	h.search(w, r, pool, identityID, scope, req)
}

func (h *EstimateOps) search(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, identityID string, scope authz.Scope, req query.Request) {
	page, err := estimate.Search(r.Context(), pool, string(scope), identityID, req)
	if err != nil {
		estimateFail(w, err, "Failed to search estimates.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success":    true,
		"scope":      scope,
		"records":    page.Records,
		"nextCursor": page.NextCursor,
		"hasMore":    page.HasMore,
	})
}

// Create POST /api/tenant/estimates
func (h *EstimateOps) Create(w http.ResponseWriter, r *http.Request) {
	pool, identityID, _, ok := h.authEstimate(w, r, authz.ActionCreate)
	if !ok {
		return
	}
	var in estimate.CreateEstimateInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	est, err := estimate.Create(r.Context(), pool, in, resolveEmployeeID(r, identityID))
	if err != nil {
		estimateFail(w, err, "Failed to create estimate.")
		return
	}
	auditEstimate(r, pool, identityID, "create", est.ID, nil, est)
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "estimate": est})
}

// ---- single record ------------------------------------------------------------

// Get GET /api/tenant/estimates/{uuid}
func (h *EstimateOps) Get(w http.ResponseWriter, r *http.Request) {
	_, _, est, ok := h.authEstimateByUUID(w, r, r.PathValue("uuid"), authz.ActionRead)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "estimate": est})
}

// Update PATCH /api/tenant/estimates/{uuid}
func (h *EstimateOps) Update(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, before, ok := h.authEstimateByUUID(w, r, uuid, authz.ActionUpdate)
	if !ok {
		return
	}
	var in estimate.UpdateEstimateInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	after, err := estimate.Update(r.Context(), pool, uuid, in, resolveEmployeeID(r, identityID))
	if err != nil {
		estimateFail(w, err, "Failed to update estimate.")
		return
	}
	auditEstimate(r, pool, identityID, "update", uuid, before, after)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "estimate": after})
}

// Delete DELETE /api/tenant/estimates/{uuid}
func (h *EstimateOps) Delete(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, before, ok := h.authEstimateByUUID(w, r, uuid, authz.ActionDelete)
	if !ok {
		return
	}
	if err := estimate.SoftDelete(r.Context(), pool, uuid, resolveEmployeeID(r, identityID)); err != nil {
		estimateFail(w, err, "Failed to delete estimate.")
		return
	}
	auditEstimateDelete(r, pool, identityID, uuid, before)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Estimate deleted."})
}

// Transition POST /api/tenant/estimates/{uuid}/transition  body {"toStatusCode":"..."}
func (h *EstimateOps) Transition(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, _, ok := h.authEstimateByUUID(w, r, uuid, authz.ActionTransition)
	if !ok {
		return
	}
	var req struct {
		ToStatusCode string `json:"toStatusCode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ToStatusCode == "" {
		fail(w, http.StatusBadRequest, "toStatusCode is required.")
		return
	}
	est, err := estimate.Transition(r.Context(), pool, uuid, req.ToStatusCode, resolveEmployeeID(r, identityID))
	if err != nil {
		estimateFail(w, err, "Failed to apply transition.")
		return
	}
	auditEstimate(r, pool, identityID, "transition", uuid, nil, est)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "estimate": est})
}

// Approve POST /api/tenant/estimates/{uuid}/approve
func (h *EstimateOps) Approve(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, _, ok := h.authEstimateByUUID(w, r, uuid, authz.ActionTransition)
	if !ok {
		return
	}
	est, err := estimate.Approve(r.Context(), pool, uuid, resolveEmployeeID(r, identityID))
	if err != nil {
		if errors.Is(err, estimate.ErrNotApprover) {
			logSecurityEvent(r, "approval_denied", "identity", identityID, "record", uuid)
		}
		estimateFail(w, err, "Failed to approve estimate.")
		return
	}
	auditEstimate(r, pool, identityID, "approve", uuid, nil, est)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "estimate": est})
}
```

- [ ] **Step 2: Verify it compiles** (will fail until Task 14 adds `auditEstimate`/`auditEstimateDelete` — expected)

Run: `go build ./controllers/...`
Expected: FAIL — `auditEstimate`, `auditEstimateDelete` undefined (Task 14 defines them next). This is expected; do not treat as a regression.

- [ ] **Step 3: Commit**

```bash
git add controllers/estimate.go
git commit -m "feat(estimate): add EstimateOps HTTP handlers"
```

---

### Task 14: `controllers/estimate_audit.go` — audit trail

**Files:**
- Create: `controllers/estimate_audit.go`

**Interfaces:**
- Consumes: `workflow.UserIDByIdentity`, `workflow.LogAuditFull` (`workflow/audit.go:19`, `workflow/store.go:491`), `loadAuditEntries` (generic, defined in `controllers/crm_audit.go:106`, reused as-is), `clientIP` and `appVersion` (generic package-level helpers already used by `controllers/salesorder_audit.go`).
- Produces: `auditEstimate`, `auditEstimateDelete`, `(*EstimateOps).Audit` — completes `EstimateOps` from Task 13.

- [ ] **Step 1: Write the file**

```go
// controllers/estimate_audit.go
package controllers

import (
	"log"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/estimate"
	"stonesuite-backend/workflow"
)

// estimateSnapshot flattens an estimate into a JSON-able map for the audit
// trail, mirroring soSnapshot (salesorder_audit.go) for the Estimate shape.
func estimateSnapshot(e *estimate.Estimate) map[string]any {
	if e == nil {
		return nil
	}
	return map[string]any{
		"id":             e.ID,
		"estimateNumber": e.Number,
		"status":         e.Status,
		"customerId":     e.Customer.ID,
		"grandTotal":     e.GrandTotal,
	}
}

// auditEstimate records an Estimate mutation in the unified audit_logs table.
// Best-effort: failures are logged, never returned, mirroring auditSO.
func auditEstimate(r *http.Request, pool *pgxpool.Pool, identityID, action, recordID string, oldEstimate, newEstimate *estimate.Estimate) {
	ctx := r.Context()
	actorUserID, _ := workflow.UserIDByIdentity(ctx, pool, identityID)
	if err := workflow.LogAuditFull(ctx, pool, actorUserID, action, "estimate", recordID, "estimate",
		estimateSnapshot(oldEstimate), estimateSnapshot(newEstimate), nil,
		clientIP(r), r.Header.Get("X-Session-Id"), appVersion); err != nil {
		log.Printf("estimate: audit %s %s: %v", action, recordID, err)
	}
}

// auditEstimateDelete is the delete-specific variant, mirroring auditSODelete.
func auditEstimateDelete(r *http.Request, pool *pgxpool.Pool, identityID, recordID string, oldEstimate *estimate.Estimate) {
	ctx := r.Context()
	actorUserID, _ := workflow.UserIDByIdentity(ctx, pool, identityID)
	if err := workflow.LogAuditFull(ctx, pool, actorUserID, "delete", "estimate", recordID, "estimate",
		estimateSnapshot(oldEstimate), nil, nil,
		clientIP(r), r.Header.Get("X-Session-Id"), appVersion); err != nil {
		log.Printf("estimate: audit delete %s: %v", recordID, err)
	}
}

// Audit GET /api/tenant/estimates/{uuid}/audit
// Returns the unified audit trail for a single estimate (most recent first).
func (h *EstimateOps) Audit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, _, _, ok := h.authEstimateByUUID(w, r, id, authz.ActionRead)
	if !ok {
		return
	}
	entries, err := loadAuditEntries(r.Context(), pool, id)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load audit trail.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "recordId": id, "audit": entries,
	})
}
```

- [ ] **Step 2: Verify the whole controllers package compiles**

Run: `go build ./controllers/...`
Expected: exits 0.

- [ ] **Step 3: Commit**

```bash
git add controllers/estimate_audit.go
git commit -m "feat(estimate): add audit trail recording and endpoint"
```

---

### Task 15: `main.go` — route registration

**Files:**
- Modify: `main.go` (add a new block immediately after the existing Sales Order block, i.e. right after the line registering `mux.Handle("GET /api/tenant/sales-orders/{uuid}/audit", tenantChain(so.Audit))`)

**Interfaces:**
- Consumes: `controllers.NewEstimateOps()`, `tenantChain` (already defined at `main.go:368-372`).

- [ ] **Step 1: Add the import**

Confirm `"stonesuite-backend/controllers"` is already imported in `main.go` (it is, since `salesorder`/`invoice` controllers are already registered there) — no import change needed.

- [ ] **Step 2: Add the route block**

Insert immediately after the Sales Order registration block:

```go
// Estimate: dedicated v2 relational module (header + line items + approval),
// a sibling of Sales Order/Invoice — not served through the generic
// /api/tenant/crm/{workflowKey} JSONB router.
est := controllers.NewEstimateOps()
mux.Handle("GET /api/tenant/estimates", tenantChain(est.List))
mux.Handle("POST /api/tenant/estimates/search", tenantChain(est.Search))
mux.Handle("POST /api/tenant/estimates", tenantChain(est.Create))
mux.Handle("GET /api/tenant/estimates/{uuid}", tenantChain(est.Get))
mux.Handle("PATCH /api/tenant/estimates/{uuid}", tenantChain(est.Update))
mux.Handle("DELETE /api/tenant/estimates/{uuid}", tenantChain(est.Delete))
mux.Handle("POST /api/tenant/estimates/{uuid}/transition", tenantChain(est.Transition))
mux.Handle("POST /api/tenant/estimates/{uuid}/approve", tenantChain(est.Approve))
mux.Handle("GET /api/tenant/estimates/{uuid}/audit", tenantChain(est.Audit))
```

- [ ] **Step 3: Verify the full binary builds**

Run: `go build ./...`
Expected: exits 0.

- [ ] **Step 4: Commit**

```bash
git add main.go
git commit -m "feat(estimate): register estimate routes"
```

---

### Task 16: Full verification pass

**Files:** none (verification only)

- [ ] **Step 1: Build the whole repo**

Run: `go build ./...`
Expected: exits 0, no output.

- [ ] **Step 2: Vet the whole repo**

Run: `go vet ./...`
Expected: exits 0, no output.

- [ ] **Step 3: Run the pure-logic test suite** (no DB required)

Run: `go test ./estimate/... -v`
Expected: PASS — `TestComputeLine`, `TestComputeHeader`, `TestFormatNumber`, `TestCanTransition`, `TestValidateTransition`, `TestResolver_Resolve`, `TestResolver_SortExpr`, `TestResolver_SearchPredicate` all pass. (The `dbtest`-tagged tests in `store_test.go` are excluded by default since they carry the `dbtest` build tag.)

- [ ] **Step 4: Run the full repo test suite**

Run: `go test ./...`
Expected: exits 0 (`ok` for every package, or `no test files` for packages without tests — no `FAIL` lines).

- [ ] **Step 5: Run the DB-backed test suite, if a test database is available**

Run: `TEST_DATABASE_URL=<your test db dsn> go test ./estimate/... -tags dbtest -v`
Expected: PASS for every `TestCreate_*`, `TestUpdate_*`, `TestSoftDelete_*`, `TestTransition_*`, `TestApprove_*`, `TestSearch_*` test.

- [ ] **Step 6: Hand off to the requested reviews** (run outside this plan, per the original task's "Reviews" section — not part of this plan's scope, listed here only so nothing is dropped)

Run, in order: migration-auditor (schema from Task 1), tenancy-security-reviewer (RBAC/IDOR from Tasks 13-14), filter-invariant-checker (resolver/search from Tasks 6, 12), feature-dev:code-reviewer, code-simplifier. Fix or justify every finding before this module is considered complete.

- [ ] **Step 7: No commit for this task** (verification only — nothing to add to git beyond what Tasks 1-15 already committed)
