# Quote Module Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the `quote` Go package + `controllers/quote.go` + schema migration, plus the Estimate→Quote and Quote→SalesOrder conversion services, completing StoneSuite's Estimate/Quote pipeline.

**Architecture:** `quote/` is a structural sibling of `estimate/` (Plan A, `docs/superpowers/plans/2026-07-14-estimate-module.md`, must be implemented first — this plan imports `estimate` and `salesorder` packages) — same hybrid-PK/audit/soft-delete/`record_version`/approval shape, plus: `quote.quote_estimate_id` lineage FK, `quote_item.estimate_item_id` line lineage FK, a `CONV` status excluded from manual transitions, and two conversion services (`estimate.ConvertToQuote`, `quote.ConvertToSalesOrder`) backed by a new `quote_conversion` mapping table.

**Tech Stack:** Go, `pgx/v5`, PostgreSQL (per-tenant DB), the shared `query/` filter-and-pagination engine, stdlib `testing` (table-driven, no testify).

**Spec:** `docs/superpowers/specs/2026-07-14-estimates-quotes-module-design.md` — this plan implements the Quote rows of §3, §5.5-§5.10, §7 (Quote transitions), §8 (Quote/conversion pricing), §9 (both conversions), §10 (Quote + convert routes), §11 (Quote resolver), §12 (Quote/conversion validation), §13 (quote rows).

**Depends on:** `docs/superpowers/plans/2026-07-14-estimate-module.md` fully implemented and merged first — `estimate.Get`, `estimate.Estimate`, `estimate.ErrNotFound`, and `estimate`'s internal snapshot columns are read by Task 15's conversion service.

## Global Constraints

Identical to Plan A's Global Constraints section (hybrid PK, audit columns, paired soft delete, `record_version`, append-only idempotent migrations with no `ALTER TABLE ADD COLUMN` on pre-existing tables, mandatory security chain, `query/` engine rules, money precision/rounding, no panics, `%w` wrapping, `context.Context` first param, no testify). Additionally:

- `authz.ResourceQuote` and all 5 actions are **already seeded** (`authz/catalog.go:37,126-130`) — zero `authz/catalog.go` changes.
- `lkp_record_type` row `QUOT` (id 5) and `lkp_record_status` rows for `record_type=5` (`DRFT, PAPV, APPV, SENT, CANC, RJCT, EXPR, CONV`) are **already seeded** — this plan does not touch those rows.
- `sales_order`/`sales_order_item` (built by the Sales Order module, already shipped) are **read-only** from this plan's perspective — no schema change to either, per spec AD-6. The only new FK pointing at `sales_order` is `quote_conversion.sales_order_id`, on a brand-new table.

---

### Task 1: Schema migration — `quote` table set + `quote_conversion`

**Files:**
- Modify: `database/migrations/tenant/schema.sql` (append after Plan A's `estimate` block)

**Interfaces:**
- Produces: tables `quote`, `quote_item`, `quote_history`, `quote_approver`, `quote_approval`, `quote_conversion` with the exact column names used by every later task.

- [ ] **Step 1: Append the DDL**

```sql
-- ── Quote module (docs/superpowers/specs/2026-07-14-estimates-quotes-module-design.md) ──

CREATE TABLE IF NOT EXISTS quote (
    quote_id                     SERIAL        PRIMARY KEY,
    quote_uuid                   UUID          NOT NULL DEFAULT gen_random_uuid(),
    ss_customer_id                 INTEGER          NULL,
    quote_number                   VARCHAR(20)      NULL,

    record_type                    INTEGER       NOT NULL REFERENCES lkp_record_type(record_type_id),
    quote_status                    INTEGER       NOT NULL REFERENCES lkp_record_status(record_status_id),

    quote_approval_status           VARCHAR(10)   NOT NULL DEFAULT 'none',
    quote_approved_by               INTEGER           NULL REFERENCES employee(employee_id),

    quote_estimate_id                INTEGER          NULL REFERENCES estimate(estimate_id),

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

    quote_sales_rep_id               INTEGER           NULL REFERENCES employee(employee_id),
    quote_owner_id                   INTEGER           NULL REFERENCES employee(employee_id),

    quote_payment_terms              INTEGER           NULL REFERENCES lkp_payment_terms(payment_terms_id),
    quote_price_level                INTEGER           NULL REFERENCES lkp_price_level(price_level_id),
    quote_currency                   INTEGER           NULL REFERENCES lkp_currency(currency_id),
    quote_exchange_rate              DECIMAL(18,6) NOT NULL DEFAULT 1,

    quote_subtotal                   DECIMAL(15,2) NOT NULL DEFAULT 0,
    quote_discount_total             DECIMAL(15,2) NOT NULL DEFAULT 0,
    quote_tax_total                  DECIMAL(15,2) NOT NULL DEFAULT 0,
    quote_shipping_charge            DECIMAL(15,2) NOT NULL DEFAULT 0,
    quote_adjustment                 DECIMAL(15,2) NOT NULL DEFAULT 0,
    quote_grand_total                DECIMAL(15,2) NOT NULL DEFAULT 0,

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

CREATE TABLE IF NOT EXISTS quote_item (
    quote_item_id              SERIAL        PRIMARY KEY,
    quote_item_uuid             UUID          NOT NULL DEFAULT gen_random_uuid(),
    quote_id                     INTEGER       NOT NULL REFERENCES quote(quote_id) ON DELETE CASCADE,
    line_number                   INTEGER      NOT NULL,
    inventory_item_id             INTEGER          NULL REFERENCES inventory_item(inventory_item_id),
    estimate_item_id               INTEGER          NULL REFERENCES estimate_item(estimate_item_id),

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

CREATE TABLE IF NOT EXISTS quote_history (
    quote_history_id         SERIAL       PRIMARY KEY,
    quote_id                   INTEGER      NOT NULL REFERENCES quote(quote_id) ON DELETE CASCADE,
    from_status_id               INTEGER          NULL REFERENCES lkp_record_status(record_status_id),
    to_status_id                  INTEGER          NULL REFERENCES lkp_record_status(record_status_id),
    action                         VARCHAR(32)  NOT NULL DEFAULT 'transition',
    actor_employee_id               INTEGER          NULL REFERENCES employee(employee_id),
    snapshot                         JSONB        NOT NULL DEFAULT '{}',
    at                                TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS quote_approver (
    quote_approver_id       SERIAL      PRIMARY KEY,
    record_type_id           INTEGER     NOT NULL REFERENCES lkp_record_type(record_type_id),
    record_status_id         INTEGER     NOT NULL REFERENCES lkp_record_status(record_status_id),
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

CREATE TABLE IF NOT EXISTS quote_conversion (
    quote_conversion_id      SERIAL       PRIMARY KEY,
    quote_id                   INTEGER      NOT NULL REFERENCES quote(quote_id) ON DELETE CASCADE,
    sales_order_id               INTEGER      NOT NULL REFERENCES sales_order(sales_order_id) ON DELETE CASCADE,
    converted_at                  TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    converted_by                   INTEGER          NULL REFERENCES employee(employee_id),
    snapshot                        JSONB        NOT NULL DEFAULT '{}',

    CONSTRAINT uq_quote_conversion_sales_order UNIQUE (sales_order_id)
);

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

- [ ] **Step 2: Verify the migration applies cleanly**

Run: `psql "$TEST_DATABASE_URL" -f database/migrations/tenant/schema.sql`
Expected: exits 0. `NOTICE` lines are fine; any `ERROR` is a failure to fix.

- [ ] **Step 3: Confirm the repo still builds**

Run: `go build ./...`
Expected: exits 0.

- [ ] **Step 4: Commit**

```bash
git add database/migrations/tenant/schema.sql
git commit -m "feat(quote): add quote schema (header, items, history, approval, conversion mapping)"
```

---

### Task 2: `quote/types.go` — domain types

**Files:**
- Create: `quote/types.go`

**Interfaces:**
- Produces: `AddressInput`, `LineInput`, `quoteFields`, `CreateQuoteInput`, `UpdateQuoteInput`, `CustomerRef`, `Line`, `Quote`, `Page` — every later task in this package.

- [ ] **Step 1: Write the file**

```go
package quote

import "time"

// AddressInput is a billing or shipping snapshot block on create/update.
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

// LineInput is one quoted line on create/update.
type LineInput struct {
	LineNumber        int     `json:"lineNumber"`
	InventoryItemUUID string  `json:"inventoryItemUuid"`
	Description       string  `json:"description"`
	Quantity          float64 `json:"quantity"`
	UnitPrice         float64 `json:"unitPrice"`
	DiscountPercent   float64 `json:"discountPercent"`
	TaxRateID         *int    `json:"taxRateId"`
}

// quoteFields is the header payload shared by create and update.
type quoteFields struct {
	PONumber           string         `json:"poNumber"`
	ReferenceNumber    string         `json:"referenceNumber"`
	QuoteDate          string         `json:"quoteDate"`
	ValidUntil         string         `json:"validUntil"`
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

// CreateQuoteInput is the create-request payload (spec §10). EstimateUUID is
// deliberately absent — a quote's estimate lineage may only be set via the
// estimate.ConvertToQuote conversion path (spec §12), never a direct create.
type CreateQuoteInput struct {
	CustomerUUID string `json:"customerUuid"`
	quoteFields
}

// UpdateQuoteInput mirrors CreateQuoteInput minus the customer.
type UpdateQuoteInput struct {
	quoteFields
}

// CustomerRef is the light customer reference on a Quote response.
type CustomerRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// EstimateRef is the light estimate lineage reference on a Quote response,
// present only when the quote traces back to a source estimate.
type EstimateRef struct {
	ID     string `json:"id"`
	Number string `json:"number"`
}

// Line is one quoted line in the API response — the frozen snapshot values,
// not live inventory_item data. EstimateItemID is the line-level lineage
// back to the source estimate line, when this quote was converted from one.
type Line struct {
	ID              string  `json:"id"`
	LineNumber      int     `json:"lineNumber"`
	InventoryItemID *string `json:"inventoryItemId,omitempty"`
	EstimateItemID  *string `json:"estimateItemId,omitempty"`
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

// Quote is the full API response for a quote header (+ lines, when loaded by
// Get). OwnerUserID backs the controller's IDOR scope check and is never
// serialized.
type Quote struct {
	ID              string       `json:"id"`
	Number          string       `json:"quoteNumber"`
	Status          string       `json:"status"`
	StatusCode      string       `json:"statusCode"`
	ApprovalStatus  string       `json:"approvalStatus"`
	Customer        CustomerRef  `json:"customer"`
	Estimate        *EstimateRef `json:"estimate,omitempty"`
	OwnerUserID     string       `json:"-"`
	QuoteDate       string       `json:"quoteDate"`
	ValidUntil      string       `json:"validUntil,omitempty"`
	PONumber        string       `json:"poNumber,omitempty"`
	ReferenceNumber string       `json:"referenceNumber,omitempty"`
	Memo            string       `json:"memo,omitempty"`
	Notes           string       `json:"notes,omitempty"`
	InternalNotes   string       `json:"internalNotes,omitempty"`
	TermsConditions string       `json:"termsConditions,omitempty"`

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

// Page is one page of a keyset-paginated quote search.
type Page struct {
	Records    []Quote
	NextCursor string
	HasMore    bool
}
```

- [ ] **Step 2: Verify it compiles**

Run: `go build ./quote/...`
Expected: exits 0.

- [ ] **Step 3: Commit**

```bash
git add quote/types.go
git commit -m "feat(quote): add quote package domain types"
```

---

### Task 3: `quote/calc.go` — money calculation (TDD)

**Files:**
- Create: `quote/calc.go`
- Test: `quote/calc_test.go`

**Interfaces:**
- Produces: `round2`, `CalcLineInput`, `LineMoney`, `ComputeLine`, `HeaderMoney`, `ComputeHeader` — identical shape to `estimate/calc.go` (Plan A Task 3), separate package (no cross-import — each document package computes its own money independently, per spec §13).

- [ ] **Step 1: Write the failing test**

```go
// quote/calc_test.go
package quote

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
			want: LineMoney{Subtotal: 300, Discount: 30, Tax: 22.28, Total: 292.28},
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
	lines := []LineMoney{
		{Subtotal: 100, Discount: 10, Tax: 7.2, Total: 97.2},
		{Subtotal: 50, Discount: 0, Tax: 4.13, Total: 54.13},
	}
	got := ComputeHeader(lines, 15, -5)
	want := HeaderMoney{Subtotal: 150, DiscountTotal: 10, TaxTotal: 11.33, GrandTotal: 161.33}
	if got != want {
		t.Fatalf("ComputeHeader(...) = %+v, want %+v", got, want)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./quote/... -run 'TestComputeLine|TestComputeHeader' -v`
Expected: FAIL — undefined symbols.

- [ ] **Step 3: Write the implementation**

```go
// quote/calc.go
package quote

import "math"

func round2(x float64) float64 { return math.Round(x*100) / 100 }

// CalcLineInput holds the raw per-line quantities and rates used to compute
// line money (kept distinct from types.go's LineInput, the API request shape).
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

// HeaderMoney holds a quote's computed subtotal, discount total, tax total, and grand total.
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

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./quote/... -run 'TestComputeLine|TestComputeHeader' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add quote/calc.go quote/calc_test.go
git commit -m "feat(quote): add line/header money calculation"
```

---

### Task 4: `quote/numbering.go` — document numbering (TDD)

**Files:**
- Create: `quote/numbering.go`
- Test: `quote/numbering_test.go`

**Interfaces:**
- Produces: `FormatNumber(serialID int64) string`.

- [ ] **Step 1: Write the failing test**

```go
// quote/numbering_test.go
package quote

import "testing"

func TestFormatNumber(t *testing.T) {
	tests := []struct {
		name     string
		serialID int64
		want     string
	}{
		{"single digit", 1, "QUOT-000001"},
		{"three digits", 123, "QUOT-000123"},
		{"six digits exact", 654321, "QUOT-654321"},
		{"seven digits not truncated", 1234567, "QUOT-1234567"},
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

Run: `go test ./quote/... -run TestFormatNumber -v`
Expected: FAIL — `FormatNumber` undefined.

- [ ] **Step 3: Write the implementation**

```go
// quote/numbering.go
package quote

import "fmt"

// numberPrefix is the QUOT record-type code (lkp_record_type.record_type_code).
const numberPrefix = "QUOT"

// FormatNumber renders the human-readable document number (spec AD-11): QUOT-000001.
func FormatNumber(serialID int64) string {
	return fmt.Sprintf("%s-%06d", numberPrefix, serialID)
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./quote/... -run TestFormatNumber -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add quote/numbering.go quote/numbering_test.go
git commit -m "feat(quote): add document numbering"
```

---

### Task 5: `quote/transitions.go` — status transition map, `CONV` excluded (TDD)

**Files:**
- Create: `quote/transitions.go`
- Test: `quote/transitions_test.go`

**Interfaces:**
- Produces: `ErrInvalidTransition`, `CanTransition`, `ValidateTransition` — used by `store_transition.go` (Task 10) and `convert.go` (Task 16, which bypasses this map entirely to set `CONV` directly).

- [ ] **Step 1: Write the failing test**

```go
// quote/transitions_test.go
package quote

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
		{"DRFT", "APPV"},
		{"DRFT", "CONV"}, // CONV is never a manual target
		{"SENT", "CONV"}, // even from a plausible source status
		{"APPV", "CONV"},
		{"RJCT", "DRFT"}, // terminal
		{"CANC", "DRFT"}, // terminal
		{"CONV", "DRFT"}, // CONV has no outgoing manual transitions at all
		{"CONV", "CANC"},
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
	if err := ValidateTransition("SENT", "CONV"); !errors.Is(err, ErrInvalidTransition) {
		t.Fatalf("ValidateTransition(SENT, CONV) = %v, want ErrInvalidTransition", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./quote/... -run 'TestCanTransition|TestValidateTransition' -v`
Expected: FAIL — undefined symbols.

- [ ] **Step 3: Write the implementation**

```go
// quote/transitions.go
package quote

import "errors"

// ErrInvalidTransition is returned when a status change is not permitted.
var ErrInvalidTransition = errors.New("invalid quote status transition")

// allowedTransitions maps a status code to the set of codes reachable from it
// via the manual /transition endpoint (spec §7). CONV is deliberately absent
// as both a key and a value anywhere in this map: it is never a legal manual
// transition target (only quote.ConvertToSalesOrder sets it, bypassing this
// map entirely), and a quote already in CONV has no manual transitions out of
// it either (CanTransition("CONV", anything) is always false, since a missing
// map key yields the zero-value nil map on lookup). Terminal states RJCT/
// EXPR/CANC map to an empty set.
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

Run: `go test ./quote/... -run 'TestCanTransition|TestValidateTransition' -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add quote/transitions.go quote/transitions_test.go
git commit -m "feat(quote): add status transition map with CONV excluded from manual moves"
```

---

### Task 6: `quote/resolver.go` — query engine field/sort/search resolver (TDD)

**Files:**
- Create: `quote/resolver.go`
- Test: `quote/resolver_test.go`

**Interfaces:**
- Consumes: `query.FieldResolver`/`SortResolver`/`SearchResolver`, `query.DataType` + constants.
- Produces: `resolver{}` — used by `store_search.go` (Task 12). Includes an `estimate_id` filter field beyond `estimate`'s resolver (spec §11).

- [ ] **Step 1: Write the failing test**

```go
// quote/resolver_test.go
package quote

import (
	"testing"

	"stonesuite-backend/query"
)

func TestResolver_Resolve(t *testing.T) {
	r := resolver{}
	tests := []struct {
		key    string
		wantOK bool
		wantDT query.DataType
	}{
		{"id", true, query.TypeString},
		{"document_number", true, query.TypeString},
		{"customer_id", true, query.TypeString},
		{"estimate_id", true, query.TypeString},
		{"status", true, query.TypeString},
		{"grand_total", true, query.TypeNumber},
		{"quote_date", true, query.TypeDate},
		{"valid_until", true, query.TypeDate},
		{"cf:budget", true, query.TypeString},
		{"cf:Invalid-Key", false, ""},
		{"nonexistent_field", false, ""},
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
	if _, _, ok := r.SortExpr("quote_internal_notes"); ok {
		t.Error("SortExpr(quote_internal_notes) found, want not found")
	}
}

func TestResolver_SearchPredicate(t *testing.T) {
	r := resolver{}
	if pred := r.SearchPredicate("$1"); pred == "" {
		t.Fatal("SearchPredicate returned empty string")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./quote/... -run TestResolver -v`
Expected: FAIL — `resolver` undefined.

- [ ] **Step 3: Write the implementation**

```go
// quote/resolver.go
package quote

import (
	"regexp"
	"strings"

	"stonesuite-backend/query"
)

// validCustomKey mirrors estimate.validCustomKey so JSONB custom keys are
// safe to interpolate.
var validCustomKey = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// resolver implements query.FieldResolver + SortResolver + SearchResolver for
// quote (spec §11). Table alias `quo` matches quoteSelect (Task 7).
type resolver struct{}

type resolved struct {
	expr string
	dt   query.DataType
}

// systemFields is the filter whitelist (spec §11 table). estimate_id is the
// one field beyond estimate's resolver — lets callers filter "quotes that
// came from an estimate" (is_null) or a specific one (eq).
var systemFields = map[string]resolved{
	"id":               {"quo.quote_uuid::text", query.TypeString},
	"document_number":  {"COALESCE(quo.quote_number,'')", query.TypeString},
	"record_number":    {"COALESCE(quo.quote_number,'')", query.TypeString},
	"customer_id":      {"quo.quote_customer_id::text", query.TypeString},
	"estimate_id":      {"quo.quote_estimate_id::text", query.TypeString},
	"status":           {"quo.quote_status::text", query.TypeString},
	"sales_rep_id":     {"quo.quote_sales_rep_id::text", query.TypeString},
	"owner_id":         {"quo.quote_owner_id::text", query.TypeString},
	"quote_date":       {"quo.quote_date", query.TypeDate},
	"valid_until":      {"quo.quote_valid_until", query.TypeDate},
	"currency_id":      {"quo.quote_currency::text", query.TypeString},
	"payment_terms_id": {"quo.quote_payment_terms::text", query.TypeString},
	"price_level_id":   {"quo.quote_price_level::text", query.TypeString},
	"grand_total":      {"quo.quote_grand_total", query.TypeNumber},
	"po_number":        {"quo.quote_po_number", query.TypeString},
	"created_by":       {"quo.quote_created_by::text", query.TypeString},
	"updated_by":       {"quo.quote_updated_by::text", query.TypeString},
	"created_at":       {"quo.quote_created_at", query.TypeDate},
	"updated_at":       {"quo.quote_updated_at", query.TypeDate},
}

func (resolver) Resolve(key string) (string, query.DataType, bool) {
	if s, ok := systemFields[key]; ok {
		return s.expr, s.dt, true
	}
	if k, ok := strings.CutPrefix(key, "cf:"); ok && validCustomKey.MatchString(k) {
		return "quo.quote_custom_fields->>'" + k + "'", query.TypeString, true
	}
	return "", "", false
}

// sortableFields is the stable (NOT NULL) sort whitelist beyond the engine's
// built-in created_at/updated_at/record_number (spec §11).
var sortableFields = map[string]resolved{
	"document_number": {"quo.quote_number", query.TypeString},
	"record_number":   {"quo.quote_number", query.TypeString},
	"quote_date":      {"quo.quote_date", query.TypeDate},
	"grand_total":     {"quo.quote_grand_total", query.TypeNumber},
	"status":          {"quo.quote_status", query.TypeNumber},
	"customer_id":     {"quo.quote_customer_id", query.TypeNumber},
}

func (resolver) SortExpr(key string) (string, query.DataType, bool) {
	if s, ok := sortableFields[key]; ok {
		return s.expr, s.dt, true
	}
	return "", "", false
}

// SearchPredicate powers the list's global-search box (spec §11).
func (resolver) SearchPredicate(ph string) string {
	return "(" +
		"quo.quote_number ILIKE '%'||" + ph + "||'%'" +
		" OR quo.quote_po_number ILIKE '%'||" + ph + "||'%'" +
		" OR quo.quote_memo ILIKE '%'||" + ph + "||'%'" +
		" OR quo.quote_notes ILIKE '%'||" + ph + "||'%'" +
		" OR quo.quote_bill_customer_name ILIKE '%'||" + ph + "||'%'" +
		" OR EXISTS (SELECT 1 FROM quote_item qi WHERE qi.quote_id = quo.quote_id" +
		"   AND (qi.sku ILIKE '%'||" + ph + "||'%' OR qi.item_name ILIKE '%'||" + ph + "||'%'))" +
		" OR EXISTS (SELECT 1 FROM customer c WHERE c.customer_id = quo.quote_customer_id" +
		"   AND (c.customer_name ILIKE '%'||" + ph + "||'%' OR c.customer_doc_num ILIKE '%'||" + ph + "||'%'))" +
		")"
}

var _ query.FieldResolver = resolver{}
var _ query.SortResolver = resolver{}
var _ query.SearchResolver = resolver{}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./quote/... -run TestResolver -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add quote/resolver.go quote/resolver_test.go
git commit -m "feat(quote): add query engine resolver with estimate lineage filter"
```

---

### Task 7: `quote/store.go` — shared helpers, `Get`, scan/select plumbing

**Files:**
- Create: `quote/store.go`

**Interfaces:**
- Consumes: `workflow.Querier`.
- Produces: `ErrNotFound`, `ClientError`, `IsClientError`, `colVal`, `buildInsert`, `buildUpdateSet`, `nullableInt`, `nullableDate`, `orNow`, `isForeignKeyViolation`, `quotRecordTypeCode`, `draftStatusCode`, `recordTypeIDByCode`, `statusIDByCode`, `customerSnapshot`, `customerSnapshotByInternalID`, `itemSnapshot`, `resolveInventoryItem`, `taxPercentForRate`, `overrideAddress`, `addrColVals`, `quoteSelect`, `itemSelect`, `scanQuote`, `scanLine`, `loadLines`, `Get` — every remaining task in this package.

- [ ] **Step 1: Write the file**

```go
// quote/store.go
package quote

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

// ErrNotFound is returned when a quote uuid matches nothing live.
var ErrNotFound = errors.New("quote not found")

// ClientError signals a client-caused failure that a controller maps to
// HTTP 400/409, mirroring estimate.ClientError.
type ClientError struct{ Msg string }

func (e ClientError) Error() string { return e.Msg }

// IsClientError reports whether err is a ClientError.
func IsClientError(err error) bool {
	var ce ClientError
	return errors.As(err, &ce)
}

// quotRecordTypeCode is the lkp_record_type code for Quote (spec §1).
const quotRecordTypeCode = "QUOT"

// draftStatusCode is the status every new quote starts at (spec §7).
const draftStatusCode = "DRFT"

func nullableInt(v int) any {
	if v <= 0 {
		return nil
	}
	return v
}

func nullableDate(d string) any {
	if d == "" {
		return nil
	}
	return d
}

func orNow(d string) string {
	if d == "" {
		return "now"
	}
	return d
}

func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

// colVal pairs a column name with its bind value (and an optional type cast
// suffix) so an INSERT/UPDATE's column list and argument list never drift.
type colVal struct {
	col  string
	val  any
	cast string
}

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

func recordTypeIDByCode(ctx context.Context, q workflow.Querier, code string) (int, error) {
	var id int
	err := q.QueryRow(ctx,
		`SELECT record_type_id FROM lkp_record_type WHERE record_type_code = $1`, code).Scan(&id)
	if err != nil {
		return 0, fmt.Errorf("record type %q: %w", code, err)
	}
	return id, nil
}

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

func customerSnapshotByInternalID(ctx context.Context, q workflow.Querier, custInternalID int) (id int, name string, billing, shipping AddressInput, err error) {
	var uuid string
	if err := q.QueryRow(ctx, `SELECT customer_uuid FROM customer WHERE customer_id = $1`, custInternalID).Scan(&uuid); err != nil {
		return 0, "", AddressInput{}, AddressInput{}, fmt.Errorf("resolve customer uuid: %w", err)
	}
	return customerSnapshot(ctx, q, uuid)
}

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
// address block. prefix is "quote_bill" or "quote_ship".
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

// quoteSelect is the base SELECT shared by Get and Search. Column order must
// match scanQuote's Scan(...) arg order exactly. estimate_number is joined
// via a LEFT JOIN since quote_estimate_id is nullable.
const quoteSelect = `
	SELECT quo.quote_uuid, COALESCE(quo.quote_number,''),
	       rs.record_status_name, rs.record_status_code,
	       quo.quote_approval_status,
	       c.customer_uuid, c.customer_name,
	       COALESCE(ou.id::text,''),
	       est.estimate_uuid, COALESCE(est.estimate_number,''),
	       to_char(quo.quote_date,'YYYY-MM-DD'),
	       COALESCE(to_char(quo.quote_valid_until,'YYYY-MM-DD'),''),
	       quo.quote_po_number, quo.quote_reference_number, quo.quote_memo,
	       quo.quote_notes, quo.quote_internal_notes, quo.quote_terms_conditions,
	       quo.quote_payment_terms, quo.quote_price_level, quo.quote_currency,
	       quo.quote_sales_rep_id, quo.quote_owner_id, quo.quote_sales_tax_percent,
	       quo.quote_ship_same_as_bill,
	       quo.quote_bill_customer_name, quo.quote_bill_attention,
	       quo.quote_bill_addr_line1, quo.quote_bill_addr_line2, quo.quote_bill_addr_suitenum,
	       quo.quote_bill_addr_city, quo.quote_bill_addr_state, quo.quote_bill_addr_zip,
	       quo.quote_bill_addr_country, quo.quote_bill_phone, quo.quote_bill_fax, quo.quote_bill_email,
	       quo.quote_ship_customer_name, quo.quote_ship_attention,
	       quo.quote_ship_addr_line1, quo.quote_ship_addr_line2, quo.quote_ship_addr_suitenum,
	       quo.quote_ship_addr_city, quo.quote_ship_addr_state, quo.quote_ship_addr_zip,
	       quo.quote_ship_addr_country, quo.quote_ship_phone, quo.quote_ship_fax, quo.quote_ship_email,
	       quo.quote_custom_fields,
	       quo.quote_subtotal, quo.quote_discount_total, quo.quote_tax_total,
	       quo.quote_shipping_charge, quo.quote_adjustment, quo.quote_grand_total,
	       quo.quote_created_at, quo.quote_updated_at
	FROM quote quo
	JOIN lkp_record_status rs ON rs.record_status_id = quo.quote_status
	JOIN customer c ON c.customer_id = quo.quote_customer_id
	LEFT JOIN employee oe ON oe.employee_id = quo.quote_owner_id
	LEFT JOIN users ou ON ou.id = oe.employee_user_id
	LEFT JOIN estimate est ON est.estimate_id = quo.quote_estimate_id`

func scanQuote(row pgx.Row) (*Quote, error) {
	var q Quote
	var customRaw []byte
	var estUUID, estNumber *string
	if err := row.Scan(
		&q.ID, &q.Number, &q.Status, &q.StatusCode, &q.ApprovalStatus,
		&q.Customer.ID, &q.Customer.Name, &q.OwnerUserID,
		&estUUID, &estNumber,
		&q.QuoteDate, &q.ValidUntil,
		&q.PONumber, &q.ReferenceNumber, &q.Memo,
		&q.Notes, &q.InternalNotes, &q.TermsConditions,
		&q.PaymentTermsID, &q.PriceLevelID, &q.CurrencyID,
		&q.SalesRepEmployeeID, &q.OwnerEmployeeID, &q.SalesTaxPercent,
		&q.ShipSameAsBilling,
		&q.Billing.CustomerName, &q.Billing.Attention,
		&q.Billing.AddrLine1, &q.Billing.AddrLine2, &q.Billing.SuiteUnit,
		&q.Billing.City, &q.Billing.StateID, &q.Billing.Zip,
		&q.Billing.CountryID, &q.Billing.Phone, &q.Billing.Fax, &q.Billing.Email,
		&q.Shipping.CustomerName, &q.Shipping.Attention,
		&q.Shipping.AddrLine1, &q.Shipping.AddrLine2, &q.Shipping.SuiteUnit,
		&q.Shipping.City, &q.Shipping.StateID, &q.Shipping.Zip,
		&q.Shipping.CountryID, &q.Shipping.Phone, &q.Shipping.Fax, &q.Shipping.Email,
		&customRaw,
		&q.Subtotal, &q.DiscountTotal, &q.TaxTotal,
		&q.ShippingCharge, &q.Adjustment, &q.GrandTotal,
		&q.CreatedAt, &q.UpdatedAt,
	); err != nil {
		return nil, err
	}
	if estUUID != nil {
		num := ""
		if estNumber != nil {
			num = *estNumber
		}
		q.Estimate = &EstimateRef{ID: *estUUID, Number: num}
	}
	if len(customRaw) > 0 {
		_ = json.Unmarshal(customRaw, &q.CustomFields)
	}
	return &q, nil
}

// itemSelect is the base SELECT for a quote's live lines. Column order must
// match scanLine's Scan(...) arg order exactly.
const itemSelect = `
	SELECT qi.quote_item_uuid, qi.line_number,
	       ii.inventory_item_uuid, ei.estimate_item_uuid,
	       qi.sku, qi.item_name, qi.description, COALESCE(qi.unit_code,''),
	       qi.quantity, qi.unit_price, qi.discount_percent, qi.tax_percent,
	       qi.line_subtotal, qi.line_discount, qi.line_tax, qi.line_total
	FROM quote_item qi
	LEFT JOIN inventory_item ii ON ii.inventory_item_id = qi.inventory_item_id
	LEFT JOIN estimate_item ei ON ei.estimate_item_id = qi.estimate_item_id
	WHERE qi.quote_id = $1 AND qi.item_deleted_at IS NULL
	ORDER BY qi.line_number`

func scanLine(row pgx.Rows) (Line, error) {
	var l Line
	err := row.Scan(
		&l.ID, &l.LineNumber, &l.InventoryItemID, &l.EstimateItemID,
		&l.SKU, &l.ItemName, &l.Description, &l.UnitCode,
		&l.Quantity, &l.UnitPrice, &l.DiscountPercent, &l.TaxPercent,
		&l.LineSubtotal, &l.LineDiscount, &l.LineTax, &l.LineTotal,
	)
	return l, err
}

// loadLines fetches a quote's live lines by its external uuid.
func loadLines(ctx context.Context, q workflow.Querier, uuid string) ([]Line, error) {
	var internalID int
	if err := q.QueryRow(ctx,
		`SELECT quote_id FROM quote WHERE quote_uuid = $1`, uuid).Scan(&internalID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("resolve quote id: %w", err)
	}
	rows, err := q.Query(ctx, itemSelect, internalID)
	if err != nil {
		return nil, fmt.Errorf("load quote items: %w", err)
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

// Get loads a single live quote by its external uuid, including its lines.
func Get(ctx context.Context, pool *pgxpool.Pool, uuid string) (*Quote, error) {
	q, err := scanQuote(pool.QueryRow(ctx, quoteSelect+`
		WHERE quo.quote_uuid = $1 AND quo.quote_deleted_at IS NULL`, uuid))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get quote: %w", err)
	}
	items, err := loadLines(ctx, pool, uuid)
	if err != nil {
		return nil, err
	}
	q.Items = items
	return q, nil
}
```

- [ ] **Step 2: Verify it compiles**

Run: `go build ./quote/...`
Expected: exits 0.

- [ ] **Step 3: Commit**

```bash
git add quote/store.go
git commit -m "feat(quote): add store helpers, Get, and scan/select plumbing"
```

---

### Task 8: `quote/store_create.go` — `Create` (+ `resolveLines`, `insertLines`, `writeHistory`)

**Files:**
- Create: `quote/store_create.go`
- Test: `quote/store_test.go` (new file; `//go:build dbtest`-gated)

**Interfaces:**
- Consumes: Tasks 3, 7.
- Produces: `resolvedLine`, `resolveLines`, `insertLines`, `writeHistory`, `Create(ctx, pool, in CreateQuoteInput, actorEmployeeID int) (*Quote, error)` — reused by Tasks 9-11; `Create` called by `controllers/quote.go` (Task 13). Note: a direct `Create` call never sets `quote_estimate_id` — only `estimate.ConvertToQuote` (Task 15) does, by inserting directly rather than calling this `Create` function (see Task 15's note).

- [ ] **Step 1: Write the failing test**

```go
// quote/store_test.go
//go:build dbtest

package quote

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/query"
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
		VALUES ($1, $2, 40.00, 1) RETURNING inventory_item_uuid`,
		"SKU-"+suffix, "Test Item "+suffix).Scan(&itemUUID); err != nil {
		t.Fatalf("seed inventory item: %v", err)
	}
	return custUUID, itemUUID
}

func TestCreate_SnapshotsAndTotals(t *testing.T) {
	pool := testPool(t)
	custUUID, itemUUID := seedCustomerAndItem(t, pool)

	in := CreateQuoteInput{
		CustomerUUID: custUUID,
		quoteFields: quoteFields{
			SalesTaxPercent: 5,
			Items:           []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 2}},
		},
	}
	got, err := Create(context.Background(), pool, in, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if got.Number == "" || got.Number[:5] != "QUOT-" {
		t.Errorf("Number = %q, want QUOT- prefix", got.Number)
	}
	if got.StatusCode != "DRFT" {
		t.Errorf("StatusCode = %q, want DRFT", got.StatusCode)
	}
	if got.Estimate != nil {
		t.Errorf("Estimate = %+v, want nil for a direct (non-converted) create", got.Estimate)
	}
	// subtotal = 2*40=80, tax = 80*0.05=4, grand = 84
	if got.GrandTotal != 84 {
		t.Errorf("GrandTotal = %v, want 84", got.GrandTotal)
	}
}

func TestCreate_RequiresCustomer(t *testing.T) {
	pool := testPool(t)
	_, err := Create(context.Background(), pool, CreateQuoteInput{}, 1)
	if !IsClientError(err) {
		t.Fatalf("Create with no customer = %v, want ClientError", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `TEST_DATABASE_URL=<your test db dsn> go test ./quote/... -tags dbtest -run TestCreate -v`
Expected: FAIL to compile — `Create`, `resolveLines` undefined. (If `TEST_DATABASE_URL` is unset, expect `SKIP` instead; proceed to Step 3 regardless.)

- [ ] **Step 3: Write the implementation**

```go
// quote/store_create.go
package quote

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"stonesuite-backend/workflow"
)

// resolvedLine is a line after catalog/free-text resolution, ready to price
// and insert. estimateItemID is set only by estimate.ConvertToQuote's own
// direct-insert path (Task 15), never by resolveLines/Create (a direct
// create has no estimate lineage — spec §12).
type resolvedLine struct {
	lineNumber      int
	inventoryItemID *int
	estimateItemID  *int
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
// (or free text), computing each line's stored money (spec §8).
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

// insertLines bulk-inserts resolved lines as quote_item rows.
func insertLines(ctx context.Context, tx pgx.Tx, quoteInternalID int, lines []resolvedLine, actorEmployeeID int) error {
	for _, l := range lines {
		_, err := tx.Exec(ctx, `
			INSERT INTO quote_item (
				quote_id, line_number, inventory_item_id, estimate_item_id,
				item_name, sku, description, unit_id, unit_code,
				quantity, unit_price, discount_percent, tax_rate_id, tax_percent,
				line_subtotal, line_discount, line_tax, line_total,
				item_created_by
			) VALUES ($1,$2,$3,$4, $5,$6,$7,$8,$9, $10,$11,$12,$13,$14, $15,$16,$17,$18, $19)`,
			quoteInternalID, l.lineNumber, l.inventoryItemID, l.estimateItemID,
			l.name, l.sku, l.desc, l.unitID, l.unitCode,
			l.quantity, l.unitPrice, l.discountPercent, l.taxRateID, l.taxPercent,
			l.money.Subtotal, l.money.Discount, l.money.Tax, l.money.Total,
			nullableInt(actorEmployeeID),
		)
		if err != nil {
			if isForeignKeyViolation(err) {
				return ClientError{Msg: fmt.Sprintf("Line %d: an invalid unit or tax rate was referenced.", l.lineNumber)}
			}
			return fmt.Errorf("insert quote item: %w", err)
		}
	}
	return nil
}

// writeHistory records one quote_history row inside the caller's transaction.
func writeHistory(ctx context.Context, tx pgx.Tx, quoteInternalID int, action string, fromStatusID, toStatusID *int, actorEmployeeID int) {
	_, _ = tx.Exec(ctx, `
		INSERT INTO quote_history (quote_id, from_status_id, to_status_id, action, actor_employee_id)
		VALUES ($1,$2,$3,$4,$5)`,
		quoteInternalID, fromStatusID, toStatusID, action, nullableInt(actorEmployeeID))
}

// Create inserts a new, standalone quote (header + lines) inside one
// transaction: snapshots billing/shipping from the customer, resolves and
// prices every line, computes header totals, assigns the quote number, and
// starts the quote at DRFT (spec §5.5, AD-4, AD-7, AD-11). quote_estimate_id
// is always NULL here — Estimate lineage is set only by
// estimate.ConvertToQuote (Task 15), which inserts directly rather than
// calling this function (spec §12: direct create rejects an estimateUuid
// field, enforced at the controller/DTO level since CreateQuoteInput has no
// such field to begin with).
func Create(ctx context.Context, pool *pgxpool.Pool, in CreateQuoteInput, actorEmployeeID int) (*Quote, error) {
	if strings.TrimSpace(in.CustomerUUID) == "" {
		return nil, ClientError{Msg: "A customer is required."}
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin create quote: %w", err)
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

	recordTypeID, err := recordTypeIDByCode(ctx, tx, quotRecordTypeCode)
	if err != nil {
		return nil, fmt.Errorf("resolve QUOT record type: %w", err)
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
		{"quote_status", draftStatusID, ""},
		{"quote_customer_id", custInternalID, ""},
		{"quote_po_number", in.PONumber, ""},
		{"quote_reference_number", in.ReferenceNumber, ""},
		{"quote_date", orNow(in.QuoteDate), "::date"},
		{"quote_valid_until", nullableDate(in.ValidUntil), "::date"},
		{"quote_sales_tax_percent", in.SalesTaxPercent, ""},
		{"quote_memo", in.Memo, ""},
		{"quote_notes", in.Notes, ""},
		{"quote_internal_notes", in.InternalNotes, ""},
		{"quote_terms_conditions", in.TermsConditions, ""},
		{"quote_sales_rep_id", in.SalesRepEmployeeID, ""},
		{"quote_owner_id", nullableInt(ownerEmployeeID), ""},
		{"quote_payment_terms", in.PaymentTermsID, ""},
		{"quote_price_level", in.PriceLevelID, ""},
		{"quote_currency", in.CurrencyID, ""},
		{"quote_subtotal", header.Subtotal, ""},
		{"quote_discount_total", header.DiscountTotal, ""},
		{"quote_tax_total", header.TaxTotal, ""},
		{"quote_shipping_charge", in.ShippingCharge, ""},
		{"quote_adjustment", in.Adjustment, ""},
		{"quote_grand_total", header.GrandTotal, ""},
		{"quote_ship_same_as_bill", in.ShipSameAsBilling, ""},
		{"quote_custom_fields", custom, ""},
		{"quote_created_by", nullableInt(actorEmployeeID), ""},
	}
	cv = append(cv, addrColVals("quote_bill", billing)...)
	cv = append(cv, addrColVals("quote_ship", shipping)...)

	insertSQL, insertArgs := buildInsert("quote", cv, "quote_id, quote_uuid")
	var internalID int
	var newUUID string
	err = tx.QueryRow(ctx, insertSQL, insertArgs...).Scan(&internalID, &newUUID)
	if err != nil {
		if isForeignKeyViolation(err) {
			return nil, ClientError{Msg: "One of the referenced ids (payment terms, price level, currency, state, or country) does not exist."}
		}
		return nil, fmt.Errorf("insert quote: %w", err)
	}

	number := FormatNumber(int64(internalID))
	if _, err := tx.Exec(ctx,
		`UPDATE quote SET quote_number = $1 WHERE quote_id = $2`, number, internalID); err != nil {
		return nil, fmt.Errorf("set quote number: %w", err)
	}

	if err := insertLines(ctx, tx, internalID, lines, actorEmployeeID); err != nil {
		return nil, err
	}

	writeHistory(ctx, tx, internalID, "create", nil, &draftStatusID, actorEmployeeID)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit create quote: %w", err)
	}
	_ = custName
	return Get(ctx, pool, newUUID)
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `TEST_DATABASE_URL=<your test db dsn> go test ./quote/... -tags dbtest -run TestCreate -v`
Expected: PASS, or `SKIP` if no test DB — in that case run `go build -tags dbtest ./quote/...` and expect exit 0.

- [ ] **Step 5: Commit**

```bash
git add quote/store_create.go quote/store_test.go
git commit -m "feat(quote): add Create with line resolution and snapshot pricing"
```

---

### Task 9: `quote/store_update.go` — `Update`, `SoftDelete`

**Files:**
- Create: `quote/store_update.go`
- Modify: `quote/store_test.go` (add tests)

**Interfaces:**
- Consumes: Tasks 3, 7, 8.
- Produces: `Update(ctx, pool, uuid string, in UpdateQuoteInput, actorEmployeeID int) (*Quote, error)`, `SoftDelete(ctx, pool, uuid string, actorEmployeeID int) error`.

- [ ] **Step 1: Write the failing test** (append to `quote/store_test.go`)

```go
func TestUpdate_RecomputesTotalsAndBumpsVersion(t *testing.T) {
	pool := testPool(t)
	custUUID, itemUUID := seedCustomerAndItem(t, pool)

	created, err := Create(context.Background(), pool, CreateQuoteInput{
		CustomerUUID: custUUID,
		quoteFields:  quoteFields{Items: []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}}},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	updated, err := Update(context.Background(), pool, created.ID, UpdateQuoteInput{
		quoteFields: quoteFields{Items: []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 3}}},
	}, 1)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	// 3 * 40 = 120
	if updated.GrandTotal != 120 {
		t.Errorf("GrandTotal after update = %v, want 120", updated.GrandTotal)
	}
}

func TestSoftDelete_ThenGetReturnsNotFound(t *testing.T) {
	pool := testPool(t)
	custUUID, itemUUID := seedCustomerAndItem(t, pool)

	created, err := Create(context.Background(), pool, CreateQuoteInput{
		CustomerUUID: custUUID,
		quoteFields:  quoteFields{Items: []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}}},
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

- [ ] **Step 2: Run test to verify it fails**

Run: `TEST_DATABASE_URL=<your test db dsn> go test ./quote/... -tags dbtest -run 'TestUpdate|TestSoftDelete' -v`
Expected: FAIL to compile — `Update`, `SoftDelete` undefined.

- [ ] **Step 3: Write the implementation**

```go
// quote/store_update.go
package quote

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Update replaces a live quote's header fields and lines (recomputing
// totals) inside one transaction. Rejected once the quote has reached a
// terminal status (RJCT/EXPR/CANC) — CONV is intentionally NOT blocked here,
// since a converted quote may still need header corrections (e.g. notes)
// without affecting the already-created sales order(s), consistent with
// CONV not being a hard lock (spec AD-6, §7).
func Update(ctx context.Context, pool *pgxpool.Pool, uuid string, in UpdateQuoteInput, actorEmployeeID int) (*Quote, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin update quote: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var internalID, custInternalID int
	var statusCode string
	err = tx.QueryRow(ctx, `
		SELECT quo.quote_id, quo.quote_customer_id, rs.record_status_code
		FROM quote quo JOIN lkp_record_status rs ON rs.record_status_id = quo.quote_status
		WHERE quo.quote_uuid = $1 AND quo.quote_deleted_at IS NULL`, uuid,
	).Scan(&internalID, &custInternalID, &statusCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load quote for update: %w", err)
	}
	if statusCode == "RJCT" || statusCode == "EXPR" || statusCode == "CANC" {
		return nil, ClientError{Msg: "A rejected, expired, or cancelled quote cannot be edited."}
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
		{"quote_po_number", in.PONumber, ""},
		{"quote_reference_number", in.ReferenceNumber, ""},
		{"quote_date", orNow(in.QuoteDate), "::date"},
		{"quote_valid_until", nullableDate(in.ValidUntil), "::date"},
		{"quote_sales_tax_percent", in.SalesTaxPercent, ""},
		{"quote_memo", in.Memo, ""},
		{"quote_notes", in.Notes, ""},
		{"quote_internal_notes", in.InternalNotes, ""},
		{"quote_terms_conditions", in.TermsConditions, ""},
		{"quote_sales_rep_id", in.SalesRepEmployeeID, ""},
		{"quote_owner_id", in.OwnerEmployeeID, ""},
		{"quote_payment_terms", in.PaymentTermsID, ""},
		{"quote_price_level", in.PriceLevelID, ""},
		{"quote_currency", in.CurrencyID, ""},
		{"quote_subtotal", header.Subtotal, ""},
		{"quote_discount_total", header.DiscountTotal, ""},
		{"quote_tax_total", header.TaxTotal, ""},
		{"quote_shipping_charge", in.ShippingCharge, ""},
		{"quote_adjustment", in.Adjustment, ""},
		{"quote_grand_total", header.GrandTotal, ""},
		{"quote_ship_same_as_bill", in.ShipSameAsBilling, ""},
		{"quote_custom_fields", in.CustomFields, ""},
	}
	cv = append(cv, addrColVals("quote_bill", billing)...)
	cv = append(cv, addrColVals("quote_ship", shipping)...)
	cv = append(cv, colVal{"quote_updated_by", nullableInt(actorEmployeeID), ""})

	updateSQL, updateArgs := buildUpdateSet("quote", []any{uuid}, cv,
		[]string{"quote_updated_at = NOW()", "quote_record_version = quote_record_version + 1"},
		"quote_uuid = $1 AND quote_deleted_at IS NULL")
	_, err = tx.Exec(ctx, updateSQL, updateArgs...)
	if err != nil {
		if isForeignKeyViolation(err) {
			return nil, ClientError{Msg: "One of the referenced ids (payment terms, price level, currency, state, or country) does not exist."}
		}
		return nil, fmt.Errorf("update quote: %w", err)
	}

	if _, err := tx.Exec(ctx,
		`UPDATE quote_item SET item_deleted_at = NOW() WHERE quote_id = $1 AND item_deleted_at IS NULL`,
		internalID); err != nil {
		return nil, fmt.Errorf("clear previous quote items: %w", err)
	}
	if err := insertLines(ctx, tx, internalID, lines, actorEmployeeID); err != nil {
		return nil, err
	}

	writeHistory(ctx, tx, internalID, "update", nil, nil, actorEmployeeID)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit update quote: %w", err)
	}
	_ = custName
	return Get(ctx, pool, uuid)
}

// SoftDelete marks a live quote deleted.
func SoftDelete(ctx context.Context, pool *pgxpool.Pool, uuid string, actorEmployeeID int) error {
	tag, err := pool.Exec(ctx, `
		UPDATE quote
		SET quote_deleted_at = NOW(), quote_deleted_by = $2
		WHERE quote_uuid = $1 AND quote_deleted_at IS NULL`,
		uuid, nullableInt(actorEmployeeID))
	if err != nil {
		return fmt.Errorf("delete quote: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `TEST_DATABASE_URL=<your test db dsn> go test ./quote/... -tags dbtest -run 'TestUpdate|TestSoftDelete' -v`
Expected: PASS, or `SKIP` if no test DB — in that case run `go build -tags dbtest ./quote/...` and expect exit 0.

- [ ] **Step 5: Commit**

```bash
git add quote/store_update.go quote/store_test.go
git commit -m "feat(quote): add Update and SoftDelete"
```

---

### Task 10: `quote/store_transition.go` — `Transition`

**Files:**
- Create: `quote/store_transition.go`
- Modify: `quote/store_test.go` (add tests)

**Interfaces:**
- Consumes: Tasks 5, 7, 8 (`writeHistory`), Task 11 (`activeApproverCount`).
- Produces: `Transition(ctx, pool, uuid, toStatusCode string, actorEmployeeID int) (*Quote, error)`.

> **Ordering note (same as Plan A Task 10):** this references `activeApproverCount`, defined in Task 11. Write the code now; expect full compilation only once Task 11 lands.

- [ ] **Step 1: Write the failing test** (append to `quote/store_test.go`)

```go
func TestTransition_DraftToPendingApproval(t *testing.T) {
	pool := testPool(t)
	custUUID, itemUUID := seedCustomerAndItem(t, pool)

	created, err := Create(context.Background(), pool, CreateQuoteInput{
		CustomerUUID: custUUID,
		quoteFields:  quoteFields{Items: []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}}},
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

func TestTransition_RejectsConvAsManualTarget(t *testing.T) {
	pool := testPool(t)
	custUUID, itemUUID := seedCustomerAndItem(t, pool)

	created, err := Create(context.Background(), pool, CreateQuoteInput{
		CustomerUUID: custUUID,
		quoteFields:  quoteFields{Items: []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}}},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := Transition(context.Background(), pool, created.ID, "CONV", 1); !IsClientError(err) {
		t.Fatalf("Transition to CONV = %v, want ClientError", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `TEST_DATABASE_URL=<your test db dsn> go test ./quote/... -tags dbtest -run TestTransition -v`
Expected: FAIL to compile (per the ordering note).

- [ ] **Step 3: Write the implementation**

```go
// quote/store_transition.go
package quote

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Transition moves a live quote to toStatusCode, validating the move against
// the static transition map (spec §7), row-locking the quote to serialize
// concurrent transitions, and writing a history row. CONV is explicitly
// rejected here as a manual target (400, ClientError) — it is set only by
// quote.ConvertToSalesOrder (Task 16), never through this endpoint (spec §7).
func Transition(ctx context.Context, pool *pgxpool.Pool, uuid, toStatusCode string, actorEmployeeID int) (*Quote, error) {
	if toStatusCode == "CONV" {
		return nil, ClientError{Msg: "CONV is not a valid manual transition target; use the convert-to-sales-order endpoint."}
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transition: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var internalID, curStatusID int
	var curStatusCode, approvalStatus string
	err = tx.QueryRow(ctx, `
		SELECT quo.quote_id, quo.quote_status, rs.record_status_code, quo.quote_approval_status
		FROM quote quo JOIN lkp_record_status rs ON rs.record_status_id = quo.quote_status
		WHERE quo.quote_uuid = $1 AND quo.quote_deleted_at IS NULL
		FOR UPDATE OF quo`, uuid,
	).Scan(&internalID, &curStatusID, &curStatusCode, &approvalStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load quote for transition: %w", err)
	}
	if err := ValidateTransition(curStatusCode, toStatusCode); err != nil {
		return nil, err
	}

	recordTypeID, err := recordTypeIDByCode(ctx, tx, quotRecordTypeCode)
	if err != nil {
		return nil, fmt.Errorf("resolve QUOT record type: %w", err)
	}
	toStatusID, err := statusIDByCode(ctx, tx, recordTypeID, toStatusCode)
	if err != nil {
		return nil, ClientError{Msg: "Unknown target status."}
	}

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
		UPDATE quote SET
			quote_status = $2, quote_approval_status = $4, quote_approved_by = NULL,
			quote_updated_at = NOW(),
			quote_updated_by = $3, quote_record_version = quote_record_version + 1
		WHERE quote_id = $1`, internalID, toStatusID, nullableInt(actorEmployeeID), newApprovalStatus); err != nil {
		return nil, fmt.Errorf("transition quote: %w", err)
	}

	writeHistory(ctx, tx, internalID, "transition", &curStatusID, &toStatusID, actorEmployeeID)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit transition: %w", err)
	}
	return Get(ctx, pool, uuid)
}
```

- [ ] **Step 4: Confirm it compiles once Task 11 lands, then run the tests**

Run (after Task 11): `TEST_DATABASE_URL=<your test db dsn> go test ./quote/... -tags dbtest -run TestTransition -v`
Expected: PASS, or `SKIP` if no test DB — in that case run `go build -tags dbtest ./quote/...` and expect exit 0.

- [ ] **Step 5: Commit**

```bash
git add quote/store_transition.go quote/store_test.go
git commit -m "feat(quote): add Transition with approval gate and CONV rejection"
```

---

### Task 11: `quote/approval.go` — `Approve` (AD-8)

**Files:**
- Create: `quote/approval.go`
- Modify: `quote/store_test.go` (add tests)

**Interfaces:**
- Consumes: Tasks 7, 8 (`writeHistory`).
- Produces: `ErrNotApprover`, `ErrApprovalRequired`, `ErrApprovalNotRequired`, `approvalNone`/`approvalPending`/`approvalApproved`, `activeApproverCount`, `signOffCount`, `isConfiguredApprover`, `Approve(ctx, pool, uuid string, approverEmployeeID int) (*Quote, error)` — `activeApproverCount` unblocks Task 10.

- [ ] **Step 1: Write the failing test** (append to `quote/store_test.go`)

```go
func TestApprove_RequiresConfiguredApprover(t *testing.T) {
	pool := testPool(t)
	custUUID, itemUUID := seedCustomerAndItem(t, pool)

	created, err := Create(context.Background(), pool, CreateQuoteInput{
		CustomerUUID: custUUID,
		quoteFields:  quoteFields{Items: []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}}},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := Transition(context.Background(), pool, created.ID, "PAPV", 1); err != nil {
		t.Fatalf("Transition to PAPV: %v", err)
	}
	if _, err := Approve(context.Background(), pool, created.ID, 1); !errors.Is(err, ErrApprovalNotRequired) {
		t.Fatalf("Approve with no configured approvers = %v, want ErrApprovalNotRequired", err)
	}
}

func TestApprove_SignOffFlipsApprovalStatus(t *testing.T) {
	pool := testPool(t)
	custUUID, itemUUID := seedCustomerAndItem(t, pool)
	ctx := context.Background()

	created, err := Create(ctx, pool, CreateQuoteInput{
		CustomerUUID: custUUID,
		quoteFields:  quoteFields{Items: []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}}},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := Transition(ctx, pool, created.ID, "PAPV", 1); err != nil {
		t.Fatalf("Transition to PAPV: %v", err)
	}

	var recordTypeID, papvStatusID int
	if err := pool.QueryRow(ctx, `SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'QUOT'`).Scan(&recordTypeID); err != nil {
		t.Fatalf("resolve QUOT record type: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT record_status_id FROM lkp_record_status WHERE record_status_record_type = $1 AND record_status_code = 'PAPV'`, recordTypeID).Scan(&papvStatusID); err != nil {
		t.Fatalf("resolve PAPV status: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO quote_approver (record_type_id, record_status_id, approver_employee_id)
		VALUES ($1, $2, 1) ON CONFLICT DO NOTHING`, recordTypeID, papvStatusID); err != nil {
		t.Fatalf("seed quote_approver: %v", err)
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

Run: `TEST_DATABASE_URL=<your test db dsn> go test ./quote/... -tags dbtest -run TestApprove -v`
Expected: FAIL to compile — `Approve`, `ErrApprovalNotRequired` undefined.

- [ ] **Step 3: Write the implementation**

```go
// quote/approval.go
package quote

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/workflow"
)

// Approval status values stored in quote.quote_approval_status (AD-8).
const (
	approvalNone     = "none"
	approvalPending  = "pending"
	approvalApproved = "approved"
)

// ErrNotApprover is returned when a caller who is not a configured approver
// for the quote's current status tries to approve it (AD-8). Maps to 403.
var ErrNotApprover = errors.New("you are not a configured approver for this quote's current status")

// ErrApprovalRequired is returned when a quote is asked to leave a status
// that still requires approval sign-off (AD-8). Maps to HTTP 409.
var ErrApprovalRequired = errors.New("this quote must be approved before it can leave its current status")

// ErrApprovalNotRequired is returned when an approval is submitted for a
// quote whose current status has no configured approvers (AD-8). Maps to 409.
var ErrApprovalNotRequired = errors.New("this quote's current status does not require approval")

func activeApproverCount(ctx context.Context, q workflow.Querier, recordTypeID, statusID int) (int, error) {
	var n int
	if err := q.QueryRow(ctx, `
		SELECT COUNT(*) FROM quote_approver
		WHERE record_type_id = $1 AND record_status_id = $2 AND is_active`,
		recordTypeID, statusID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count quote approvers: %w", err)
	}
	return n, nil
}

func signOffCount(ctx context.Context, q workflow.Querier, quoteInternalID, statusID int) (int, error) {
	var n int
	if err := q.QueryRow(ctx, `
		SELECT COUNT(*) FROM quote_approval
		WHERE quote_id = $1 AND record_status_id = $2`,
		quoteInternalID, statusID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count quote approvals: %w", err)
	}
	return n, nil
}

func isConfiguredApprover(ctx context.Context, q workflow.Querier, recordTypeID, statusID, employeeID int) (bool, error) {
	var exists bool
	if err := q.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM quote_approver
			WHERE record_type_id = $1 AND record_status_id = $2 AND approver_employee_id = $3 AND is_active)`,
		recordTypeID, statusID, employeeID).Scan(&exists); err != nil {
		return false, fmt.Errorf("check quote approver: %w", err)
	}
	return exists, nil
}

// Approve records one approver's sign-off on a quote at its current status
// (AD-8). Requires the caller to be a configured approver, is idempotent per
// (quote, status, approver), and flips approval_status to 'approved' once
// the sign-off count reaches the configured approver count.
func Approve(ctx context.Context, pool *pgxpool.Pool, uuid string, approverEmployeeID int) (*Quote, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin approve quote: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var internalID, curStatusID int
	err = tx.QueryRow(ctx, `
		SELECT quote_id, quote_status FROM quote
		WHERE quote_uuid = $1 AND quote_deleted_at IS NULL
		FOR UPDATE`, uuid).Scan(&internalID, &curStatusID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load quote for approval: %w", err)
	}

	recordTypeID, err := recordTypeIDByCode(ctx, tx, quotRecordTypeCode)
	if err != nil {
		return nil, fmt.Errorf("resolve QUOT record type: %w", err)
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
		INSERT INTO quote_approval (quote_id, record_status_id, approver_employee_id)
		VALUES ($1, $2, $3)
		ON CONFLICT (quote_id, record_status_id, approver_employee_id) DO NOTHING`,
		internalID, curStatusID, approverEmployeeID); err != nil {
		return nil, fmt.Errorf("record quote approval: %w", err)
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
		UPDATE quote SET
			quote_approval_status = $2, quote_approved_by = $3, quote_updated_at = NOW()
		WHERE quote_id = $1`, internalID, newStatus, approvedBy); err != nil {
		return nil, fmt.Errorf("update quote approval status: %w", err)
	}

	writeHistory(ctx, tx, internalID, "approve", &curStatusID, &curStatusID, approverEmployeeID)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit approve quote: %w", err)
	}
	return Get(ctx, pool, uuid)
}
```

- [ ] **Step 4: Run all store tests to verify they pass**

Run: `TEST_DATABASE_URL=<your test db dsn> go test ./quote/... -tags dbtest -v`
Expected: PASS for every test in the package so far, or `SKIP` if no test DB — in that case run `go build -tags dbtest ./quote/...` and `go vet -tags dbtest ./quote/...`, both exit 0.

- [ ] **Step 5: Commit**

```bash
git add quote/approval.go quote/store_test.go
git commit -m "feat(quote): add Approve with configuration-driven approval gate"
```

---

### Task 12: `quote/store_search.go` — `Search` (query engine integration)

**Files:**
- Create: `quote/store_search.go`
- Modify: `quote/store_test.go` (add test)

**Interfaces:**
- Consumes: Task 6 (`resolver{}`), `query.Build`, `query.NextCursor`, `query.Request`.
- Produces: `Search(ctx, pool, scope, actorIdentityID string, req query.Request) (Page, error)`, `employeeIDByIdentity`, `sortValue`.

- [ ] **Step 1: Write the failing test** (append to `quote/store_test.go`)

```go
func TestSearch_ReturnsCreatedQuote(t *testing.T) {
	pool := testPool(t)
	custUUID, itemUUID := seedCustomerAndItem(t, pool)
	ctx := context.Background()

	created, err := Create(ctx, pool, CreateQuoteInput{
		CustomerUUID: custUUID,
		quoteFields:  quoteFields{Items: []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}}},
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
		t.Errorf("Search(%q) did not include the created quote", created.Number)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `TEST_DATABASE_URL=<your test db dsn> go test ./quote/... -tags dbtest -run TestSearch -v`
Expected: FAIL to compile — `Search` undefined.

- [ ] **Step 3: Write the implementation**

```go
// quote/store_search.go
package quote

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/query"
)

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

// Search lists quotes under the caller's RBAC scope with filter/sort/global
// search + keyset pagination. List rows omit line items.
func Search(ctx context.Context, pool *pgxpool.Pool, scope, actorIdentityID string, req query.Request) (Page, error) {
	where := []string{"quo.quote_deleted_at IS NULL"}
	var args []any
	nextIdx := 1
	if scope == "own" || scope == "team" {
		empID, found := employeeIDByIdentity(ctx, pool, actorIdentityID)
		if !found {
			return Page{}, nil
		}
		where = append(where, fmt.Sprintf("quo.quote_owner_id = $%d", nextIdx))
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

	q := quoteSelect + " WHERE " + strings.Join(where, " AND ") +
		" ORDER BY " + built.OrderBy + " LIMIT " + strconv.Itoa(built.EffLimit+1)

	rows, err := pool.Query(ctx, q, args...)
	if err != nil {
		return Page{}, fmt.Errorf("search quotes: %w", err)
	}
	defer rows.Close()
	out := []Quote{}
	for rows.Next() {
		e, err := scanQuote(rows)
		if err != nil {
			return Page{}, err
		}
		out = append(out, *e)
	}
	if err := rows.Err(); err != nil {
		return Page{}, fmt.Errorf("search quotes: %w", err)
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

func sortValue(q Quote, field string) any {
	switch field {
	case "updated_at":
		return q.UpdatedAt
	case "grand_total":
		return q.GrandTotal
	case "quote_date":
		return q.QuoteDate
	case "document_number", "record_number":
		return q.Number
	default:
		return q.CreatedAt
	}
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `TEST_DATABASE_URL=<your test db dsn> go test ./quote/... -tags dbtest -v`
Expected: PASS for every test in the package, or `SKIP` if no test DB — in that case run `go build -tags dbtest ./quote/...` and expect exit 0.

- [ ] **Step 5: Commit**

```bash
git add quote/store_search.go quote/store_test.go
git commit -m "feat(quote): add Search with keyset pagination"
```

---

### Task 13: `controllers/quote.go` — HTTP handlers (RBAC, IDOR, error mapping)

**Files:**
- Create: `controllers/quote.go`

**Interfaces:**
- Consumes: `middleware.GetUserFromContext`, `tenancy.PoolFromContext`, `authz.Check`, `authz.ResourceQuote`, `recordInScope`, `logSecurityEvent`, `resolveEmployeeID`, `writeJSON`/`fail`, every `quote.*` function from Tasks 7-12.
- Produces: `QuoteOps`, `NewQuoteOps()`, `quoteFail` — consumed by `main.go` (Task 17) and Task 14 (`controllers/quote_audit.go`) and Task 16 (`ConvertToSalesOrder` handler, added to this file).

- [ ] **Step 1: Write the file**

```go
// controllers/quote.go
package controllers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/middleware"
	"stonesuite-backend/query"
	"stonesuite-backend/quote"
	"stonesuite-backend/tenancy"
)

// QuoteOps handles the Quote endpoints: a dedicated relational module
// (header + line items), a sibling of the Estimate/Sales Order/Invoice
// modules. Mirrors EstimateOps' auth/IDOR/error-mapping conventions.
//
// Routes:
//
//	GET    /api/tenant/quotes                              — unfiltered list (cursor-paginated)
//	POST   /api/tenant/quotes/search                        — filter + sort + search + pagination
//	POST   /api/tenant/quotes                                — create
//	GET    /api/tenant/quotes/{uuid}                         — get (+ items)
//	PATCH  /api/tenant/quotes/{uuid}                          — update
//	DELETE /api/tenant/quotes/{uuid}                          — soft delete
//	POST   /api/tenant/quotes/{uuid}/transition               — status change
//	POST   /api/tenant/quotes/{uuid}/approve                  — approval sign-off
//	POST   /api/tenant/quotes/{uuid}/convert-to-sales-order    — Quote -> Sales Order conversion (Task 16)
//	GET    /api/tenant/quotes/{uuid}/audit                     — audit trail
type QuoteOps struct{}

// NewQuoteOps constructs the handler group.
func NewQuoteOps() *QuoteOps { return &QuoteOps{} }

// authQuote resolves JWT + tenant pool + the quote:<action> RBAC grant for
// requests with no specific record yet (list/search/create).
func (h *QuoteOps) authQuote(w http.ResponseWriter, r *http.Request, action authz.Action) (*pgxpool.Pool, string, authz.Scope, bool) {
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
	decision, err := authz.Check(r.Context(), pool, payload.ID, authz.ResourceQuote, action)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, "", "", false
	}
	if !decision.Allowed {
		fail(w, http.StatusForbidden, "You do not have permission to "+string(action)+" quotes.")
		return nil, "", "", false
	}
	return pool, payload.ID, decision.Scope, true
}

// authQuoteByUUID resolves auth for a single-record action, then enforces
// the row-level IDOR guard. Denial returns 404 (not 403).
func (h *QuoteOps) authQuoteByUUID(w http.ResponseWriter, r *http.Request, uuid string, action authz.Action) (*pgxpool.Pool, string, *quote.Quote, bool) {
	pool, identityID, scope, ok := h.authQuote(w, r, action)
	if !ok {
		return nil, "", nil, false
	}
	q, err := quote.Get(r.Context(), pool, uuid)
	if errors.Is(err, quote.ErrNotFound) {
		fail(w, http.StatusNotFound, "Quote not found.")
		return nil, "", nil, false
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load quote.")
		return nil, "", nil, false
	}
	if scope != authz.ScopeAll {
		allowed, aerr := recordInScope(r.Context(), pool, scope, identityID, q.OwnerUserID, "")
		if aerr != nil {
			fail(w, http.StatusInternalServerError, "Permission check failed.")
			return nil, "", nil, false
		}
		if !allowed {
			logSecurityEvent(r, "idor_denied",
				"identity", identityID, "record", uuid, "resource", "quote",
				"action", string(action), "scope", string(scope))
			fail(w, http.StatusNotFound, "Quote not found.")
			return nil, "", nil, false
		}
	}
	return pool, identityID, q, true
}

// quoteFail maps a store error to an HTTP response.
func quoteFail(w http.ResponseWriter, err error, serverMsg string) {
	switch {
	case errors.Is(err, quote.ErrNotFound):
		fail(w, http.StatusNotFound, "Quote not found.")
	case errors.Is(err, quote.ErrInvalidTransition),
		errors.Is(err, quote.ErrApprovalRequired),
		errors.Is(err, quote.ErrApprovalNotRequired):
		fail(w, http.StatusConflict, err.Error())
	case errors.Is(err, quote.ErrNotApprover):
		fail(w, http.StatusForbidden, err.Error())
	case quote.IsClientError(err):
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

// List GET /api/tenant/quotes
func (h *QuoteOps) List(w http.ResponseWriter, r *http.Request) {
	pool, identityID, scope, ok := h.authQuote(w, r, authz.ActionRead)
	if !ok {
		return
	}
	req := query.Request{Cursor: r.URL.Query().Get("cursor"), Search: r.URL.Query().Get("search")}
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil {
		req.Limit = n
	}
	h.search(w, r, pool, identityID, scope, req)
}

// Search POST /api/tenant/quotes/search
func (h *QuoteOps) Search(w http.ResponseWriter, r *http.Request) {
	pool, identityID, scope, ok := h.authQuote(w, r, authz.ActionRead)
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

func (h *QuoteOps) search(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, identityID string, scope authz.Scope, req query.Request) {
	page, err := quote.Search(r.Context(), pool, string(scope), identityID, req)
	if err != nil {
		quoteFail(w, err, "Failed to search quotes.")
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

// Create POST /api/tenant/quotes
func (h *QuoteOps) Create(w http.ResponseWriter, r *http.Request) {
	pool, identityID, _, ok := h.authQuote(w, r, authz.ActionCreate)
	if !ok {
		return
	}
	var in quote.CreateQuoteInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	q, err := quote.Create(r.Context(), pool, in, resolveEmployeeID(r, identityID))
	if err != nil {
		quoteFail(w, err, "Failed to create quote.")
		return
	}
	auditQuote(r, pool, identityID, "create", q.ID, nil, q)
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "quote": q})
}

// ---- single record ------------------------------------------------------------

// Get GET /api/tenant/quotes/{uuid}
func (h *QuoteOps) Get(w http.ResponseWriter, r *http.Request) {
	_, _, q, ok := h.authQuoteByUUID(w, r, r.PathValue("uuid"), authz.ActionRead)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "quote": q})
}

// Update PATCH /api/tenant/quotes/{uuid}
func (h *QuoteOps) Update(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, before, ok := h.authQuoteByUUID(w, r, uuid, authz.ActionUpdate)
	if !ok {
		return
	}
	var in quote.UpdateQuoteInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	after, err := quote.Update(r.Context(), pool, uuid, in, resolveEmployeeID(r, identityID))
	if err != nil {
		quoteFail(w, err, "Failed to update quote.")
		return
	}
	auditQuote(r, pool, identityID, "update", uuid, before, after)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "quote": after})
}

// Delete DELETE /api/tenant/quotes/{uuid}
func (h *QuoteOps) Delete(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, before, ok := h.authQuoteByUUID(w, r, uuid, authz.ActionDelete)
	if !ok {
		return
	}
	if err := quote.SoftDelete(r.Context(), pool, uuid, resolveEmployeeID(r, identityID)); err != nil {
		quoteFail(w, err, "Failed to delete quote.")
		return
	}
	auditQuoteDelete(r, pool, identityID, uuid, before)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Quote deleted."})
}

// Transition POST /api/tenant/quotes/{uuid}/transition  body {"toStatusCode":"..."}
func (h *QuoteOps) Transition(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, _, ok := h.authQuoteByUUID(w, r, uuid, authz.ActionTransition)
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
	q, err := quote.Transition(r.Context(), pool, uuid, req.ToStatusCode, resolveEmployeeID(r, identityID))
	if err != nil {
		quoteFail(w, err, "Failed to apply transition.")
		return
	}
	auditQuote(r, pool, identityID, "transition", uuid, nil, q)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "quote": q})
}

// Approve POST /api/tenant/quotes/{uuid}/approve
func (h *QuoteOps) Approve(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, _, ok := h.authQuoteByUUID(w, r, uuid, authz.ActionTransition)
	if !ok {
		return
	}
	q, err := quote.Approve(r.Context(), pool, uuid, resolveEmployeeID(r, identityID))
	if err != nil {
		if errors.Is(err, quote.ErrNotApprover) {
			logSecurityEvent(r, "approval_denied", "identity", identityID, "record", uuid)
		}
		quoteFail(w, err, "Failed to approve quote.")
		return
	}
	auditQuote(r, pool, identityID, "approve", uuid, nil, q)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "quote": q})
}
```

- [ ] **Step 2: Verify it compiles** (will fail until Task 14 adds `auditQuote`/`auditQuoteDelete` — expected)

Run: `go build ./controllers/...`
Expected: FAIL — `auditQuote`, `auditQuoteDelete` undefined. Expected at this point; not a regression.

- [ ] **Step 3: Commit**

```bash
git add controllers/quote.go
git commit -m "feat(quote): add QuoteOps HTTP handlers"
```

---

### Task 14: `controllers/quote_audit.go` — audit trail

**Files:**
- Create: `controllers/quote_audit.go`

**Interfaces:**
- Consumes: `workflow.UserIDByIdentity`, `workflow.LogAuditFull`, `loadAuditEntries` (generic, reused as-is), `clientIP`/`appVersion` (generic).
- Produces: `auditQuote`, `auditQuoteDelete`, `(*QuoteOps).Audit` — completes `QuoteOps` from Task 13.

- [ ] **Step 1: Write the file**

```go
// controllers/quote_audit.go
package controllers

import (
	"log"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/quote"
	"stonesuite-backend/workflow"
)

// quoteSnapshot flattens a quote into a JSON-able map for the audit trail.
func quoteSnapshot(q *quote.Quote) map[string]any {
	if q == nil {
		return nil
	}
	return map[string]any{
		"id":         q.ID,
		"quoteNumber": q.Number,
		"status":     q.Status,
		"customerId": q.Customer.ID,
		"grandTotal": q.GrandTotal,
	}
}

// auditQuote records a Quote mutation in the unified audit_logs table.
// Best-effort: failures are logged, never returned.
func auditQuote(r *http.Request, pool *pgxpool.Pool, identityID, action, recordID string, oldQuote, newQuote *quote.Quote) {
	ctx := r.Context()
	actorUserID, _ := workflow.UserIDByIdentity(ctx, pool, identityID)
	if err := workflow.LogAuditFull(ctx, pool, actorUserID, action, "quote", recordID, "quote",
		quoteSnapshot(oldQuote), quoteSnapshot(newQuote), nil,
		clientIP(r), r.Header.Get("X-Session-Id"), appVersion); err != nil {
		log.Printf("quote: audit %s %s: %v", action, recordID, err)
	}
}

// auditQuoteDelete is the delete-specific variant.
func auditQuoteDelete(r *http.Request, pool *pgxpool.Pool, identityID, recordID string, oldQuote *quote.Quote) {
	ctx := r.Context()
	actorUserID, _ := workflow.UserIDByIdentity(ctx, pool, identityID)
	if err := workflow.LogAuditFull(ctx, pool, actorUserID, "delete", "quote", recordID, "quote",
		quoteSnapshot(oldQuote), nil, nil,
		clientIP(r), r.Header.Get("X-Session-Id"), appVersion); err != nil {
		log.Printf("quote: audit delete %s: %v", recordID, err)
	}
}

// Audit GET /api/tenant/quotes/{uuid}/audit
func (h *QuoteOps) Audit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, _, _, ok := h.authQuoteByUUID(w, r, id, authz.ActionRead)
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

- [ ] **Step 2: Verify the controllers package compiles**

Run: `go build ./controllers/...`
Expected: exits 0.

- [ ] **Step 3: Commit**

```bash
git add controllers/quote_audit.go
git commit -m "feat(quote): add audit trail recording and endpoint"
```

---

### Task 15: `estimate/convert.go` — Estimate → Quote conversion (spec §9.1)

**Files:**
- Create: `estimate/convert.go`
- Modify: `controllers/estimate.go` (add the `ConvertToQuote` handler)
- Modify: `estimate/store_test.go` (add a `dbtest` test) — wait, this test needs the `quote` package, so it actually belongs alongside a cross-package integration test; create it in a new file to keep `estimate/store_test.go` free of a `quote` import (see Step 1 below for the exact file).

**Interfaces:**
- Consumes: `quote.ComputeLine`, `quote.ComputeHeader`, `quote.CalcLineInput`, `quote.FormatNumber`, `quote.Get`, `quote.Quote` (all exported, Tasks 3, 4, 7). This is the one place `estimate` imports `quote` — a one-directional dependency (`quote` never imports `estimate`'s Go code, only references it via the nullable `quote_estimate_id`/`estimate_item_id` FKs in SQL).
- Produces: `ConvertToQuote(ctx, pool, estimateUUID string, actorEmployeeID int) (*quote.Quote, error)` — called by `controllers/estimate.go`'s new `ConvertToQuote` handler.

> **Why this doesn't call `quote.Create`:** `quote.Create` (Task 8) resolves lines via `resolveLines`, which re-reads `inventory_item`'s *current* price/sku/name for any line with an `InventoryItemUUID`. That would silently re-price the quote against today's catalog instead of preserving the estimate's frozen snapshot — the opposite of spec AD-4/§8's "copies `estimate_item`'s already-frozen snapshot columns verbatim." So this function inserts directly into `quote`/`quote_item`/`quote_history` with its own SQL (mirroring `quote.Create`'s shape, not calling it), then calls the exported `quote.Get` once at the end to return a fully-populated object — the same pattern the design spec describes for Quote→SalesOrder in Task 16.

- [ ] **Step 1: Write the failing test**

```go
// estimate/convert_test.go
//go:build dbtest

package estimate

import (
	"context"
	"testing"
)

func TestConvertToQuote_CopiesSnapshotAndSetsLineage(t *testing.T) {
	pool := testPool(t)
	custUUID, itemUUID := seedCustomerAndItem(t, pool)
	ctx := context.Background()

	created, err := Create(ctx, pool, CreateEstimateInput{
		CustomerUUID:   custUUID,
		estimateFields: estimateFields{SalesTaxPercent: 8, Items: []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 2}}},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := Transition(ctx, pool, created.ID, "PAPV", 1); err != nil {
		t.Fatalf("Transition to PAPV: %v", err)
	}
	if _, err := Transition(ctx, pool, created.ID, "APPV", 1); err != nil {
		t.Fatalf("Transition to APPV: %v", err)
	}

	q, err := ConvertToQuote(ctx, pool, created.ID, 1)
	if err != nil {
		t.Fatalf("ConvertToQuote: %v", err)
	}
	if q.Number == "" || q.Number[:5] != "QUOT-" {
		t.Errorf("Number = %q, want QUOT- prefix", q.Number)
	}
	if q.Estimate == nil || q.Estimate.ID != created.ID {
		t.Errorf("Estimate lineage = %+v, want ID %q", q.Estimate, created.ID)
	}
	if len(q.Items) != 1 || q.Items[0].UnitPrice != created.Items[0].UnitPrice {
		t.Errorf("Items = %+v, want snapshot copied from estimate (unit price %v)", q.Items, created.Items[0].UnitPrice)
	}
	if q.GrandTotal != created.GrandTotal {
		t.Errorf("GrandTotal = %v, want %v (recomputed from copied inputs, should match since header adjustments are both zero)", q.GrandTotal, created.GrandTotal)
	}
}

func TestConvertToQuote_RejectsDraftEstimate(t *testing.T) {
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
	if _, err := ConvertToQuote(ctx, pool, created.ID, 1); !IsClientError(err) {
		t.Fatalf("ConvertToQuote on DRFT estimate = %v, want ClientError", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `TEST_DATABASE_URL=<your test db dsn> go test ./estimate/... -tags dbtest -run TestConvertToQuote -v`
Expected: FAIL to compile — `ConvertToQuote` undefined.

- [ ] **Step 3: Write the implementation**

```go
// estimate/convert.go
package estimate

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/quote"
)

// convertibleEstimateStatuses are the estimate statuses eligible for
// conversion to a quote (spec §9.1): the estimate must have been approved
// (and, having gone through SENT, plausibly accepted by the customer) —
// DRFT/PAPV/RJCT/EXPR/CANC estimates have nothing quotable yet or anymore.
var convertibleEstimateStatuses = map[string]bool{"APPV": true, "SENT": true}

// ConvertToQuote creates a new Quote from a live, approved-or-sent Estimate
// (spec §9.1): re-fetches the estimate + lines inside a transaction, copies
// the billing/shipping snapshot and every line's frozen snapshot columns
// verbatim (never re-reading inventory_item), recomputes quote-side money
// from those copied inputs, assigns the quote number, and starts the quote
// at DRFT. Does not mutate the source estimate's status — multiple quotes
// may be created from one estimate (spec AD-5).
func ConvertToQuote(ctx context.Context, pool *pgxpool.Pool, estimateUUID string, actorEmployeeID int) (*quote.Quote, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin convert estimate to quote: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var estInternalID, custInternalID int
	var statusCode string
	err = tx.QueryRow(ctx, `
		SELECT est.estimate_id, est.estimate_customer_id, rs.record_status_code
		FROM estimate est JOIN lkp_record_status rs ON rs.record_status_id = est.estimate_status
		WHERE est.estimate_uuid = $1 AND est.estimate_deleted_at IS NULL
		FOR UPDATE OF est`, estimateUUID,
	).Scan(&estInternalID, &custInternalID, &statusCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load estimate for conversion: %w", err)
	}
	if !convertibleEstimateStatuses[statusCode] {
		return nil, ClientError{Msg: "Estimate must be Approved or Sent to convert to a quote."}
	}

	// Load the estimate header's snapshot/terms fields needed on the new quote.
	var (
		poNumber, refNumber, memo, notes, internalNotes, terms string
		paymentTermsID, priceLevelID, currencyID                *int
		salesRepID, ownerID                                     *int
		salesTaxPercent, shippingCharge, adjustment              float64
		shipSameAsBill                                            bool
		billName, billAttn, billL1, billL2, billSuite, billCity   string
		billState, billCountry                                    *int
		billZip, billPhone, billFax, billEmail                    string
		shipName, shipAttn, shipL1, shipL2, shipSuite, shipCity   string
		shipState, shipCountry                                    *int
		shipZip, shipPhone, shipFax, shipEmail                    string
	)
	err = tx.QueryRow(ctx, `
		SELECT estimate_po_number, estimate_reference_number, estimate_memo, estimate_notes, estimate_internal_notes, estimate_terms_conditions,
		       estimate_payment_terms, estimate_price_level, estimate_currency,
		       estimate_sales_rep_id, estimate_owner_id, estimate_sales_tax_percent,
		       estimate_shipping_charge, estimate_adjustment, estimate_ship_same_as_bill,
		       estimate_bill_customer_name, estimate_bill_attention, estimate_bill_addr_line1, estimate_bill_addr_line2, estimate_bill_addr_suitenum,
		       estimate_bill_addr_city, estimate_bill_addr_state, estimate_bill_addr_zip, estimate_bill_addr_country,
		       estimate_bill_phone, estimate_bill_fax, estimate_bill_email,
		       estimate_ship_customer_name, estimate_ship_attention, estimate_ship_addr_line1, estimate_ship_addr_line2, estimate_ship_addr_suitenum,
		       estimate_ship_addr_city, estimate_ship_addr_state, estimate_ship_addr_zip, estimate_ship_addr_country,
		       estimate_ship_phone, estimate_ship_fax, estimate_ship_email
		FROM estimate WHERE estimate_id = $1`, estInternalID).Scan(
		&poNumber, &refNumber, &memo, &notes, &internalNotes, &terms,
		&paymentTermsID, &priceLevelID, &currencyID,
		&salesRepID, &ownerID, &salesTaxPercent,
		&shippingCharge, &adjustment, &shipSameAsBill,
		&billName, &billAttn, &billL1, &billL2, &billSuite, &billCity, &billState, &billZip, &billCountry,
		&billPhone, &billFax, &billEmail,
		&shipName, &shipAttn, &shipL1, &shipL2, &shipSuite, &shipCity, &shipState, &shipZip, &shipCountry,
		&shipPhone, &shipFax, &shipEmail,
	)
	if err != nil {
		return nil, fmt.Errorf("load estimate snapshot for conversion: %w", err)
	}

	// Load the estimate's live lines with internal ids (not the public Get/Line
	// API, which only exposes external uuids — the quote_item insert needs the
	// internal inventory_item_id/estimate_item_id FK values).
	rows, err := tx.Query(ctx, `
		SELECT estimate_item_id, inventory_item_id,
		       item_name, sku, description, unit_id, unit_code,
		       quantity, unit_price, discount_percent, tax_rate_id, tax_percent
		FROM estimate_item WHERE estimate_id = $1 AND item_deleted_at IS NULL ORDER BY line_number`, estInternalID)
	if err != nil {
		return nil, fmt.Errorf("load estimate items for conversion: %w", err)
	}
	type estLine struct {
		itemID, invItemID                    int
		hasInvItem                           bool
		name, sku, desc, unitCode             string
		unitID                                *int
		quantity, unitPrice, discountPercent  float64
		taxRateID                             *int
		taxPercent                            float64
	}
	var lines []estLine
	for rows.Next() {
		var l estLine
		var invItemID *int
		if err := rows.Scan(&l.itemID, &invItemID, &l.name, &l.sku, &l.desc, &l.unitID, &l.unitCode,
			&l.quantity, &l.unitPrice, &l.discountPercent, &l.taxRateID, &l.taxPercent); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan estimate item for conversion: %w", err)
		}
		if invItemID != nil {
			l.invItemID, l.hasInvItem = *invItemID, true
		}
		lines = append(lines, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load estimate items for conversion: %w", err)
	}
	rows.Close()
	if len(lines) == 0 {
		return nil, ClientError{Msg: "Estimate must have at least one line to convert."}
	}

	// Recompute quote-side money from the copied inputs (defensive recompute,
	// not a re-price — spec §8).
	lineMoney := make([]quote.LineMoney, len(lines))
	for i, l := range lines {
		lineMoney[i] = quote.ComputeLine(quote.CalcLineInput{
			Quantity: l.quantity, UnitPrice: l.unitPrice,
			DiscountPercent: l.discountPercent, TaxPercent: l.taxPercent,
		})
	}
	header := quote.ComputeHeader(lineMoney, shippingCharge, adjustment)

	var quotRecordTypeID, draftStatusID int
	if err := tx.QueryRow(ctx, `SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'QUOT'`).Scan(&quotRecordTypeID); err != nil {
		return nil, fmt.Errorf("resolve QUOT record type: %w", err)
	}
	if err := tx.QueryRow(ctx, `
		SELECT record_status_id FROM lkp_record_status
		WHERE record_status_record_type = $1 AND record_status_code = 'DRFT'`, quotRecordTypeID).Scan(&draftStatusID); err != nil {
		return nil, fmt.Errorf("resolve QUOT DRFT status: %w", err)
	}

	var quoteInternalID int
	var quoteUUID string
	err = tx.QueryRow(ctx, `
		INSERT INTO quote (
			record_type, quote_status, quote_estimate_id, quote_customer_id,
			quote_po_number, quote_reference_number, quote_memo, quote_notes, quote_internal_notes, quote_terms_conditions,
			quote_payment_terms, quote_price_level, quote_currency,
			quote_sales_rep_id, quote_owner_id, quote_sales_tax_percent,
			quote_shipping_charge, quote_adjustment, quote_ship_same_as_bill,
			quote_subtotal, quote_discount_total, quote_tax_total, quote_grand_total,
			quote_bill_customer_name, quote_bill_attention, quote_bill_addr_line1, quote_bill_addr_line2, quote_bill_addr_suitenum,
			quote_bill_addr_city, quote_bill_addr_state, quote_bill_addr_zip, quote_bill_addr_country,
			quote_bill_phone, quote_bill_fax, quote_bill_email,
			quote_ship_customer_name, quote_ship_attention, quote_ship_addr_line1, quote_ship_addr_line2, quote_ship_addr_suitenum,
			quote_ship_addr_city, quote_ship_addr_state, quote_ship_addr_zip, quote_ship_addr_country,
			quote_ship_phone, quote_ship_fax, quote_ship_email,
			quote_created_by
		) VALUES (
			$1,$2,$3,$4,
			$5,$6,$7,$8,$9,$10,
			$11,$12,$13,
			$14,$15,$16,
			$17,$18,$19,
			$20,$21,$22,$23,
			$24,$25,$26,$27,$28,
			$29,$30,$31,$32,
			$33,$34,$35,
			$36,$37,$38,$39,$40,
			$41,$42,$43,$44,
			$45,$46,$47,
			$48
		) RETURNING quote_id, quote_uuid`,
		quotRecordTypeID, draftStatusID, estInternalID, custInternalID,
		poNumber, refNumber, memo, notes, internalNotes, terms,
		paymentTermsID, priceLevelID, currencyID,
		salesRepID, nullableInt(func() int {
			if ownerID != nil {
				return *ownerID
			}
			return actorEmployeeID
		}()), salesTaxPercent,
		shippingCharge, adjustment, shipSameAsBill,
		header.Subtotal, header.DiscountTotal, header.TaxTotal, header.GrandTotal,
		billName, billAttn, billL1, billL2, billSuite,
		billCity, billState, billZip, billCountry,
		billPhone, billFax, billEmail,
		shipName, shipAttn, shipL1, shipL2, shipSuite,
		shipCity, shipState, shipZip, shipCountry,
		shipPhone, shipFax, shipEmail,
		nullableInt(actorEmployeeID),
	).Scan(&quoteInternalID, &quoteUUID)
	if err != nil {
		return nil, fmt.Errorf("insert quote from estimate conversion: %w", err)
	}

	number := quote.FormatNumber(int64(quoteInternalID))
	if _, err := tx.Exec(ctx, `UPDATE quote SET quote_number = $1 WHERE quote_id = $2`, number, quoteInternalID); err != nil {
		return nil, fmt.Errorf("set quote number: %w", err)
	}

	for i, l := range lines {
		var invItemID any
		if l.hasInvItem {
			invItemID = l.invItemID
		}
		_, err := tx.Exec(ctx, `
			INSERT INTO quote_item (
				quote_id, line_number, inventory_item_id, estimate_item_id,
				item_name, sku, description, unit_id, unit_code,
				quantity, unit_price, discount_percent, tax_rate_id, tax_percent,
				line_subtotal, line_discount, line_tax, line_total,
				item_created_by
			) VALUES ($1,$2,$3,$4, $5,$6,$7,$8,$9, $10,$11,$12,$13,$14, $15,$16,$17,$18, $19)`,
			quoteInternalID, i+1, invItemID, l.itemID,
			l.name, l.sku, l.desc, l.unitID, l.unitCode,
			l.quantity, l.unitPrice, l.discountPercent, l.taxRateID, l.taxPercent,
			lineMoney[i].Subtotal, lineMoney[i].Discount, lineMoney[i].Tax, lineMoney[i].Total,
			nullableInt(actorEmployeeID),
		)
		if err != nil {
			return nil, fmt.Errorf("insert quote item from estimate conversion: %w", err)
		}
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO quote_history (quote_id, to_status_id, action, actor_employee_id, snapshot)
		VALUES ($1, $2, 'convert', $3, $4)`,
		quoteInternalID, draftStatusID, nullableInt(actorEmployeeID),
		fmt.Sprintf(`{"sourceEstimateId": %d}`, estInternalID)); err != nil {
		return nil, fmt.Errorf("write quote history for conversion: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit convert estimate to quote: %w", err)
	}
	return quote.Get(ctx, pool, quoteUUID)
}
```

- [ ] **Step 4: Add the `ConvertToQuote` handler to `controllers/estimate.go`**

Append this method to the existing `controllers/estimate.go` (created in Plan A Task 13), and add `"stonesuite-backend/quote"` — no, the handler does not need to import `quote` directly since `estimate.ConvertToQuote` already returns `*quote.Quote`, which the handler only needs to marshal via `writeJSON`'s `any` parameter (no explicit type reference needed in the handler body):

```go
// ConvertToQuote POST /api/tenant/estimates/{uuid}/convert-to-quote
// Requires estimate:read (+ IDOR) on the source estimate and quote:create
// (spec §9.1, §10).
func (h *EstimateOps) ConvertToQuote(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, _, ok := h.authEstimateByUUID(w, r, uuid, authz.ActionRead)
	if !ok {
		return
	}
	decision, err := authz.Check(r.Context(), pool, identityID, authz.ResourceQuote, authz.ActionCreate)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return
	}
	if !decision.Allowed {
		fail(w, http.StatusForbidden, "You do not have permission to create quotes.")
		return
	}
	q, err := estimate.ConvertToQuote(r.Context(), pool, uuid, resolveEmployeeID(r, identityID))
	if err != nil {
		estimateFail(w, err, "Failed to convert estimate to quote.")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "quote": q})
}
```

- [ ] **Step 5: Run tests to verify they pass**

Run: `TEST_DATABASE_URL=<your test db dsn> go test ./estimate/... -tags dbtest -run TestConvertToQuote -v`
Expected: PASS, or `SKIP` if no test DB — in that case run `go build -tags dbtest ./estimate/... ./controllers/...` and expect exit 0 for both.

- [ ] **Step 6: Commit**

```bash
git add estimate/convert.go estimate/convert_test.go controllers/estimate.go
git commit -m "feat(estimate): add ConvertToQuote conversion service and endpoint"
```

---

### Task 16: `quote/convert.go` — Quote → Sales Order conversion (spec §9.2, AD-6)

**Files:**
- Create: `quote/convert.go`
- Create: `quote/convert_test.go` (`//go:build dbtest`)
- Modify: `controllers/quote.go` (add the `ConvertToSalesOrder` handler)

**Interfaces:**
- Consumes: `salesorder.ComputeLine`, `salesorder.ComputeHeader`, `salesorder.LineInput` (the calc-only type — `salesorder` package, unlike this plan's `estimate`/`quote` packages, did not need to rename it since it has no colliding API-shape `LineInput`; see Plan A Task 3's note), `salesorder.FormatNumber`, `salesorder.Get`, `salesorder.Order` (all exported, from the already-shipped Sales Order module). This is the one place `quote` imports `salesorder` — `sales_order`/`sales_order_item` schemas are untouched (AD-6).
- Produces: `ConvertToSalesOrder(ctx, pool, quoteUUID string, actorEmployeeID int) (*salesorder.Order, error)` — called by `controllers/quote.go`'s new `ConvertToSalesOrder` handler.

> **Why this doesn't call `salesorder.Create`:** identical reasoning to Task 15 — `salesorder.Create` re-resolves lines against the live `inventory_item` catalog, which would defeat the "copy the quote's already-frozen snapshot verbatim" requirement (spec §8). This function inserts directly into `sales_order`/`sales_order_item` with its own SQL (mirroring `salesorder.Create`'s column shape), then calls the exported `salesorder.Get` to return a fully-populated object. It additionally inserts one `quote_conversion` row and updates `quote_status` to `CONV` directly (bypassing `ValidateTransition`, per spec §7 — `CONV` is a system-only status change).

- [ ] **Step 1: Write the failing test**

```go
// quote/convert_test.go
//go:build dbtest

package quote

import (
	"context"
	"testing"
)

func TestConvertToSalesOrder_CopiesSnapshotAndRecordsLineage(t *testing.T) {
	pool := testPool(t)
	custUUID, itemUUID := seedCustomerAndItem(t, pool)
	ctx := context.Background()

	created, err := Create(ctx, pool, CreateQuoteInput{
		CustomerUUID: custUUID,
		quoteFields:  quoteFields{SalesTaxPercent: 6, Items: []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 4}}},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := Transition(ctx, pool, created.ID, "PAPV", 1); err != nil {
		t.Fatalf("Transition to PAPV: %v", err)
	}
	if _, err := Transition(ctx, pool, created.ID, "APPV", 1); err != nil {
		t.Fatalf("Transition to APPV: %v", err)
	}

	so, err := ConvertToSalesOrder(ctx, pool, created.ID, 1)
	if err != nil {
		t.Fatalf("ConvertToSalesOrder: %v", err)
	}
	if so.Number == "" || so.Number[:5] != "SORD-" {
		t.Errorf("Number = %q, want SORD- prefix", so.Number)
	}
	if len(so.Items) != 1 || so.Items[0].Quantity != 4 {
		t.Errorf("Items = %+v, want single line qty 4", so.Items)
	}

	reloaded, err := Get(ctx, pool, created.ID)
	if err != nil {
		t.Fatalf("Get quote after conversion: %v", err)
	}
	if reloaded.StatusCode != "CONV" {
		t.Errorf("quote StatusCode after conversion = %q, want CONV", reloaded.StatusCode)
	}

	// A second conversion from the same (now CONV) quote must still succeed,
	// producing a second, distinct sales order (spec: many SOs per quote allowed).
	so2, err := ConvertToSalesOrder(ctx, pool, created.ID, 1)
	if err != nil {
		t.Fatalf("second ConvertToSalesOrder = %v, want success (multiple conversions allowed)", err)
	}
	if so2.ID == so.ID {
		t.Error("second conversion returned the same sales order id, want a distinct one")
	}
}

func TestConvertToSalesOrder_RejectsDraftQuote(t *testing.T) {
	pool := testPool(t)
	custUUID, itemUUID := seedCustomerAndItem(t, pool)
	ctx := context.Background()

	created, err := Create(ctx, pool, CreateQuoteInput{
		CustomerUUID: custUUID,
		quoteFields:  quoteFields{Items: []LineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1}}},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := ConvertToSalesOrder(ctx, pool, created.ID, 1); !IsClientError(err) {
		t.Fatalf("ConvertToSalesOrder on DRFT quote = %v, want ClientError", err)
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `TEST_DATABASE_URL=<your test db dsn> go test ./quote/... -tags dbtest -run TestConvertToSalesOrder -v`
Expected: FAIL to compile — `ConvertToSalesOrder` undefined.

- [ ] **Step 3: Write the implementation**

```go
// quote/convert.go
package quote

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/salesorder"
)

// convertibleQuoteStatuses are the quote statuses eligible for conversion to
// a sales order (spec §9.2). CONV is included so a quote may be converted
// more than once (progress/phased conversion, spec AD-6) — it is not a hard
// lock, only excluded from the manual /transition endpoint (Task 10).
var convertibleQuoteStatuses = map[string]bool{"APPV": true, "SENT": true, "CONV": true}

// ConvertToSalesOrder creates a new Sales Order from a live, convertible
// Quote (spec §9.2, AD-6): re-fetches the quote + lines inside a
// transaction, copies the billing/shipping snapshot and every line's frozen
// snapshot columns verbatim into sales_order/sales_order_item, records one
// quote_conversion row, and — only on the first conversion — flips the
// quote's status to CONV. sales_order/sales_order_item's own schema and
// insert shape (mirrored here, not called directly) are untouched.
func ConvertToSalesOrder(ctx context.Context, pool *pgxpool.Pool, quoteUUID string, actorEmployeeID int) (*salesorder.Order, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin convert quote to sales order: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var quoteInternalID, custInternalID, curStatusID int
	var statusCode string
	err = tx.QueryRow(ctx, `
		SELECT quo.quote_id, quo.quote_customer_id, quo.quote_status, rs.record_status_code
		FROM quote quo JOIN lkp_record_status rs ON rs.record_status_id = quo.quote_status
		WHERE quo.quote_uuid = $1 AND quo.quote_deleted_at IS NULL
		FOR UPDATE OF quo`, quoteUUID,
	).Scan(&quoteInternalID, &custInternalID, &curStatusID, &statusCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load quote for conversion: %w", err)
	}
	if !convertibleQuoteStatuses[statusCode] {
		return nil, ClientError{Msg: "Quote must be Approved, Sent, or already Converted to convert to a sales order."}
	}

	var (
		poNumber, refNumber, memo, notes, internalNotes, terms string
		paymentTermsID, priceLevelID, currencyID                *int
		salesRepID, ownerID                                     *int
		salesTaxPercent, shippingCharge, adjustment              float64
		shipSameAsBill                                            bool
		billName, billAttn, billL1, billL2, billSuite, billCity   string
		billState, billCountry                                    *int
		billZip, billPhone, billFax, billEmail                    string
		shipName, shipAttn, shipL1, shipL2, shipSuite, shipCity   string
		shipState, shipCountry                                    *int
		shipZip, shipPhone, shipFax, shipEmail                    string
	)
	err = tx.QueryRow(ctx, `
		SELECT quote_po_number, quote_reference_number, quote_memo, quote_notes, quote_internal_notes, quote_terms_conditions,
		       quote_payment_terms, quote_price_level, quote_currency,
		       quote_sales_rep_id, quote_owner_id, quote_sales_tax_percent,
		       quote_shipping_charge, quote_adjustment, quote_ship_same_as_bill,
		       quote_bill_customer_name, quote_bill_attention, quote_bill_addr_line1, quote_bill_addr_line2, quote_bill_addr_suitenum,
		       quote_bill_addr_city, quote_bill_addr_state, quote_bill_addr_zip, quote_bill_addr_country,
		       quote_bill_phone, quote_bill_fax, quote_bill_email,
		       quote_ship_customer_name, quote_ship_attention, quote_ship_addr_line1, quote_ship_addr_line2, quote_ship_addr_suitenum,
		       quote_ship_addr_city, quote_ship_addr_state, quote_ship_addr_zip, quote_ship_addr_country,
		       quote_ship_phone, quote_ship_fax, quote_ship_email
		FROM quote WHERE quote_id = $1`, quoteInternalID).Scan(
		&poNumber, &refNumber, &memo, &notes, &internalNotes, &terms,
		&paymentTermsID, &priceLevelID, &currencyID,
		&salesRepID, &ownerID, &salesTaxPercent,
		&shippingCharge, &adjustment, &shipSameAsBill,
		&billName, &billAttn, &billL1, &billL2, &billSuite, &billCity, &billState, &billZip, &billCountry,
		&billPhone, &billFax, &billEmail,
		&shipName, &shipAttn, &shipL1, &shipL2, &shipSuite, &shipCity, &shipState, &shipZip, &shipCountry,
		&shipPhone, &shipFax, &shipEmail,
	)
	if err != nil {
		return nil, fmt.Errorf("load quote snapshot for conversion: %w", err)
	}

	rows, err := tx.Query(ctx, `
		SELECT quote_item_id, inventory_item_id,
		       item_name, sku, description, unit_id, unit_code,
		       quantity, unit_price, discount_percent, tax_rate_id, tax_percent
		FROM quote_item WHERE quote_id = $1 AND item_deleted_at IS NULL ORDER BY line_number`, quoteInternalID)
	if err != nil {
		return nil, fmt.Errorf("load quote items for conversion: %w", err)
	}
	type quoLine struct {
		itemID, invItemID                    int
		hasInvItem                           bool
		name, sku, desc, unitCode             string
		unitID                                *int
		quantity, unitPrice, discountPercent  float64
		taxRateID                             *int
		taxPercent                            float64
	}
	var lines []quoLine
	for rows.Next() {
		var l quoLine
		var invItemID *int
		if err := rows.Scan(&l.itemID, &invItemID, &l.name, &l.sku, &l.desc, &l.unitID, &l.unitCode,
			&l.quantity, &l.unitPrice, &l.discountPercent, &l.taxRateID, &l.taxPercent); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan quote item for conversion: %w", err)
		}
		if invItemID != nil {
			l.invItemID, l.hasInvItem = *invItemID, true
		}
		lines = append(lines, l)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("load quote items for conversion: %w", err)
	}
	rows.Close()
	if len(lines) == 0 {
		return nil, ClientError{Msg: "Quote must have at least one line to convert."}
	}

	lineMoney := make([]salesorder.LineMoney, len(lines))
	for i, l := range lines {
		lineMoney[i] = salesorder.ComputeLine(salesorder.LineInput{
			Quantity: l.quantity, UnitPrice: l.unitPrice,
			DiscountPercent: l.discountPercent, TaxPercent: l.taxPercent,
		})
	}
	header := salesorder.ComputeHeader(lineMoney, shippingCharge, adjustment)

	var sordRecordTypeID, draftStatusID int
	if err := tx.QueryRow(ctx, `SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'SORD'`).Scan(&sordRecordTypeID); err != nil {
		return nil, fmt.Errorf("resolve SORD record type: %w", err)
	}
	if err := tx.QueryRow(ctx, `
		SELECT record_status_id FROM lkp_record_status
		WHERE record_status_record_type = $1 AND record_status_code = 'DRFT'`, sordRecordTypeID).Scan(&draftStatusID); err != nil {
		return nil, fmt.Errorf("resolve SORD DRFT status: %w", err)
	}

	ownerEmployeeID := actorEmployeeID
	if ownerID != nil && *ownerID > 0 {
		ownerEmployeeID = *ownerID
	}

	var soInternalID int
	var soUUID string
	err = tx.QueryRow(ctx, `
		INSERT INTO sales_order (
			record_type, sales_order_status, sales_order_customer_id,
			sales_order_po_number, sales_order_reference_number, sales_order_memo, sales_order_notes, sales_order_internal_notes, sales_order_terms_conditions,
			sales_order_sales_rep_id, sales_order_owner_id, sales_order_sales_tax_percent,
			sales_order_payment_terms, sales_order_price_level, sales_order_currency,
			sales_order_shipping_charge, sales_order_adjustment, sales_order_ship_same_as_bill,
			sales_order_subtotal, sales_order_discount_total, sales_order_tax_total, sales_order_grand_total,
			sales_order_bill_customer_name, sales_order_bill_attention, sales_order_bill_addr_line1, sales_order_bill_addr_line2, sales_order_bill_addr_suitenum,
			sales_order_bill_addr_city, sales_order_bill_addr_state, sales_order_bill_addr_zip, sales_order_bill_addr_country,
			sales_order_bill_phone, sales_order_bill_fax, sales_order_bill_email,
			sales_order_ship_customer_name, sales_order_ship_attention, sales_order_ship_addr_line1, sales_order_ship_addr_line2, sales_order_ship_addr_suitenum,
			sales_order_ship_addr_city, sales_order_ship_addr_state, sales_order_ship_addr_zip, sales_order_ship_addr_country,
			sales_order_ship_phone, sales_order_ship_fax, sales_order_ship_email,
			sales_order_created_by
		) VALUES (
			$1,$2,$3,
			$4,$5,$6,$7,$8,$9,
			$10,$11,$12,
			$13,$14,$15,
			$16,$17,$18,
			$19,$20,$21,$22,
			$23,$24,$25,$26,$27,
			$28,$29,$30,$31,
			$32,$33,$34,
			$35,$36,$37,$38,$39,
			$40,$41,$42,$43,
			$44,$45,$46,
			$47
		) RETURNING sales_order_id, sales_order_uuid`,
		sordRecordTypeID, draftStatusID, custInternalID,
		poNumber, refNumber, memo, notes, internalNotes, terms,
		salesRepID, nullableInt(ownerEmployeeID), salesTaxPercent,
		paymentTermsID, priceLevelID, currencyID,
		shippingCharge, adjustment, shipSameAsBill,
		header.Subtotal, header.DiscountTotal, header.TaxTotal, header.GrandTotal,
		billName, billAttn, billL1, billL2, billSuite,
		billCity, billState, billZip, billCountry,
		billPhone, billFax, billEmail,
		shipName, shipAttn, shipL1, shipL2, shipSuite,
		shipCity, shipState, shipZip, shipCountry,
		shipPhone, shipFax, shipEmail,
		nullableInt(actorEmployeeID),
	).Scan(&soInternalID, &soUUID)
	if err != nil {
		return nil, fmt.Errorf("insert sales order from quote conversion: %w", err)
	}

	soNumber := salesorder.FormatNumber(int64(soInternalID))
	if _, err := tx.Exec(ctx, `UPDATE sales_order SET sales_order_number = $1 WHERE sales_order_id = $2`, soNumber, soInternalID); err != nil {
		return nil, fmt.Errorf("set sales order number: %w", err)
	}

	lineMapping := make([]map[string]any, 0, len(lines))
	for i, l := range lines {
		var invItemID any
		if l.hasInvItem {
			invItemID = l.invItemID
		}
		var newItemID int
		err := tx.QueryRow(ctx, `
			INSERT INTO sales_order_item (
				sales_order_id, line_number, inventory_item_id,
				item_name, sku, description, unit_id, unit_code,
				quantity, unit_price, discount_percent, tax_rate_id, tax_percent,
				line_subtotal, line_discount, line_tax, line_total,
				item_created_by
			) VALUES ($1,$2,$3, $4,$5,$6,$7,$8, $9,$10,$11,$12,$13, $14,$15,$16,$17, $18)
			RETURNING sales_order_item_id`,
			soInternalID, i+1, invItemID,
			l.name, l.sku, l.desc, l.unitID, l.unitCode,
			l.quantity, l.unitPrice, l.discountPercent, l.taxRateID, l.taxPercent,
			lineMoney[i].Subtotal, lineMoney[i].Discount, lineMoney[i].Tax, lineMoney[i].Total,
			nullableInt(actorEmployeeID),
		).Scan(&newItemID)
		if err != nil {
			return nil, fmt.Errorf("insert sales order item from quote conversion: %w", err)
		}
		lineMapping = append(lineMapping, map[string]any{"quoteItemId": l.itemID, "salesOrderItemId": newItemID})
	}

	snapshotJSON, err := jsonMarshalLineMapping(lineMapping)
	if err != nil {
		return nil, fmt.Errorf("encode conversion line mapping: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO quote_conversion (quote_id, sales_order_id, converted_by, snapshot)
		VALUES ($1, $2, $3, $4)`,
		quoteInternalID, soInternalID, nullableInt(actorEmployeeID), snapshotJSON); err != nil {
		return nil, fmt.Errorf("record quote conversion: %w", err)
	}

	// quotRecordTypeID was already resolved above (as sordRecordTypeID is
	// SORD's id, not QUOT's — CONV only exists in QUOT's status set).
	if statusCode != "CONV" {
		var convStatusID int
		if err := tx.QueryRow(ctx, `
			SELECT record_status_id FROM lkp_record_status
			WHERE record_status_record_type = $1 AND record_status_code = 'CONV'`, quotRecordTypeID).Scan(&convStatusID); err != nil {
			return nil, fmt.Errorf("resolve QUOT CONV status: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE quote SET
				quote_status = $2, quote_updated_at = NOW(), quote_updated_by = $3,
				quote_record_version = quote_record_version + 1
			WHERE quote_id = $1`, quoteInternalID, convStatusID, nullableInt(actorEmployeeID)); err != nil {
			return nil, fmt.Errorf("set quote status to CONV: %w", err)
		}
		writeHistory(ctx, tx, quoteInternalID, "convert", &curStatusID, &convStatusID, actorEmployeeID)
	} else {
		writeHistory(ctx, tx, quoteInternalID, "convert", &curStatusID, &curStatusID, actorEmployeeID)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit convert quote to sales order: %w", err)
	}
	return salesorder.Get(ctx, pool, soUUID)
}
```

This requires one earlier change: resolve `quotRecordTypeID` once, right alongside `sordRecordTypeID` near the top of the function (both are needed — `sordRecordTypeID` for the new Sales Order's `record_type`/`DRFT` status, `quotRecordTypeID` for looking up Quote's own `CONV` status later). Update that earlier block from:

```go
	var sordRecordTypeID, draftStatusID int
	if err := tx.QueryRow(ctx, `SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'SORD'`).Scan(&sordRecordTypeID); err != nil {
		return nil, fmt.Errorf("resolve SORD record type: %w", err)
	}
	if err := tx.QueryRow(ctx, `
		SELECT record_status_id FROM lkp_record_status
		WHERE record_status_record_type = $1 AND record_status_code = 'DRFT'`, sordRecordTypeID).Scan(&draftStatusID); err != nil {
		return nil, fmt.Errorf("resolve SORD DRFT status: %w", err)
	}
```

to:

```go
	var sordRecordTypeID, quotRecordTypeID, draftStatusID int
	if err := tx.QueryRow(ctx, `SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'SORD'`).Scan(&sordRecordTypeID); err != nil {
		return nil, fmt.Errorf("resolve SORD record type: %w", err)
	}
	if err := tx.QueryRow(ctx, `SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'QUOT'`).Scan(&quotRecordTypeID); err != nil {
		return nil, fmt.Errorf("resolve QUOT record type: %w", err)
	}
	if err := tx.QueryRow(ctx, `
		SELECT record_status_id FROM lkp_record_status
		WHERE record_status_record_type = $1 AND record_status_code = 'DRFT'`, sordRecordTypeID).Scan(&draftStatusID); err != nil {
		return nil, fmt.Errorf("resolve SORD DRFT status: %w", err)
	}
```

- [ ] **Step 4: Add the small JSON-encoding helper this file uses**

```go
// quote/convert_util.go
package quote

import "encoding/json"

// jsonMarshalLineMapping encodes the quote-item -> sales-order-item line
// mapping for quote_conversion.snapshot (spec §9.2).
func jsonMarshalLineMapping(m []map[string]any) ([]byte, error) {
	return json.Marshal(map[string]any{"lineMapping": m})
}
```

- [ ] **Step 5: Add the `ConvertToSalesOrder` handler to `controllers/quote.go`**

```go
// ConvertToSalesOrder POST /api/tenant/quotes/{uuid}/convert-to-sales-order
// Requires quote:read (+ IDOR) on the source quote and sales_order:create
// (spec §9.2, §10).
func (h *QuoteOps) ConvertToSalesOrder(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, _, ok := h.authQuoteByUUID(w, r, uuid, authz.ActionRead)
	if !ok {
		return
	}
	decision, err := authz.Check(r.Context(), pool, identityID, authz.ResourceSalesOrder, authz.ActionCreate)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return
	}
	if !decision.Allowed {
		fail(w, http.StatusForbidden, "You do not have permission to create sales orders.")
		return
	}
	so, err := quote.ConvertToSalesOrder(r.Context(), pool, uuid, resolveEmployeeID(r, identityID))
	if err != nil {
		quoteFail(w, err, "Failed to convert quote to sales order.")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "salesOrder": so})
}
```

- [ ] **Step 6: Run tests to verify they pass**

Run: `TEST_DATABASE_URL=<your test db dsn> go test ./quote/... -tags dbtest -run TestConvertToSalesOrder -v`
Expected: PASS (including the "second conversion succeeds and produces a distinct sales order" assertion), or `SKIP` if no test DB — in that case run `go build -tags dbtest ./quote/... ./controllers/...` and `go vet -tags dbtest ./quote/...`, all exit 0.

- [ ] **Step 7: Commit**

```bash
git add quote/convert.go quote/convert_util.go quote/convert_test.go controllers/quote.go
git commit -m "feat(quote): add ConvertToSalesOrder conversion service and endpoint"
```

---

### Task 17: `main.go` — route registration (Quote routes + both conversion routes)

**Files:**
- Modify: `main.go` (add two blocks: the Quote route block right after Plan A's Estimate block, and — since the convert-to-quote route belongs to the already-registered `est` handler group — one additional line inserted into that existing Estimate block)

**Interfaces:**
- Consumes: `controllers.NewQuoteOps()`, `tenantChain`, the existing `est` variable from Plan A's Task 15 registration block.

- [ ] **Step 1: Add the Estimate→Quote convert route to the existing Estimate block**

In the Estimate route block Plan A's Task 15 added, insert one line after the `approve` route and before the `audit` route:

```go
est := controllers.NewEstimateOps()
mux.Handle("GET /api/tenant/estimates", tenantChain(est.List))
mux.Handle("POST /api/tenant/estimates/search", tenantChain(est.Search))
mux.Handle("POST /api/tenant/estimates", tenantChain(est.Create))
mux.Handle("GET /api/tenant/estimates/{uuid}", tenantChain(est.Get))
mux.Handle("PATCH /api/tenant/estimates/{uuid}", tenantChain(est.Update))
mux.Handle("DELETE /api/tenant/estimates/{uuid}", tenantChain(est.Delete))
mux.Handle("POST /api/tenant/estimates/{uuid}/transition", tenantChain(est.Transition))
mux.Handle("POST /api/tenant/estimates/{uuid}/approve", tenantChain(est.Approve))
mux.Handle("POST /api/tenant/estimates/{uuid}/convert-to-quote", tenantChain(est.ConvertToQuote))
mux.Handle("GET /api/tenant/estimates/{uuid}/audit", tenantChain(est.Audit))
```

- [ ] **Step 2: Add the Quote route block**

Insert immediately after the (now-updated) Estimate block:

```go
// Quote: dedicated v2 relational module (header + line items + approval),
// a sibling of Estimate/Sales Order/Invoice, with lineage back to its
// source Estimate (when converted from one) and forward to any Sales
// Order(s) it has been converted into.
quo := controllers.NewQuoteOps()
mux.Handle("GET /api/tenant/quotes", tenantChain(quo.List))
mux.Handle("POST /api/tenant/quotes/search", tenantChain(quo.Search))
mux.Handle("POST /api/tenant/quotes", tenantChain(quo.Create))
mux.Handle("GET /api/tenant/quotes/{uuid}", tenantChain(quo.Get))
mux.Handle("PATCH /api/tenant/quotes/{uuid}", tenantChain(quo.Update))
mux.Handle("DELETE /api/tenant/quotes/{uuid}", tenantChain(quo.Delete))
mux.Handle("POST /api/tenant/quotes/{uuid}/transition", tenantChain(quo.Transition))
mux.Handle("POST /api/tenant/quotes/{uuid}/approve", tenantChain(quo.Approve))
mux.Handle("POST /api/tenant/quotes/{uuid}/convert-to-sales-order", tenantChain(quo.ConvertToSalesOrder))
mux.Handle("GET /api/tenant/quotes/{uuid}/audit", tenantChain(quo.Audit))
```

- [ ] **Step 3: Verify the full binary builds**

Run: `go build ./...`
Expected: exits 0.

- [ ] **Step 4: Commit**

```bash
git add main.go
git commit -m "feat(quote): register quote routes and both conversion endpoints"
```

---

### Task 18: Full verification pass

**Files:** none (verification only)

- [ ] **Step 1: Build the whole repo**

Run: `go build ./...`
Expected: exits 0, no output.

- [ ] **Step 2: Vet the whole repo**

Run: `go vet ./...`
Expected: exits 0, no output. (This specifically catches the kind of dead-code/unused-variable issue caught and fixed during Task 16's drafting — confirm none remain.)

- [ ] **Step 3: Run the pure-logic test suite** (no DB required, both packages)

Run: `go test ./estimate/... ./quote/... -v`
Expected: PASS — every `TestComputeLine`, `TestComputeHeader`, `TestFormatNumber`, `TestCanTransition`, `TestValidateTransition`, `TestResolver_*` test in both packages passes. (`dbtest`-tagged tests are excluded by default.)

- [ ] **Step 4: Run the full repo test suite**

Run: `go test ./...`
Expected: exits 0 (`ok` or `no test files` for every package — no `FAIL` lines).

- [ ] **Step 5: Run the DB-backed test suite, if a test database is available**

Run: `TEST_DATABASE_URL=<your test db dsn> go test ./estimate/... ./quote/... -tags dbtest -v`
Expected: PASS for every test in both packages, including `TestConvertToQuote_*` (estimate) and `TestConvertToSalesOrder_*` (quote) — the latter specifically covering the "second conversion from an already-`CONV` quote still succeeds and produces a distinct Sales Order" business rule from spec AD-6.

- [ ] **Step 6: Manually exercise the full pipeline once, end-to-end** (smoke test beyond unit/integration tests — confirms the two conversions chain correctly through real HTTP)

With a running server and a valid auth token:
1. `POST /api/tenant/estimates` with one line → note the returned `id`.
2. `POST /api/tenant/estimates/{id}/transition` `{"toStatusCode":"PAPV"}`, then `{"toStatusCode":"APPV"}`.
3. `POST /api/tenant/estimates/{id}/convert-to-quote` → note the returned quote's `id`; confirm `quote.estimate.id` matches the source estimate's `id`, and `quote.items[0].unitPrice` matches the estimate's line unit price exactly.
4. `POST /api/tenant/quotes/{quoteId}/transition` `{"toStatusCode":"PAPV"}`, then `{"toStatusCode":"APPV"}`.
5. `POST /api/tenant/quotes/{quoteId}/convert-to-sales-order` → confirm `salesOrder.status` is `"Draft"` and `salesOrder.items[0].quantity` matches the quote's line quantity exactly.
6. `GET /api/tenant/quotes/{quoteId}` → confirm `status`/`statusCode` is now `"Converted"`/`"CONV"`.
7. Repeat step 5 once more on the same `quoteId` → confirm it succeeds again (201, not 409) and returns a **different** `salesOrder.id` than step 5's result.

Expected: every step returns the documented status code from spec §10 with no unexpected 500s, and the lineage/status assertions in steps 3, 5, 6, 7 all hold.

- [ ] **Step 7: Hand off to the requested reviews** (run outside this plan, per the original task's "Reviews" section)

Run, in order, against the combined diff of both Plan A and Plan B: migration-auditor (both schema migrations), tenancy-security-reviewer (RBAC/IDOR across both controllers, plus the two conversion endpoints' dual-resource permission checks), filter-invariant-checker (both resolvers/searches), feature-dev:code-reviewer, code-simplifier. Fix or justify every finding before this module is considered complete.

- [ ] **Step 8: No commit for this task** (verification only)
