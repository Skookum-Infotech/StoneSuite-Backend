# Vendor Bill Module Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Implement the Vendor Bill module (header + lines + AD-6 approval + bill-owned settlement ledger + optional PO lineage + attachments) per `docs/superpowers/specs/2026-08-10-vendor-bill-module-design.md`.

**Architecture:** A new `vendorbill/` Go package (relational store, invoice-shaped balance/status machine, purchaseorder-shaped vendor/approval conventions) + 3 controller files + 7 new tables appended to `tenant/schema.sql` + 3 shared-file edits (`main.go`, `workflow/attachments.go`; `authz/catalog.go` needs no edit — already seeded).

**Tech Stack:** Go, pgx/v5, PostgreSQL (Neon), the `query/` filter engine, stdlib `testing` for pure functions.

## Global Constraints

- Package `vendorbill/`; controllers `controllers/vendorbill*.go` + `controllers/purchaseorder_convert.go`.
- Every mutating handler enforces `authz.ResourceVendorBill` + action via `authVB`/`authVBByUUID`; scope denial is **404, not 403**; `permission_denied`/`idor_denied` logged via `logSecurityEvent` — copy the skeleton from `controllers/purchaseorder.go`, never quote/estimate.
- `vendor_bill_custom_fields` is `NOT NULL DEFAULT '{}'` — every write path nil-guards: `if custom == nil { custom = map[string]any{} }`.
- All SQL is parameterized (`$n`); filтеррing goes only through `query.FieldResolver` (unresolved key → `*query.InvalidFilterError` → 400, never 500).
- Pagination is keyset only (`query.Build`/`query.NextCursor`), capped at `query.MaxLimit`.
- Money is always `round2`-ed (`math.Round(x*100)/100`); every stored total is 2dp.
- Record type code is `"VBIL"`, statuses `DRFT/PAPV/APPV/PART/PAID/ODUE/VOID` — **already seeded** (`lkp_record_type` id 15, `lkp_record_status` rows at `tenant/schema.sql:749`). No seed stanzas are added by this plan.
- `authz.ResourceVendorBill` and its 5 catalog rows **already exist** (`authz/catalog.go:50,309-313`). No catalog edit in this plan.
- This codebase's established convention (confirmed in every sibling module: `purchaseorder/`, `requisition/`, `invoice/`) is stdlib table-driven TDD for **pure** files (`calc.go`, `numbering.go`, `transitions.go`, `resolver.go`, `balance.go`'s pure helpers) and a single consolidated `//go:build dbtest` file for the DB-backed store layer, verified via `go build ./... && go vet ./...` per task and a real Postgres pass at the end (Task 15) — not per-function red/green against a live database. Tasks below follow that split explicitly.
- File cap is 300 lines; every file below is written pre-split (mirrors `purchaseorder/`'s file-per-verb layout from day one).

---

## Task 1: Database Schema — 7 new tables

**Files:**
- Modify: `database/migrations/tenant/schema.sql` (append at end of file)

**Interfaces:**
- Produces: tables `vendor_bill`, `vendor_bill_item`, `vendor_bill_history`, `vendor_bill_approver`, `vendor_bill_approval`, `vendor_bill_payment`, `vendor_bill_conversion`, and every index/constraint named below — every later task's SQL depends on these exact column names.

- [ ] **Step 1: Append the VENDOR BILL MODULE section**

Add at the very end of `database/migrations/tenant/schema.sql`:

```sql

-- =====================================================================
-- VENDOR BILL MODULE
-- Spec: docs/superpowers/specs/2026-08-10-vendor-bill-module-design.md
-- Reuses (already seeded, do not recreate): lkp_record_type VBIL (id 15),
-- lkp_record_status rows for record_type=15 (DRFT/PAPV/APPV/PART/PAID/ODUE/VOID),
-- authz.ResourceVendorBill, the 'vendor_bill' JSONB workflow (custom-field
-- definition host), vendor, purchase_order, purchase_order_item,
-- inventory_item, lkp_unit, lkp_tax_rate, lkp_payment_terms, lkp_currency,
-- lkp_payment_method. Adds zero seed stanzas. Zero changes to any existing
-- table.
-- =====================================================================

-- vendor_bill (header) -- the AP mirror of invoice: what a vendor has billed
-- us, approved (AD-6), and settled via its own payment ledger (AD-7). Vendor
-- is fixed at creation (AD-2); Purchase Order link is optional (AD-4); no
-- address block -- an inbound document is never rendered and mailed (AD-13).
CREATE TABLE IF NOT EXISTS vendor_bill (
    vendor_bill_id                SERIAL        PRIMARY KEY,
    vendor_bill_uuid              UUID          NOT NULL DEFAULT gen_random_uuid(),
    ss_customer_id                 INTEGER          NULL,  -- platform owner stamp, no cross-DB FK
    vendor_bill_number             VARCHAR(20)      NULL,  -- 'VBIL-000001', generated post-insert in Go

    record_type                    INTEGER       NOT NULL REFERENCES lkp_record_type(record_type_id),   -- = VBIL
    vendor_bill_status              INTEGER       NOT NULL REFERENCES lkp_record_status(record_status_id),

    -- Approval (AD-6, mirrors purchase_order_approval_status)
    vendor_bill_approval_status     VARCHAR(10)  NOT NULL DEFAULT 'none',
    vendor_bill_approved_by         INTEGER          NULL REFERENCES employee(employee_id),

    -- Counterparty (AD-2: fixed at creation, name snapshotted)
    vendor_bill_vendor_id           INTEGER       NOT NULL REFERENCES vendor(vendor_id),
    vendor_bill_vendor_name         VARCHAR(150)  NOT NULL DEFAULT '',

    -- Optional PO lineage (AD-4, AD-8) -- set only via the convert endpoint,
    -- never by manual Create/Update input.
    vendor_bill_purchase_order_id   INTEGER          NULL REFERENCES purchase_order(purchase_order_id) ON DELETE SET NULL,

    -- Primary info
    vendor_bill_vendor_invoice_number VARCHAR(50) NOT NULL DEFAULT '',  -- the vendor's own bill/invoice # (not globally unique)
    vendor_bill_reference_number    VARCHAR(50)  NOT NULL DEFAULT '',
    vendor_bill_date                DATE         NOT NULL DEFAULT CURRENT_DATE,
    vendor_bill_due_date            DATE             NULL,
    vendor_bill_sales_tax_percent   DECIMAL(6,4) NOT NULL DEFAULT 0,
    vendor_bill_memo                TEXT         NOT NULL DEFAULT '',
    vendor_bill_notes               TEXT         NOT NULL DEFAULT '',
    vendor_bill_internal_notes      TEXT         NOT NULL DEFAULT '',
    vendor_bill_terms_conditions    TEXT         NOT NULL DEFAULT '',

    -- Assignment (IDOR scope owner)
    vendor_bill_owner_id            INTEGER          NULL REFERENCES employee(employee_id),

    -- Terms / currency
    vendor_bill_payment_terms       INTEGER          NULL REFERENCES lkp_payment_terms(payment_terms_id),
    vendor_bill_currency            INTEGER          NULL REFERENCES lkp_currency(currency_id),
    vendor_bill_exchange_rate       DECIMAL(18,6) NOT NULL DEFAULT 1,

    -- Money summary (stored)
    vendor_bill_subtotal            DECIMAL(15,2) NOT NULL DEFAULT 0,
    vendor_bill_discount_total      DECIMAL(15,2) NOT NULL DEFAULT 0,
    vendor_bill_tax_total           DECIMAL(15,2) NOT NULL DEFAULT 0,
    vendor_bill_adjustment          DECIMAL(15,2) NOT NULL DEFAULT 0,
    vendor_bill_grand_total         DECIMAL(15,2) NOT NULL DEFAULT 0,

    -- AP balance (stored, sole writer is vendorbill.RecomputeBalance -- AD-7)
    vendor_bill_amount_paid         DECIMAL(15,2) NOT NULL DEFAULT 0,
    vendor_bill_balance_due         DECIMAL(15,2) NOT NULL DEFAULT 0,

    -- Dynamic + audit
    vendor_bill_custom_fields       JSONB        NOT NULL DEFAULT '{}',
    vendor_bill_created_at          TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    vendor_bill_created_by          INTEGER          NULL REFERENCES employee(employee_id),
    vendor_bill_updated_at          TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    vendor_bill_updated_by          INTEGER          NULL REFERENCES employee(employee_id),
    vendor_bill_deleted_at          TIMESTAMP        NULL,
    vendor_bill_deleted_by          INTEGER          NULL REFERENCES employee(employee_id),
    vendor_bill_record_version      INTEGER      NOT NULL DEFAULT 1,

    CONSTRAINT uq_vendor_bill_uuid     UNIQUE (vendor_bill_uuid),
    CONSTRAINT uq_vendor_bill_number   UNIQUE (vendor_bill_number),
    CONSTRAINT chk_vbil_approval_status CHECK (vendor_bill_approval_status IN ('none','pending','approved')),
    CONSTRAINT chk_vbil_tax_percent    CHECK (vendor_bill_sales_tax_percent >= 0 AND vendor_bill_sales_tax_percent <= 100),
    CONSTRAINT chk_vbil_totals_nonneg  CHECK (vendor_bill_subtotal >= 0 AND vendor_bill_grand_total >= 0),
    CONSTRAINT chk_vbil_paid_nonneg    CHECK (vendor_bill_amount_paid >= 0 AND vendor_bill_balance_due >= 0),
    CONSTRAINT chk_vbil_soft_delete    CHECK (
        (vendor_bill_deleted_at IS NULL AND vendor_bill_deleted_by IS NULL) OR
        (vendor_bill_deleted_at IS NOT NULL AND vendor_bill_deleted_by IS NOT NULL)
    )
);

-- vendor_bill_item (lines) -- mirrors invoice_item; purchase_order_item_id is
-- set only by the convert path (AD-8), never by manual line input.
CREATE TABLE IF NOT EXISTS vendor_bill_item (
    vendor_bill_item_id       SERIAL        PRIMARY KEY,
    vendor_bill_item_uuid     UUID          NOT NULL DEFAULT gen_random_uuid(),
    vendor_bill_id            INTEGER       NOT NULL REFERENCES vendor_bill(vendor_bill_id) ON DELETE CASCADE,
    line_number               INTEGER       NOT NULL,
    inventory_item_id         INTEGER           NULL REFERENCES inventory_item(inventory_item_id),
    purchase_order_item_id    INTEGER           NULL REFERENCES purchase_order_item(purchase_order_item_id) ON DELETE SET NULL,

    item_name                 VARCHAR(150)  NOT NULL DEFAULT '',
    sku                       VARCHAR(50)   NOT NULL DEFAULT '',
    description               TEXT          NOT NULL DEFAULT '',
    unit_id                   INTEGER           NULL REFERENCES lkp_unit(unit_id),
    unit_code                 VARCHAR(10)   NOT NULL DEFAULT '',
    quantity                  DECIMAL(14,3) NOT NULL DEFAULT 0,
    unit_price                DECIMAL(15,2) NOT NULL DEFAULT 0,
    discount_percent          DECIMAL(6,4)  NOT NULL DEFAULT 0,
    tax_rate_id                INTEGER          NULL REFERENCES lkp_tax_rate(tax_rate_id),
    tax_percent                DECIMAL(6,4)  NOT NULL DEFAULT 0,

    line_subtotal               DECIMAL(15,2) NOT NULL DEFAULT 0,
    line_discount                DECIMAL(15,2) NOT NULL DEFAULT 0,
    line_tax                     DECIMAL(15,2) NOT NULL DEFAULT 0,
    line_total                   DECIMAL(15,2) NOT NULL DEFAULT 0,

    item_created_at              TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    item_created_by              INTEGER           NULL REFERENCES employee(employee_id),
    item_updated_at              TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    item_deleted_at               TIMESTAMP        NULL,
    item_record_version           INTEGER       NOT NULL DEFAULT 1,

    CONSTRAINT uq_vbi_uuid       UNIQUE (vendor_bill_item_uuid),
    CONSTRAINT chk_vbi_qty       CHECK (quantity >= 0),
    CONSTRAINT chk_vbi_unit_price CHECK (unit_price >= 0),
    CONSTRAINT chk_vbi_discount  CHECK (discount_percent >= 0 AND discount_percent <= 100),
    CONSTRAINT chk_vbi_tax       CHECK (tax_percent >= 0 AND tax_percent <= 100)
);

-- vendor_bill_history -- status/action trail (mirrors purchase_order_history)
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

-- vendor_bill_approver / vendor_bill_approval (AD-6, exact structural copies
-- of purchase_order_approver / purchase_order_approval)
CREATE TABLE IF NOT EXISTS vendor_bill_approver (
    vendor_bill_approver_id   SERIAL      PRIMARY KEY,
    record_type_id             INTEGER     NOT NULL REFERENCES lkp_record_type(record_type_id),      -- = VBIL
    record_status_id           INTEGER     NOT NULL REFERENCES lkp_record_status(record_status_id),  -- e.g. PAPV
    approver_employee_id       INTEGER     NOT NULL REFERENCES employee(employee_id),
    is_active                  BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at                 TIMESTAMP   NOT NULL DEFAULT CURRENT_TIMESTAMP,
    created_by                 INTEGER         NULL REFERENCES employee(employee_id),
    CONSTRAINT uq_vendor_bill_approver UNIQUE (record_type_id, record_status_id, approver_employee_id)
);

CREATE TABLE IF NOT EXISTS vendor_bill_approval (
    vendor_bill_approval_id   SERIAL     PRIMARY KEY,
    vendor_bill_id             INTEGER    NOT NULL REFERENCES vendor_bill(vendor_bill_id) ON DELETE CASCADE,
    record_status_id           INTEGER    NOT NULL REFERENCES lkp_record_status(record_status_id),
    approver_employee_id       INTEGER    NOT NULL REFERENCES employee(employee_id),
    approved_at                 TIMESTAMP  NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT uq_vendor_bill_approval UNIQUE (vendor_bill_id, record_status_id, approver_employee_id)
);

-- vendor_bill_payment (AD-7 settlement ledger) -- the sole source Recompute-
-- Balance sums to derive amount_paid/balance_due/status. Soft delete is the
-- "unapply" (mirrors payment_application's application_deleted_at).
CREATE TABLE IF NOT EXISTS vendor_bill_payment (
    vendor_bill_payment_id    SERIAL        PRIMARY KEY,
    vendor_bill_payment_uuid  UUID          NOT NULL DEFAULT gen_random_uuid(),
    vendor_bill_id             INTEGER       NOT NULL REFERENCES vendor_bill(vendor_bill_id) ON DELETE CASCADE,
    payment_method_id          INTEGER           NULL REFERENCES lkp_payment_method(payment_method_id),
    amount                      DECIMAL(15,2) NOT NULL,
    reference_number             VARCHAR(50)  NOT NULL DEFAULT '',
    memo                         TEXT         NOT NULL DEFAULT '',
    paid_at                      DATE         NOT NULL DEFAULT CURRENT_DATE,
    created_by                   INTEGER          NULL REFERENCES employee(employee_id),
    created_at                   TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    deleted_at                   TIMESTAMP        NULL,
    CONSTRAINT uq_vbp_uuid           UNIQUE (vendor_bill_payment_uuid),
    CONSTRAINT chk_vbp_amount_positive CHECK (amount > 0)
);

-- vendor_bill_conversion (AD-8 lineage) -- UNIQUE on vendor_bill_id ONLY,
-- deliberately NOT on purchase_order_id: a PO may be billed in installments
-- across multiple bills, so every call to ConvertFromPurchaseOrder creates a
-- new bill row rather than short-circuiting on an existing one.
CREATE TABLE IF NOT EXISTS vendor_bill_conversion (
    vendor_bill_conversion_id SERIAL      PRIMARY KEY,
    purchase_order_id          INTEGER     NOT NULL REFERENCES purchase_order(purchase_order_id) ON DELETE CASCADE,
    vendor_bill_id              INTEGER     NOT NULL REFERENCES vendor_bill(vendor_bill_id) ON DELETE CASCADE,
    converted_at                 TIMESTAMP   NOT NULL DEFAULT CURRENT_TIMESTAMP,
    converted_by                 INTEGER         NULL REFERENCES employee(employee_id),
    snapshot                     JSONB       NOT NULL DEFAULT '{}',
    CONSTRAINT uq_vendor_bill_conversion_bill UNIQUE (vendor_bill_id)
);

-- vendor_bill indexes (listing/filtering -- all partial on live rows)
CREATE INDEX IF NOT EXISTS idx_vbil_vendor        ON vendor_bill (vendor_bill_vendor_id)         WHERE vendor_bill_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vbil_po             ON vendor_bill (vendor_bill_purchase_order_id) WHERE vendor_bill_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vbil_status         ON vendor_bill (vendor_bill_status)             WHERE vendor_bill_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vbil_date           ON vendor_bill (vendor_bill_date)               WHERE vendor_bill_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vbil_due_date       ON vendor_bill (vendor_bill_due_date)            WHERE vendor_bill_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vbil_owner          ON vendor_bill (vendor_bill_owner_id)            WHERE vendor_bill_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vbil_created_id     ON vendor_bill (vendor_bill_created_at, vendor_bill_id) WHERE vendor_bill_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vbil_updated_id     ON vendor_bill (vendor_bill_updated_at, vendor_bill_id) WHERE vendor_bill_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vbil_duedate_id     ON vendor_bill (vendor_bill_due_date, vendor_bill_id)   WHERE vendor_bill_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vbil_grandtotal_id  ON vendor_bill (vendor_bill_grand_total, vendor_bill_id) WHERE vendor_bill_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vbil_balance_id     ON vendor_bill (vendor_bill_balance_due, vendor_bill_id) WHERE vendor_bill_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vbil_custom_gin     ON vendor_bill USING GIN (vendor_bill_custom_fields);

CREATE INDEX IF NOT EXISTS idx_vbi_bill   ON vendor_bill_item (vendor_bill_id) WHERE item_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_vbi_item   ON vendor_bill_item (inventory_item_id);
CREATE INDEX IF NOT EXISTS idx_vbi_po_item ON vendor_bill_item (purchase_order_item_id);
CREATE UNIQUE INDEX IF NOT EXISTS uq_vbi_line_active
    ON vendor_bill_item (vendor_bill_id, line_number) WHERE item_deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_vbil_history_bill ON vendor_bill_history (vendor_bill_id);

CREATE INDEX IF NOT EXISTS idx_vendor_bill_approver_lookup
    ON vendor_bill_approver (record_type_id, record_status_id) WHERE is_active;
CREATE INDEX IF NOT EXISTS idx_vendor_bill_approval_bill ON vendor_bill_approval (vendor_bill_id);

CREATE INDEX IF NOT EXISTS idx_vbp_bill ON vendor_bill_payment (vendor_bill_id) WHERE deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_vendor_bill_conversion_po ON vendor_bill_conversion (purchase_order_id);
```

- [ ] **Step 2: Verify idempotency against a real database**

```bash
docker run --rm -d -p 5433:5432 -e POSTGRES_PASSWORD=pg --name ss-dev pgvector/pgvector:pg16
psql "postgres://postgres:pg@localhost:5433/postgres" --single-transaction -v ON_ERROR_STOP=1 -f database/migrations/tenant/schema.sql
psql "postgres://postgres:pg@localhost:5433/postgres" --single-transaction -v ON_ERROR_STOP=1 -f database/migrations/tenant/schema.sql
```

Expected: both runs exit 0 with no errors (the second is a no-op — every `CREATE TABLE IF NOT EXISTS`/`CREATE INDEX IF NOT EXISTS` is idempotent).

- [ ] **Step 3: Commit**

```bash
git add database/migrations/tenant/schema.sql
git commit -m "feat(vendor-bill): add schema — header, lines, history, approval, payment ledger, PO lineage"
```

---

## Task 2: Pure Logic — Types, Money Math, Numbering

**Files:**
- Create: `vendorbill/types.go`
- Create: `vendorbill/calc.go`
- Create: `vendorbill/calc_test.go`
- Create: `vendorbill/numbering.go`
- Create: `vendorbill/numbering_test.go`

**Interfaces:**
- Consumes: nothing (first Go files in the package).
- Produces: `CreateVendorBillInput`, `UpdateVendorBillInput`, `LineInput`, `Line`, `VendorRef`, `PurchaseOrderRef`, `BillPayment`, `VendorBill`, `Page` (types.go); `CalcLineInput`, `LineMoney`, `ComputeLine(CalcLineInput) LineMoney`, `HeaderMoney{Subtotal,DiscountTotal,TaxTotal,GrandTotal,AmountPaid,BalanceDue float64}`, `ComputeHeader(lines []LineMoney, adjustment, amountPaid float64) HeaderMoney`, `round2(float64) float64` (calc.go); `FormatNumber(int64) string` (numbering.go) — every later task calls these exact names.

- [ ] **Step 1: Write `vendorbill/types.go`**

```go
// Package vendorbill implements the relational Vendor Bill module: the
// accounts-payable mirror of invoice/ -- header + lines + AD-6 approval gate
// + a bill-owned settlement ledger + optional Purchase Order lineage. A
// sibling of estimate/quote/salesorder/invoice/purchaseorder, not the
// generic v1 JSONB workflow engine.
// Spec: docs/superpowers/specs/2026-08-10-vendor-bill-module-design.md
package vendorbill

import "time"

// VendorRef is the flattened {id, name, number} for "who billed us" navigation.
type VendorRef struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Number string `json:"number,omitempty"`
}

// PurchaseOrderRef is the flattened {id, number} for lineage navigation (AD-8).
type PurchaseOrderRef struct {
	ID     string `json:"id"`
	Number string `json:"number"`
}

// Line is one vendor_bill_item row: catalog/free-text snapshot + stored money.
type Line struct {
	ID                  string  `json:"id"`
	LineNumber          int     `json:"lineNumber"`
	InventoryItemID     *string `json:"inventoryItemId,omitempty"`
	PurchaseOrderItemID *string `json:"purchaseOrderItemId,omitempty"`
	SKU                 string  `json:"sku"`
	ItemName            string  `json:"itemName"`
	Description         string  `json:"description"`
	UnitID              *int    `json:"unitId,omitempty"`
	UnitCode            string  `json:"unitCode"`
	Quantity            float64 `json:"quantity"`
	UnitPrice           float64 `json:"unitPrice"`
	DiscountPercent     float64 `json:"discountPercent"`
	TaxRateID           *int    `json:"taxRateId,omitempty"`
	TaxPercent          float64 `json:"taxPercent"`
	LineSubtotal        float64 `json:"lineSubtotal"`
	LineDiscount        float64 `json:"lineDiscount"`
	LineTax             float64 `json:"lineTax"`
	LineTotal           float64 `json:"lineTotal"`
}

// LineInput is one line of a create/update request. There is no
// purchaseOrderItemUuid field here on purpose (AD-8): that lineage FK is set
// exclusively by the convert path, never by manual Create/Update input.
type LineInput struct {
	LineNumber        int     `json:"lineNumber"`
	InventoryItemUUID string  `json:"inventoryItemUuid,omitempty"`
	Description       string  `json:"description,omitempty"`
	Quantity          float64 `json:"quantity"`
	UnitPrice         float64 `json:"unitPrice"`
	DiscountPercent   float64 `json:"discountPercent"`
	TaxRateID         *int    `json:"taxRateId,omitempty"`
}

// BillPayment is one live vendor_bill_payment row (AD-7).
type BillPayment struct {
	ID              string    `json:"id"`
	Amount          float64   `json:"amount"`
	MethodID        *int      `json:"methodId,omitempty"`
	MethodName      string    `json:"method,omitempty"`
	ReferenceNumber string    `json:"referenceNumber"`
	Memo            string    `json:"memo"`
	PaidAt          string    `json:"paidAt"`
	CreatedAt       time.Time `json:"createdAt"`
}

// vendorBillFields is the header payload shared by create and update --
// everything except the vendor, which is fixed at creation and never
// changes (AD-2).
type vendorBillFields struct {
	VendorInvoiceNumber string         `json:"vendorInvoiceNumber"`
	ReferenceNumber     string         `json:"referenceNumber"`
	BillDate            string         `json:"billDate"`         // "yyyy-mm-dd"; blank => CURRENT_DATE
	DueDate             string         `json:"dueDate,omitempty"` // "yyyy-mm-dd"
	PaymentTermsID      *int           `json:"paymentTermsId,omitempty"`
	CurrencyID          *int           `json:"currencyId,omitempty"`
	OwnerEmployeeID     *int           `json:"ownerEmployeeId,omitempty"`
	SalesTaxPercent     float64        `json:"salesTaxPercent"`
	Memo                string         `json:"memo"`
	Notes               string         `json:"notes"`
	InternalNotes       string         `json:"internalNotes"`
	TermsConditions     string         `json:"termsConditions"`
	Adjustment          float64        `json:"adjustment"`
	CustomFields        map[string]any `json:"customFields"`
	Items               []LineInput    `json:"items"`
}

// CreateVendorBillInput is the request payload for POST /api/tenant/vendor-bills.
// Notice it doesn't take purchaseOrderUuid (that is only set via the convert endpoint).
type CreateVendorBillInput struct {
	VendorUUID string `json:"vendorUuid"`
	vendorBillFields
}

// UpdateVendorBillInput mirrors CreateVendorBillInput minus the vendor
// (a vendor bill's vendor is fixed after creation -- AD-2).
type UpdateVendorBillInput struct {
	vendorBillFields
}

// VendorBill is the full API response for a vendor bill header (+ lines,
// + payments, when loaded by Get). OwnerUserID backs the controller's IDOR
// scope check and is never serialized.
type VendorBill struct {
	ID     string `json:"id"`
	Number string `json:"vendorBillNumber"`

	StatusCode     string `json:"statusCode"`
	StatusName     string `json:"status"`
	ApprovalStatus string `json:"approvalStatus"` // none | pending | approved

	Vendor        VendorRef         `json:"vendor"`
	PurchaseOrder *PurchaseOrderRef `json:"purchaseOrder,omitempty"` // nullable lineage (AD-8)

	OwnerUserID     string `json:"-"`
	OwnerEmployeeID *int   `json:"ownerEmployeeId,omitempty"`

	VendorInvoiceNumber string `json:"vendorInvoiceNumber"`
	ReferenceNumber     string `json:"referenceNumber"`
	BillDate            string `json:"billDate"`
	DueDate             string `json:"dueDate,omitempty"`

	PaymentTermsID  *int    `json:"paymentTermsId,omitempty"`
	CurrencyID      *int    `json:"currencyId,omitempty"`
	ExchangeRate    float64 `json:"exchangeRate"`
	SalesTaxPercent float64 `json:"salesTaxPercent"`

	Memo            string `json:"memo"`
	Notes           string `json:"notes"`
	InternalNotes   string `json:"internalNotes"`
	TermsConditions string `json:"termsConditions"`

	Subtotal      float64 `json:"subtotal"`
	DiscountTotal float64 `json:"discountTotal"`
	TaxTotal      float64 `json:"taxTotal"`
	Adjustment    float64 `json:"adjustment"`
	GrandTotal    float64 `json:"grandTotal"`

	AmountPaid float64 `json:"amountPaid"`
	BalanceDue float64 `json:"balanceDue"`

	CustomFields map[string]any `json:"customFields"`
	Items        []Line         `json:"items"`
	Payments     []BillPayment  `json:"payments,omitempty"`

	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
	RecordVersion int       `json:"recordVersion"`
}

// Page is one page of vendor bills plus keyset-pagination state.
type Page struct {
	Records    []VendorBill `json:"records"`
	NextCursor string       `json:"nextCursor"`
	HasMore    bool         `json:"hasMore"`
}
```

- [ ] **Step 2: Write `vendorbill/calc.go`**

```go
package vendorbill

import "math"

func round2(x float64) float64 { return math.Round(x*100) / 100 }

// CalcLineInput holds the raw per-line quantities and rates used to compute
// line money.
type CalcLineInput struct {
	Quantity, UnitPrice, DiscountPercent, TaxPercent float64
}

// LineMoney holds a line's computed subtotal, discount, tax, and total (2-dp rounded).
type LineMoney struct{ Subtotal, Discount, Tax, Total float64 }

// ComputeLine derives a line's stored money (AD-9).
func ComputeLine(in CalcLineInput) LineMoney {
	sub := round2(in.Quantity * in.UnitPrice)
	disc := round2(sub * in.DiscountPercent / 100)
	tax := round2((sub - disc) * in.TaxPercent / 100)
	return LineMoney{Subtotal: sub, Discount: disc, Tax: tax, Total: round2(sub - disc + tax)}
}

// HeaderMoney holds a vendor bill's computed totals and balance.
type HeaderMoney struct {
	Subtotal, DiscountTotal, TaxTotal, GrandTotal, AmountPaid, BalanceDue float64
}

// ComputeHeader sums line money and applies the flat adjustment, then
// computes balance due (AD-9). There is no shipping term -- a vendor bill has
// no separate shipping-charge field (AD-13).
func ComputeHeader(lines []LineMoney, adjustment, amountPaid float64) HeaderMoney {
	var h HeaderMoney
	for _, l := range lines {
		h.Subtotal += l.Subtotal
		h.DiscountTotal += l.Discount
		h.TaxTotal += l.Tax
	}
	h.Subtotal = round2(h.Subtotal)
	h.DiscountTotal = round2(h.DiscountTotal)
	h.TaxTotal = round2(h.TaxTotal)
	h.GrandTotal = round2(h.Subtotal - h.DiscountTotal + h.TaxTotal + adjustment)
	h.AmountPaid = round2(amountPaid)
	h.BalanceDue = round2(h.GrandTotal - h.AmountPaid)
	if h.BalanceDue < 0 {
		h.BalanceDue = 0
	}
	return h
}
```

- [ ] **Step 3: Write `vendorbill/calc_test.go`**

```go
package vendorbill

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
			want: LineMoney{Subtotal: 49.97, Discount: 2.5, Tax: 0, Total: 47.47},
		},
		{
			name: "zero quantity",
			in:   CalcLineInput{Quantity: 0, UnitPrice: 99.99, DiscountPercent: 10, TaxPercent: 5},
			want: LineMoney{},
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
		name       string
		lines      []LineMoney
		adjustment float64
		amountPaid float64
		want       HeaderMoney
	}{
		{
			name:  "single line no adjustment",
			lines: []LineMoney{{Subtotal: 100, Discount: 10, Tax: 7.2, Total: 97.2}},
			want:  HeaderMoney{Subtotal: 100, DiscountTotal: 10, TaxTotal: 7.2, GrandTotal: 97.2, BalanceDue: 97.2},
		},
		{
			name: "multiple lines with adjustment",
			lines: []LineMoney{
				{Subtotal: 100, Discount: 10, Tax: 7.2, Total: 97.2},
				{Subtotal: 50, Discount: 0, Tax: 4.13, Total: 54.13},
			},
			adjustment: -5,
			// subtotal=150, discount=10, tax=11.33, grand=150-10+11.33-5=146.33
			want: HeaderMoney{Subtotal: 150, DiscountTotal: 10, TaxTotal: 11.33, GrandTotal: 146.33, BalanceDue: 146.33},
		},
		{
			name:       "partially paid balance",
			lines:      []LineMoney{{Subtotal: 100, Discount: 0, Tax: 0, Total: 100}},
			amountPaid: 40,
			want:       HeaderMoney{Subtotal: 100, GrandTotal: 100, AmountPaid: 40, BalanceDue: 60},
		},
		{
			name:       "balance never goes negative",
			lines:      []LineMoney{{Subtotal: 100, Discount: 0, Tax: 0, Total: 100}},
			amountPaid: 150,
			want:       HeaderMoney{Subtotal: 100, GrandTotal: 100, AmountPaid: 150, BalanceDue: 0},
		},
		{
			name:  "no lines",
			lines: nil,
			want:  HeaderMoney{},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ComputeHeader(tt.lines, tt.adjustment, tt.amountPaid); got != tt.want {
				t.Fatalf("ComputeHeader(%+v, %v, %v) = %+v, want %+v", tt.lines, tt.adjustment, tt.amountPaid, got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 4: Write `vendorbill/numbering.go`**

```go
package vendorbill

import "fmt"

// numberPrefix is the VBIL record-type code (lkp_record_type.record_type_code).
const numberPrefix = "VBIL"

// FormatNumber renders the human-readable document number from the row's
// serial PK, zero-padded to 6 digits (AD-10): VBIL-000001.
func FormatNumber(serialID int64) string {
	return fmt.Sprintf("%s-%06d", numberPrefix, serialID)
}
```

- [ ] **Step 5: Write `vendorbill/numbering_test.go`**

```go
package vendorbill

import "testing"

func TestFormatNumber(t *testing.T) {
	tests := []struct {
		id   int64
		want string
	}{
		{1, "VBIL-000001"},
		{42, "VBIL-000042"},
		{999999, "VBIL-999999"},
		{1000000, "VBIL-1000000"}, // grows past the pad, never truncates
	}
	for _, tt := range tests {
		if got := FormatNumber(tt.id); got != tt.want {
			t.Fatalf("FormatNumber(%d) = %q, want %q", tt.id, got, tt.want)
		}
	}
}
```

- [ ] **Step 6: Run the pure-logic tests**

```bash
go test ./vendorbill/... -run TestComputeLine -v
go test ./vendorbill/... -run TestComputeHeader -v
go test ./vendorbill/... -run TestFormatNumber -v
```

Expected: all PASS (types.go has no test — it is pure data shape, verified by the package compiling).

- [ ] **Step 7: Commit**

```bash
git add vendorbill/types.go vendorbill/calc.go vendorbill/calc_test.go vendorbill/numbering.go vendorbill/numbering_test.go
git commit -m "feat(vendor-bill): add types, money math, numbering"
```

---

## Task 3: Pure Logic — Transitions, Resolver

**Files:**
- Create: `vendorbill/transitions.go`
- Create: `vendorbill/transitions_test.go`
- Create: `vendorbill/resolver.go`
- Create: `vendorbill/resolver_test.go`

**Interfaces:**
- Consumes: nothing new.
- Produces: `ErrInvalidTransition`, `CanTransition(from, to string) bool`, `ValidateTransition(from, to string) error` (transitions.go); `resolver` struct implementing `query.FieldResolver` + `query.SortResolver` + `query.SearchResolver` (resolver.go) — Task 7 (Search), Task 8 (Transition) depend on these.

- [ ] **Step 1: Write `vendorbill/transitions.go`**

```go
package vendorbill

import "errors"

// ErrInvalidTransition is returned when a status change is not permitted.
var ErrInvalidTransition = errors.New("invalid vendor bill status transition")

// allowedTransitions maps a status code to the set of codes reachable from it
// (AD-5) -- invoice's machine minus SENT, since a bill is received rather
// than sent. Terminal states (PAID, VOID) map to an empty set. PART/PAID are
// normally reached by DeriveStatus (balance.go), not this map; they remain
// here so an operator can correct state manually.
var allowedTransitions = map[string]map[string]bool{
	"DRFT": {"PAPV": true, "VOID": true},
	"PAPV": {"APPV": true, "DRFT": true, "VOID": true},
	"APPV": {"PART": true, "PAID": true, "ODUE": true, "VOID": true},
	"PART": {"PAID": true, "ODUE": true, "VOID": true},
	"ODUE": {"PART": true, "PAID": true, "VOID": true},
	"PAID": {},
	"VOID": {},
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

- [ ] **Step 2: Write `vendorbill/transitions_test.go`**

```go
package vendorbill

import "testing"

func TestCanTransition(t *testing.T) {
	tests := []struct {
		from, to string
		want     bool
	}{
		{"DRFT", "PAPV", true},
		{"DRFT", "VOID", true},
		{"DRFT", "APPV", false}, // must pass through PAPV
		{"PAPV", "APPV", true},
		{"PAPV", "DRFT", true}, // recall
		{"APPV", "PART", true},
		{"APPV", "PAID", true},
		{"APPV", "ODUE", true},
		{"APPV", "DRFT", false}, // no recall once approved
		{"PART", "PAID", true},
		{"PART", "ODUE", true},
		{"ODUE", "PART", true},
		{"ODUE", "PAID", true},
		{"PAID", "VOID", false}, // terminal
		{"VOID", "DRFT", false}, // terminal
	}
	for _, tt := range tests {
		if got := CanTransition(tt.from, tt.to); got != tt.want {
			t.Errorf("CanTransition(%q, %q) = %v, want %v", tt.from, tt.to, got, tt.want)
		}
	}
}

func TestValidateTransition(t *testing.T) {
	if err := ValidateTransition("DRFT", "PAPV"); err != nil {
		t.Errorf("ValidateTransition(DRFT, PAPV) = %v, want nil", err)
	}
	if err := ValidateTransition("PAID", "VOID"); err != ErrInvalidTransition {
		t.Errorf("ValidateTransition(PAID, VOID) = %v, want ErrInvalidTransition", err)
	}
}
```

- [ ] **Step 3: Write `vendorbill/resolver.go`**

```go
package vendorbill

import (
	"regexp"
	"strings"

	"stonesuite-backend/query"
)

var validCustomKey = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

// resolver implements query.FieldResolver + query.SortResolver +
// query.SearchResolver for vendor_bill. Table alias "vb" = vendor_bill.
type resolver struct{}

type resolved struct {
	expr string
	dt   query.DataType
}

var systemFields = map[string]resolved{
	"id":                    {"vb.vendor_bill_uuid::text", query.TypeString},
	"document_number":       {"COALESCE(vb.vendor_bill_number,'')", query.TypeString},
	"record_number":         {"COALESCE(vb.vendor_bill_number,'')", query.TypeString},
	"vendor_id":             {"vb.vendor_bill_vendor_id::text", query.TypeString},
	"purchase_order_id":     {"vb.vendor_bill_purchase_order_id::text", query.TypeString},
	"status":                {"vb.vendor_bill_status::text", query.TypeString},
	"owner_id":              {"vb.vendor_bill_owner_id::text", query.TypeString},
	"vendor_invoice_number": {"vb.vendor_bill_vendor_invoice_number", query.TypeString},
	"bill_date":             {"vb.vendor_bill_date", query.TypeDate},
	"due_date":              {"vb.vendor_bill_due_date", query.TypeDate},
	"currency_id":           {"vb.vendor_bill_currency::text", query.TypeString},
	"payment_terms_id":      {"vb.vendor_bill_payment_terms::text", query.TypeString},
	"grand_total":           {"vb.vendor_bill_grand_total", query.TypeNumber},
	"amount_paid":           {"vb.vendor_bill_amount_paid", query.TypeNumber},
	"balance_due":           {"vb.vendor_bill_balance_due", query.TypeNumber},
	"created_by":            {"vb.vendor_bill_created_by::text", query.TypeString},
	"updated_by":            {"vb.vendor_bill_updated_by::text", query.TypeString},
	"created_at":            {"vb.vendor_bill_created_at", query.TypeDate},
	"updated_at":            {"vb.vendor_bill_updated_at", query.TypeDate},
}

func (resolver) Resolve(key string) (string, query.DataType, bool) {
	if s, ok := systemFields[key]; ok {
		return s.expr, s.dt, true
	}
	if k, ok := strings.CutPrefix(key, "cf:"); ok && validCustomKey.MatchString(k) {
		return "vb.vendor_bill_custom_fields->>'" + k + "'", query.TypeString, true
	}
	return "", "", false
}

var _ query.FieldResolver = resolver{}

// sortFields is the stable sort whitelist. due_date is deliberately excluded
// (nullable columns break keyset comparison) -- it stays filterable via
// systemFields, just not sortable, mirroring invoice.sortFields.
var sortFields = map[string]resolved{
	"document_number": {"COALESCE(vb.vendor_bill_number,'')", query.TypeString},
	"record_number":   {"COALESCE(vb.vendor_bill_number,'')", query.TypeString},
	"bill_date":       {"vb.vendor_bill_date", query.TypeDate},
	"grand_total":     {"vb.vendor_bill_grand_total", query.TypeNumber},
	"balance_due":     {"vb.vendor_bill_balance_due", query.TypeNumber},
	"status":          {"vb.vendor_bill_status", query.TypeNumber},
	"vendor_id":       {"vb.vendor_bill_vendor_id", query.TypeNumber},
}

func (resolver) SortExpr(key string) (string, query.DataType, bool) {
	if s, ok := sortFields[key]; ok {
		return s.expr, s.dt, true
	}
	return "", "", false
}

var _ query.SortResolver = resolver{}

func (resolver) SearchPredicate(ph string) string {
	return "(" +
		"vb.vendor_bill_number ILIKE '%'||" + ph + "||'%'" +
		" OR vb.vendor_bill_vendor_invoice_number ILIKE '%'||" + ph + "||'%'" +
		" OR vb.vendor_bill_memo ILIKE '%'||" + ph + "||'%'" +
		" OR vb.vendor_bill_notes ILIKE '%'||" + ph + "||'%'" +
		" OR vb.vendor_bill_vendor_name ILIKE '%'||" + ph + "||'%'" +
		" OR EXISTS (SELECT 1 FROM vendor_bill_item vbi WHERE vbi.vendor_bill_id = vb.vendor_bill_id" +
		"   AND (vbi.sku ILIKE '%'||" + ph + "||'%' OR vbi.item_name ILIKE '%'||" + ph + "||'%'))" +
		" OR EXISTS (SELECT 1 FROM vendor v WHERE v.vendor_id = vb.vendor_bill_vendor_id" +
		"   AND (v.vendor_legal_name ILIKE '%'||" + ph + "||'%' OR v.vendor_given_name ILIKE '%'||" + ph + "||'%' OR v.vendor_family_name ILIKE '%'||" + ph + "||'%'))" +
		")"
}

var _ query.SearchResolver = resolver{}
```

- [ ] **Step 4: Write `vendorbill/resolver_test.go`**

```go
package vendorbill

import (
	"strings"
	"testing"

	"stonesuite-backend/query"
)

func TestResolverResolve(t *testing.T) {
	r := resolver{}
	tests := []struct {
		key     string
		wantOK  bool
		wantDT  query.DataType
	}{
		{"id", true, query.TypeString},
		{"grand_total", true, query.TypeNumber},
		{"balance_due", true, query.TypeNumber},
		{"bill_date", true, query.TypeDate},
		{"cf:priority", true, query.TypeString},
		{"cf:INVALID KEY", false, ""},
		{"not_a_real_field", false, ""},
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

func TestResolverSortExpr(t *testing.T) {
	r := resolver{}
	if _, _, ok := r.SortExpr("grand_total"); !ok {
		t.Error("SortExpr(grand_total) should be sortable")
	}
	if _, _, ok := r.SortExpr("due_date"); ok {
		t.Error("SortExpr(due_date) should NOT be sortable -- nullable column breaks keyset pagination")
	}
}

func TestResolverSearchPredicate(t *testing.T) {
	r := resolver{}
	frag := r.SearchPredicate("$3")
	if !strings.Contains(frag, "$3") {
		t.Errorf("SearchPredicate must reference the given placeholder, got: %s", frag)
	}
	if !strings.Contains(frag, "vendor_bill_vendor_invoice_number") {
		t.Error("SearchPredicate must search the vendor's own invoice number")
	}
}
```

- [ ] **Step 5: Run pure-logic tests**

```bash
go test ./vendorbill/... -run "TestCanTransition|TestValidateTransition|TestResolver" -v
```

Expected: all PASS.

- [ ] **Step 6: Commit**

```bash
git add vendorbill/transitions.go vendorbill/transitions_test.go vendorbill/resolver.go vendorbill/resolver_test.go
git commit -m "feat(vendor-bill): add status transitions and query resolver"
```

---

## Task 4: Store Foundation — Shared Helpers + Get

**Files:**
- Create: `vendorbill/store.go`
- Create: `vendorbill/store_get.go`

**Interfaces:**
- Consumes: `ComputeLine`, `LineMoney` (calc.go); `resolver` (resolver.go, indirectly via Search in Task 7).
- Produces: `ErrNotFound`, `ClientError{Msg string}`, `IsClientError(error) bool`, `nullableInt(int) any`, `actorOrSystem(int) int`, `nullableDate(string) any`, `orNow(string) string`, `isForeignKeyViolation/isUniqueViolation(error) bool`, `colVal{col string; val any; cast string}`, `buildInsert(table string, cv []colVal, returning string) (string, []any)`, `buildUpdateSet(table string, leadingArgs []any, cv []colVal, extraSets []string, where string) (string, []any)`, `recordTypeIDByCode`, `statusIDByCode`, `vendorSnapshot(ctx, q, vendorUUID) (id int, name string, err error)`, `itemSnapshot` struct, `resolveInventoryItem`, `validateCustom`, `taxPercentForRate` (store.go); `vbSelect` (SQL const), `vbMeta{internalID, statusID, vendorID int}`, `scanVendorBill(row pgx.Row) (*VendorBill, vbMeta, error)`, `loadLines(ctx, pool, internalID int) ([]Line, error)`, `Get(ctx, pool, uuid string) (*VendorBill, error)` (store_get.go) — every remaining task depends on these exact names.

- [ ] **Step 1: Write `vendorbill/store.go`**

```go
// vendorbill/store.go — shared helpers used by every verb file.
package vendorbill

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

// ErrNotFound is returned when a vendor bill uuid matches no live row.
var ErrNotFound = errors.New("vendor bill not found")

// ClientError signals a client-caused failure (validation, bad input) that a
// controller maps to HTTP 400.
type ClientError struct{ Msg string }

func (e ClientError) Error() string { return e.Msg }

// IsClientError reports whether err is a ClientError.
func IsClientError(err error) bool {
	var ce ClientError
	return errors.As(err, &ce)
}

// vbilRecordTypeCode is the lkp_record_type code for Vendor Bill.
const vbilRecordTypeCode = "VBIL"

// draftStatusCode is the status every new vendor bill starts at (AD-5).
const draftStatusCode = "DRFT"

// nullableInt converts a non-positive id to SQL NULL.
func nullableInt(v int) any {
	if v <= 0 {
		return nil
	}
	return v
}

// systemEmployeeID is the fallback actor for soft-delete columns that must
// never be NULL when their paired *_deleted_at timestamp is set.
const systemEmployeeID = 1

// actorOrSystem returns actorEmployeeID, or systemEmployeeID if it's unset (0).
func actorOrSystem(actorEmployeeID int) int {
	if actorEmployeeID == 0 {
		return systemEmployeeID
	}
	return actorEmployeeID
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
// violation (code 23503) -- an invalid caller-supplied reference id.
func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

// isUniqueViolation reports whether err is a PostgreSQL unique-constraint
// violation (code 23505).
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
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

// vendorSnapshot loads a vendor's internal id and display name for the
// create-time snapshot (AD-2). The display name prefers the organization
// legal name, falling back to the person given/family names.
func vendorSnapshot(ctx context.Context, q workflow.Querier, vendorUUID string) (id int, name string, err error) {
	err = q.QueryRow(ctx, `
		SELECT vendor_id,
		       CASE WHEN vendor_type = 'Organization' THEN vendor_legal_name
		            ELSE TRIM(vendor_given_name || ' ' || vendor_family_name) END
		FROM vendor WHERE vendor_uuid = $1 AND vendor_deleted_at IS NULL`, vendorUUID).Scan(&id, &name)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, "", ClientError{Msg: "Unknown vendor."}
	}
	if err != nil {
		return 0, "", fmt.Errorf("load vendor snapshot: %w", err)
	}
	return id, name, nil
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

// validateCustom validates custom fields against the "vendor_bill" workflow's
// field definitions (<=15, typed).
func validateCustom(ctx context.Context, pool *pgxpool.Pool, custom map[string]any) error {
	if custom == nil {
		return nil
	}
	wf, err := workflow.GetWorkflowByKey(ctx, pool, "vendor_bill")
	if errors.Is(err, workflow.ErrWorkflowNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load vendor_bill workflow: %w", err)
	}
	def, err := workflow.LoadDefinition(ctx, pool, wf.ID)
	if err != nil {
		return fmt.Errorf("load vendor_bill field definitions: %w", err)
	}
	if err := workflow.ValidateCustomFieldsPartial(def.Fields, custom); err != nil {
		return ClientError{Msg: err.Error()}
	}
	return nil
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
```

- [ ] **Step 2: Write `vendorbill/store_get.go`**

```go
// vendorbill/store_get.go — the shared header SELECT + scan and Get.
package vendorbill

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// vbSelect is the base SELECT shared by Get and Search. Column order must
// match scanVendorBill's Scan(...) arg order exactly. Table alias `vb`
// matches resolver.go's field expressions.
const vbSelect = `
	SELECT vb.vendor_bill_uuid, COALESCE(vb.vendor_bill_number,''),
	       rs.record_status_name, rs.record_status_code,
	       vb.vendor_bill_approval_status,
	       v.vendor_uuid, vb.vendor_bill_vendor_name, COALESCE(v.vendor_number,''),
	       COALESCE(po.purchase_order_uuid::text,''), COALESCE(po.purchase_order_number,''),
	       COALESCE(ou.id::text,''), vb.vendor_bill_owner_id,
	       vb.vendor_bill_vendor_invoice_number, vb.vendor_bill_reference_number,
	       to_char(vb.vendor_bill_date,'YYYY-MM-DD'), COALESCE(to_char(vb.vendor_bill_due_date,'YYYY-MM-DD'),''),
	       vb.vendor_bill_payment_terms, vb.vendor_bill_currency, vb.vendor_bill_exchange_rate,
	       vb.vendor_bill_sales_tax_percent,
	       vb.vendor_bill_memo, vb.vendor_bill_notes, vb.vendor_bill_internal_notes, vb.vendor_bill_terms_conditions,
	       vb.vendor_bill_subtotal, vb.vendor_bill_discount_total, vb.vendor_bill_tax_total,
	       vb.vendor_bill_adjustment, vb.vendor_bill_grand_total,
	       vb.vendor_bill_amount_paid, vb.vendor_bill_balance_due,
	       vb.vendor_bill_custom_fields,
	       vb.vendor_bill_created_at, vb.vendor_bill_updated_at, vb.vendor_bill_record_version,
	       vb.vendor_bill_id, vb.vendor_bill_status, vb.vendor_bill_vendor_id
	FROM vendor_bill vb
	JOIN lkp_record_status rs ON rs.record_status_id = vb.vendor_bill_status
	JOIN vendor v ON v.vendor_id = vb.vendor_bill_vendor_id
	LEFT JOIN purchase_order po ON po.purchase_order_id = vb.vendor_bill_purchase_order_id
	LEFT JOIN employee oe ON oe.employee_id = vb.vendor_bill_owner_id
	LEFT JOIN users ou ON ou.id = oe.employee_user_id`

// vbMeta carries the internal numeric ids a vendor bill row has but the API
// response deliberately does not expose. Search needs them to mint a keyset
// cursor for sorts that run on those columns (status, vendor_id).
type vbMeta struct {
	internalID int
	statusID   int
	vendorID   int
}

func scanVendorBill(row pgx.Row) (*VendorBill, vbMeta, error) {
	var (
		b                          VendorBill
		ownerEmpID                 *int
		paymentTermsID, currencyID *int
		custom                     map[string]any
		meta                       vbMeta
		poUUID, poNum              string
	)
	err := row.Scan(
		&b.ID, &b.Number,
		&b.StatusName, &b.StatusCode,
		&b.ApprovalStatus,
		&b.Vendor.ID, &b.Vendor.Name, &b.Vendor.Number,
		&poUUID, &poNum,
		&b.OwnerUserID, &ownerEmpID,
		&b.VendorInvoiceNumber, &b.ReferenceNumber,
		&b.BillDate, &b.DueDate,
		&paymentTermsID, &currencyID, &b.ExchangeRate,
		&b.SalesTaxPercent,
		&b.Memo, &b.Notes, &b.InternalNotes, &b.TermsConditions,
		&b.Subtotal, &b.DiscountTotal, &b.TaxTotal,
		&b.Adjustment, &b.GrandTotal,
		&b.AmountPaid, &b.BalanceDue,
		&custom, &b.CreatedAt, &b.UpdatedAt, &b.RecordVersion,
		&meta.internalID, &meta.statusID, &meta.vendorID,
	)
	if err != nil {
		return nil, vbMeta{}, err
	}
	if poUUID != "" {
		b.PurchaseOrder = &PurchaseOrderRef{ID: poUUID, Number: poNum}
	}
	b.OwnerEmployeeID = ownerEmpID
	b.PaymentTermsID = paymentTermsID
	b.CurrencyID = currencyID
	if custom == nil {
		custom = map[string]any{}
	}
	b.CustomFields = custom
	b.Items = []Line{}
	return &b, meta, nil
}

const lineSelect = `
	SELECT vbi.vendor_bill_item_uuid, vbi.line_number,
	       COALESCE(ii.inventory_item_uuid::text,''), COALESCE(poi.purchase_order_item_uuid::text,''),
	       vbi.sku, vbi.item_name, vbi.description, vbi.unit_id, vbi.unit_code,
	       vbi.quantity, vbi.unit_price, vbi.discount_percent, vbi.tax_rate_id, vbi.tax_percent,
	       vbi.line_subtotal, vbi.line_discount, vbi.line_tax, vbi.line_total
	FROM vendor_bill_item vbi
	LEFT JOIN inventory_item ii ON ii.inventory_item_id = vbi.inventory_item_id
	LEFT JOIN purchase_order_item poi ON poi.purchase_order_item_id = vbi.purchase_order_item_id
	WHERE vbi.vendor_bill_id = $1 AND vbi.item_deleted_at IS NULL
	ORDER BY vbi.line_number ASC`

func loadLines(ctx context.Context, pool *pgxpool.Pool, internalID int) ([]Line, error) {
	rows, err := pool.Query(ctx, lineSelect, internalID)
	if err != nil {
		return nil, fmt.Errorf("load vendor bill lines: %w", err)
	}
	defer rows.Close()
	out := []Line{}
	for rows.Next() {
		var l Line
		var invItemUUID, poItemUUID string
		if err := rows.Scan(&l.ID, &l.LineNumber, &invItemUUID, &poItemUUID,
			&l.SKU, &l.ItemName, &l.Description, &l.UnitID, &l.UnitCode,
			&l.Quantity, &l.UnitPrice, &l.DiscountPercent, &l.TaxRateID, &l.TaxPercent,
			&l.LineSubtotal, &l.LineDiscount, &l.LineTax, &l.LineTotal); err != nil {
			return nil, fmt.Errorf("scan vendor bill line: %w", err)
		}
		if invItemUUID != "" {
			l.InventoryItemID = &invItemUUID
		}
		if poItemUUID != "" {
			l.PurchaseOrderItemID = &poItemUUID
		}
		out = append(out, l)
	}
	return out, rows.Err()
}

// Get loads a single live vendor bill (header + lines + payments) by its
// external uuid.
func Get(ctx context.Context, pool *pgxpool.Pool, id string) (*VendorBill, error) {
	b, meta, err := scanVendorBill(pool.QueryRow(ctx, vbSelect+`
		WHERE vb.vendor_bill_uuid = $1 AND vb.vendor_bill_deleted_at IS NULL`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get vendor bill: %w", err)
	}
	lines, err := loadLines(ctx, pool, meta.internalID)
	if err != nil {
		return nil, err
	}
	b.Items = lines
	return b, nil
}
```

Note: `Get` does not yet load payments — `b.Payments` stays nil (`omitempty`, so it is simply absent from the JSON response) until Task 9 adds the payment ledger and edits this function to populate it. This keeps every task's build green; no task deliberately ships a non-compiling package.

- [ ] **Step 3: Verify it compiles**

```bash
go build ./vendorbill/... && go vet ./vendorbill/...
```

Expected: both exit 0. `resolver.go`'s `_ = query.FieldResolver(resolver{})` assertions confirm the interfaces are satisfied at compile time.

- [ ] **Step 4: Commit**

```bash
git add vendorbill/store.go vendorbill/store_get.go
git commit -m "feat(vendor-bill): add store shared helpers and Get"
```

---

## Task 5: Store — Line Resolution + Create

**Files:**
- Create: `vendorbill/store_line_resolve.go`
- Create: `vendorbill/store_create.go`

**Interfaces:**
- Consumes: `ClientError`, `isForeignKeyViolation`, `resolveInventoryItem`, `taxPercentForRate`, `colVal`, `buildInsert`, `nullableInt`, `orNow`, `nullableDate`, `recordTypeIDByCode`, `statusIDByCode`, `vbilRecordTypeCode`, `draftStatusCode`, `vendorSnapshot`, `validateCustom` (store.go); `ComputeLine`, `ComputeHeader`, `CalcLineInput`, `LineMoney` (calc.go); `FormatNumber` (numbering.go); `Get` (store_get.go).
- Produces: `resolvedLine` struct, `resolveLines(ctx, q, items []LineInput, headerTax float64) ([]resolvedLine, error)`, `insertLines(ctx, tx, vbInternalID int, lines []resolvedLine, actorEmployeeID int) error`, `writeHistory(ctx, tx, vbInternalID int, action string, fromStatusID, toStatusID *int, actorEmployeeID int)` (store_line_resolve.go — `writeHistory` is used by every remaining store task); `Create(ctx, pool, in CreateVendorBillInput, actorEmployeeID int) (*VendorBill, error)` (store_create.go).

- [ ] **Step 1: Write `vendorbill/store_line_resolve.go`**

```go
// vendorbill/store_line_resolve.go — line snapshot + tax resolution, shared
// by Create and Update.
package vendorbill

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"

	"stonesuite-backend/workflow"
)

// resolvedLine is a line after catalog/free-text resolution, ready to price
// and insert. It never carries a purchase_order_item_id (AD-8) -- that
// lineage FK is set only by the convert path (store_convert.go), never here.
type resolvedLine struct {
	lineNumber      int
	inventoryItemID *int
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
// (or free text), computing each line's stored money (AD-3, AD-9). headerTax
// is the header's default tax percent, used when a line has no tax rate.
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
		if in.DiscountPercent < 0 || in.DiscountPercent > 100 {
			return nil, ClientError{Msg: fmt.Sprintf("Line %d: discountPercent must be between 0 and 100.", in.LineNumber)}
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
			if in.Description != "" {
				rl.desc = in.Description
			}
		} else if strings.TrimSpace(in.Description) == "" {
			return nil, ClientError{Msg: fmt.Sprintf("Line %d: either an inventoryItemUuid or a description is required.", in.LineNumber)}
		} else {
			rl.name = in.Description
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

// insertLines bulk-inserts resolved lines as vendor_bill_item rows.
// purchase_order_item_id is always NULL here (AD-8) -- populated only by
// store_convert.go's own separate insert path.
func insertLines(ctx context.Context, tx pgx.Tx, vbInternalID int, lines []resolvedLine, actorEmployeeID int) error {
	for _, l := range lines {
		_, err := tx.Exec(ctx, `
			INSERT INTO vendor_bill_item (
				vendor_bill_id, line_number, inventory_item_id,
				item_name, sku, description, unit_id, unit_code,
				quantity, unit_price, discount_percent, tax_rate_id, tax_percent,
				line_subtotal, line_discount, line_tax, line_total,
				item_created_by
			) VALUES ($1,$2,$3, $4,$5,$6,$7,$8, $9,$10,$11,$12,$13, $14,$15,$16,$17, $18)`,
			vbInternalID, l.lineNumber, l.inventoryItemID,
			l.name, l.sku, l.desc, l.unitID, l.unitCode,
			l.quantity, l.unitPrice, l.discountPercent, l.taxRateID, l.taxPercent,
			l.money.Subtotal, l.money.Discount, l.money.Tax, l.money.Total,
			nullableInt(actorEmployeeID),
		)
		if err != nil {
			if isForeignKeyViolation(err) {
				return ClientError{Msg: fmt.Sprintf("Line %d: an invalid unit or tax rate was referenced.", l.lineNumber)}
			}
			return fmt.Errorf("insert vendor bill line: %w", err)
		}
	}
	return nil
}

// writeHistory records one vendor_bill_history row inside the caller's
// transaction. Used by Create, Update, Transition, Approve, and the convert
// path.
func writeHistory(ctx context.Context, tx pgx.Tx, vbInternalID int, action string, fromStatusID, toStatusID *int, actorEmployeeID int) {
	_, _ = tx.Exec(ctx, `
		INSERT INTO vendor_bill_history (vendor_bill_id, from_status_id, to_status_id, action, actor_employee_id)
		VALUES ($1,$2,$3,$4,$5)`,
		vbInternalID, fromStatusID, toStatusID, action, nullableInt(actorEmployeeID))
}
```

- [ ] **Step 2: Write `vendorbill/store_create.go`**

```go
// vendorbill/store_create.go
package vendorbill

import (
	"context"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Create inserts a new vendor bill (header + lines) inside one transaction:
// snapshots the vendor name (AD-2), resolves and prices every line (AD-3,
// AD-9), computes header totals, assigns the VBIL number (AD-10), and starts
// the bill at DRFT (AD-5). CustomFields is validated and nil-guarded before
// the insert -- vendor_bill_custom_fields is NOT NULL DEFAULT '{}'.
func Create(ctx context.Context, pool *pgxpool.Pool, in CreateVendorBillInput, actorEmployeeID int) (*VendorBill, error) {
	if strings.TrimSpace(in.VendorUUID) == "" {
		return nil, ClientError{Msg: "A vendor is required."}
	}
	if in.SalesTaxPercent < 0 || in.SalesTaxPercent > 100 {
		return nil, ClientError{Msg: "salesTaxPercent must be between 0 and 100."}
	}
	if err := validateCustom(ctx, pool, in.CustomFields); err != nil {
		return nil, err
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin create vendor bill: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	vendorInternalID, vendorName, err := vendorSnapshot(ctx, tx, in.VendorUUID)
	if err != nil {
		return nil, err
	}

	lines, err := resolveLines(ctx, tx, in.Items, in.SalesTaxPercent)
	if err != nil {
		return nil, err
	}
	lineMoney := make([]LineMoney, len(lines))
	for i, l := range lines {
		lineMoney[i] = l.money
	}
	header := ComputeHeader(lineMoney, in.Adjustment, 0)

	recordTypeID, err := recordTypeIDByCode(ctx, tx, vbilRecordTypeCode)
	if err != nil {
		return nil, fmt.Errorf("resolve VBIL record type: %w", err)
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
		{"vendor_bill_status", draftStatusID, ""},
		{"vendor_bill_vendor_id", vendorInternalID, ""},
		{"vendor_bill_vendor_name", vendorName, ""},
		{"vendor_bill_vendor_invoice_number", in.VendorInvoiceNumber, ""},
		{"vendor_bill_reference_number", in.ReferenceNumber, ""},
		{"vendor_bill_date", orNow(in.BillDate), "::date"},
		{"vendor_bill_due_date", nullableDate(in.DueDate), "::date"},
		{"vendor_bill_payment_terms", in.PaymentTermsID, ""},
		{"vendor_bill_currency", in.CurrencyID, ""},
		{"vendor_bill_sales_tax_percent", in.SalesTaxPercent, ""},
		{"vendor_bill_memo", in.Memo, ""},
		{"vendor_bill_notes", in.Notes, ""},
		{"vendor_bill_internal_notes", in.InternalNotes, ""},
		{"vendor_bill_terms_conditions", in.TermsConditions, ""},
		{"vendor_bill_owner_id", nullableInt(ownerEmployeeID), ""},
		{"vendor_bill_subtotal", header.Subtotal, ""},
		{"vendor_bill_discount_total", header.DiscountTotal, ""},
		{"vendor_bill_tax_total", header.TaxTotal, ""},
		{"vendor_bill_adjustment", in.Adjustment, ""},
		{"vendor_bill_grand_total", header.GrandTotal, ""},
		{"vendor_bill_amount_paid", header.AmountPaid, ""},
		{"vendor_bill_balance_due", header.BalanceDue, ""},
		{"vendor_bill_custom_fields", custom, ""},
		{"vendor_bill_created_by", nullableInt(actorEmployeeID), ""},
	}

	insertSQL, insertArgs := buildInsert("vendor_bill", cv, "vendor_bill_id, vendor_bill_uuid")
	var internalID int
	var newUUID string
	err = tx.QueryRow(ctx, insertSQL, insertArgs...).Scan(&internalID, &newUUID)
	if err != nil {
		if isForeignKeyViolation(err) {
			return nil, ClientError{Msg: "One of the referenced ids (payment terms, currency, or vendor) does not exist."}
		}
		return nil, fmt.Errorf("insert vendor bill: %w", err)
	}

	number := FormatNumber(int64(internalID))
	if _, err := tx.Exec(ctx, `UPDATE vendor_bill SET vendor_bill_number = $1 WHERE vendor_bill_id = $2`, number, internalID); err != nil {
		return nil, fmt.Errorf("set vendor bill number: %w", err)
	}

	if err := insertLines(ctx, tx, internalID, lines, actorEmployeeID); err != nil {
		return nil, err
	}

	writeHistory(ctx, tx, internalID, "create", nil, &draftStatusID, actorEmployeeID)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit create vendor bill: %w", err)
	}
	return Get(ctx, pool, newUUID)
}
```

- [ ] **Step 3: Verify it compiles**

```bash
go build ./vendorbill/... && go vet ./vendorbill/...
```

Expected: both exit 0.

- [ ] **Step 4: Commit**

```bash
git add vendorbill/store_line_resolve.go vendorbill/store_create.go
git commit -m "feat(vendor-bill): add line resolution and Create"
```

---

## Task 6: Store — Update + Delete

**Files:**
- Create: `vendorbill/store_update.go`
- Create: `vendorbill/store_delete.go`

**Interfaces:**
- Consumes: `resolveLines`, `insertLines`, `writeHistory` (store_line_resolve.go); `ComputeHeader`, `LineMoney` (calc.go); `colVal`, `buildUpdateSet`, `nullableInt`, `actorOrSystem`, `orNow`, `nullableDate`, `isForeignKeyViolation`, `validateCustom`, `draftStatusCode`, `ErrNotFound`, `ClientError` (store.go); `Get` (store_get.go).
- Produces: `Update(ctx, pool, uuid string, in UpdateVendorBillInput, actorEmployeeID int) (*VendorBill, error)` (store_update.go); `SoftDelete(ctx, pool, uuid string, actorEmployeeID int) error` (store_delete.go) — Task 12's controller calls both by these exact names.

- [ ] **Step 1: Write `vendorbill/store_update.go`**

```go
// vendorbill/store_update.go
package vendorbill

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Update replaces a live vendor bill's header fields and lines (recomputing
// totals) inside one transaction. Allowed only at DRFT (AD-12) -- once
// submitted for approval, recall it to draft (PAPV->DRFT) to revise.
// DRFT bills never have payments (AD-7 gates settlement to APPV/PART/ODUE),
// so amountPaid is always 0 here -- no "can't reduce below what's paid"
// guard is needed, unlike invoice's Update.
func Update(ctx context.Context, pool *pgxpool.Pool, uuid string, in UpdateVendorBillInput, actorEmployeeID int) (*VendorBill, error) {
	if in.SalesTaxPercent < 0 || in.SalesTaxPercent > 100 {
		return nil, ClientError{Msg: "salesTaxPercent must be between 0 and 100."}
	}
	if err := validateCustom(ctx, pool, in.CustomFields); err != nil {
		return nil, err
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin update vendor bill: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var internalID int
	var statusCode string
	err = tx.QueryRow(ctx, `
		SELECT vb.vendor_bill_id, rs.record_status_code
		FROM vendor_bill vb JOIN lkp_record_status rs ON rs.record_status_id = vb.vendor_bill_status
		WHERE vb.vendor_bill_uuid = $1 AND vb.vendor_bill_deleted_at IS NULL
		FOR UPDATE OF vb`, uuid,
	).Scan(&internalID, &statusCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load vendor bill for update: %w", err)
	}
	if statusCode != draftStatusCode {
		return nil, ClientError{Msg: "Only a draft vendor bill can be edited. Recall it to draft first."}
	}

	lines, err := resolveLines(ctx, tx, in.Items, in.SalesTaxPercent)
	if err != nil {
		return nil, err
	}
	lineMoney := make([]LineMoney, len(lines))
	for i, l := range lines {
		lineMoney[i] = l.money
	}
	header := ComputeHeader(lineMoney, in.Adjustment, 0)

	custom := in.CustomFields
	if custom == nil {
		custom = map[string]any{}
	}

	cv := []colVal{
		{"vendor_bill_vendor_invoice_number", in.VendorInvoiceNumber, ""},
		{"vendor_bill_reference_number", in.ReferenceNumber, ""},
		{"vendor_bill_date", orNow(in.BillDate), "::date"},
		{"vendor_bill_due_date", nullableDate(in.DueDate), "::date"},
		{"vendor_bill_payment_terms", in.PaymentTermsID, ""},
		{"vendor_bill_currency", in.CurrencyID, ""},
		{"vendor_bill_sales_tax_percent", in.SalesTaxPercent, ""},
		{"vendor_bill_memo", in.Memo, ""},
		{"vendor_bill_notes", in.Notes, ""},
		{"vendor_bill_internal_notes", in.InternalNotes, ""},
		{"vendor_bill_terms_conditions", in.TermsConditions, ""},
		{"vendor_bill_owner_id", in.OwnerEmployeeID, ""},
		{"vendor_bill_subtotal", header.Subtotal, ""},
		{"vendor_bill_discount_total", header.DiscountTotal, ""},
		{"vendor_bill_tax_total", header.TaxTotal, ""},
		{"vendor_bill_adjustment", in.Adjustment, ""},
		{"vendor_bill_grand_total", header.GrandTotal, ""},
		{"vendor_bill_balance_due", header.BalanceDue, ""},
		{"vendor_bill_custom_fields", custom, ""},
		{"vendor_bill_updated_by", nullableInt(actorEmployeeID), ""},
	}

	updateSQL, updateArgs := buildUpdateSet("vendor_bill", []any{uuid}, cv,
		[]string{"vendor_bill_updated_at = NOW()", "vendor_bill_record_version = vendor_bill_record_version + 1"},
		"vendor_bill_uuid = $1 AND vendor_bill_deleted_at IS NULL")
	if _, err = tx.Exec(ctx, updateSQL, updateArgs...); err != nil {
		if isForeignKeyViolation(err) {
			return nil, ClientError{Msg: "One of the referenced ids (payment terms or currency) does not exist."}
		}
		return nil, fmt.Errorf("update vendor bill: %w", err)
	}

	if _, err := tx.Exec(ctx,
		`UPDATE vendor_bill_item SET item_deleted_at = NOW() WHERE vendor_bill_id = $1 AND item_deleted_at IS NULL`,
		internalID); err != nil {
		return nil, fmt.Errorf("clear previous vendor bill items: %w", err)
	}
	if err := insertLines(ctx, tx, internalID, lines, actorEmployeeID); err != nil {
		return nil, err
	}

	writeHistory(ctx, tx, internalID, "update", nil, nil, actorEmployeeID)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit update vendor bill: %w", err)
	}
	return Get(ctx, pool, uuid)
}
```

- [ ] **Step 2: Write `vendorbill/store_delete.go`**

```go
// vendorbill/store_delete.go
package vendorbill

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// SoftDelete marks a vendor bill deleted (paired deleted_at/deleted_by).
// Only DRFT and VOID bills may be deleted (AD-12) -- a bill visible to an
// approver or already settled keeps its trail. Blocked while any live
// vendor_bill_payment references it (mirrors invoice.SoftDelete's guard on
// payment_application) -- remove the payment entries first.
func SoftDelete(ctx context.Context, pool *pgxpool.Pool, uuid string, actorEmployeeID int) error {
	var internalID int
	if err := pool.QueryRow(ctx,
		`SELECT vendor_bill_id FROM vendor_bill WHERE vendor_bill_uuid = $1 AND vendor_bill_deleted_at IS NULL`, uuid,
	).Scan(&internalID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return fmt.Errorf("resolve vendor bill for delete: %w", err)
	}

	var livePayments int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM vendor_bill_payment WHERE vendor_bill_id = $1 AND deleted_at IS NULL`,
		internalID).Scan(&livePayments); err != nil {
		return fmt.Errorf("count live vendor bill payments: %w", err)
	}
	if livePayments > 0 {
		return ClientError{Msg: "Cannot delete a vendor bill with live payments; remove them first."}
	}

	tag, err := pool.Exec(ctx, `
		UPDATE vendor_bill vb
		SET vendor_bill_deleted_at = NOW(), vendor_bill_deleted_by = $2
		FROM lkp_record_status rs
		WHERE vb.vendor_bill_uuid = $1 AND vb.vendor_bill_deleted_at IS NULL
		  AND rs.record_status_id = vb.vendor_bill_status
		  AND rs.record_status_code IN ('DRFT','VOID')`,
		uuid, actorOrSystem(actorEmployeeID))
	if err != nil {
		return fmt.Errorf("delete vendor bill: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ClientError{Msg: "Only a draft or void vendor bill can be deleted."}
	}
	return nil
}
```

- [ ] **Step 3: Verify it compiles**

```bash
go build ./vendorbill/... && go vet ./vendorbill/...
```

Expected: both exit 0.

- [ ] **Step 4: Commit**

```bash
git add vendorbill/store_update.go vendorbill/store_delete.go
git commit -m "feat(vendor-bill): add Update and SoftDelete"
```

---

## Task 7: Store — Search

**Files:**
- Create: `vendorbill/store_search.go`

**Interfaces:**
- Consumes: `resolver` (resolver.go); `vbSelect`, `scanVendorBill`, `vbMeta` (store_get.go); `query.Request`, `query.Build`, `query.NextCursor` (external `query/` package).
- Produces: `Search(ctx, pool, scope, actorIdentityID string, req query.Request) (Page, error)` — Task 12's controller `List`/`Search` handlers call this by name.

- [ ] **Step 1: Write `vendorbill/store_search.go`**

```go
// vendorbill/store_search.go
package vendorbill

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/query"
)

// employeeIDByIdentity resolves a control-plane identity to a tenant
// employee_id.
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

// Search lists vendor bills under the caller's RBAC scope with filter/sort/
// global search + keyset pagination. Scope x filter is ANDed -- a filter can
// only narrow the permitted set. List rows include lines already loaded by
// scanVendorBill's Items=[]Line{} default (empty, not nil) -- Search does not
// N+1-load lines, mirroring purchaseorder.Search.
func Search(ctx context.Context, pool *pgxpool.Pool, scope, actorIdentityID string, req query.Request) (Page, error) {
	where := []string{"vb.vendor_bill_deleted_at IS NULL"}
	var args []any
	nextIdx := 1
	if scope != string(authz.ScopeAll) {
		empID, found := employeeIDByIdentity(ctx, pool, actorIdentityID)
		if !found {
			return Page{}, nil
		}
		where = append(where, fmt.Sprintf("vb.vendor_bill_owner_id = $%d", nextIdx))
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

	q := vbSelect + " WHERE " + strings.Join(where, " AND ") +
		" ORDER BY " + built.OrderBy + " LIMIT " + strconv.Itoa(built.EffLimit+1)

	rows, err := pool.Query(ctx, q, args...)
	if err != nil {
		return Page{}, fmt.Errorf("search vendor bills: %w", err)
	}
	defer rows.Close()
	out := []VendorBill{}
	metas := []vbMeta{}
	for rows.Next() {
		b, meta, err := scanVendorBill(rows)
		if err != nil {
			return Page{}, err
		}
		out = append(out, *b)
		metas = append(metas, meta)
	}
	if err := rows.Err(); err != nil {
		return Page{}, fmt.Errorf("search vendor bills: %w", err)
	}

	page := Page{Records: out}
	if len(out) > built.EffLimit {
		page.HasMore = true
		page.Records = out[:built.EffLimit]
		lastIdx := built.EffLimit - 1
		last, lastMeta := page.Records[lastIdx], metas[lastIdx]
		page.NextCursor = query.NextCursor(last.ID, built.Sort, sortValue(last, lastMeta, built.Sort.Field))
	}
	return page, nil
}

// sortValue reads the effective sort field's value from a vendor bill to
// mint the next cursor. Every key in resolver.go's sortFields must appear
// here, or the cursor is built from the wrong column and every page after
// the first is wrong.
func sortValue(b VendorBill, meta vbMeta, field string) any {
	switch field {
	case "bill_date":
		return b.BillDate
	case "grand_total":
		return b.GrandTotal
	case "balance_due":
		return b.BalanceDue
	case "document_number", "record_number":
		return b.Number
	case "status":
		return meta.statusID
	case "vendor_id":
		return meta.vendorID
	default: // created_at (default)
		return b.CreatedAt
	}
}
```

- [ ] **Step 2: Verify it compiles**

```bash
go build ./vendorbill/... && go vet ./vendorbill/...
```

Expected: both exit 0.

- [ ] **Step 3: Commit**

```bash
git add vendorbill/store_search.go
git commit -m "feat(vendor-bill): add Search"
```

---

## Task 8: Approval Gate (AD-6)

**Files:**
- Create: `vendorbill/approval.go`

**Interfaces:**
- Consumes: `recordTypeIDByCode`, `vbilRecordTypeCode`, `ErrNotFound` (store.go); `writeHistory` (store_line_resolve.go); `Get` (store_get.go); `workflow.Querier` (external).
- Produces: `approvalNone`, `approvalPending`, `approvalApproved` (const strings); `ErrNotApprover`, `ErrApprovalRequired`, `ErrApprovalNotRequired` (errors); `activeApproverCount(ctx, q, recordTypeID, statusID int) (int, error)`, `signOffCount`, `isConfiguredApprover`, `Approve(ctx, pool, uuid string, approverEmployeeID int) (*VendorBill, error)` — Task 9's `Transition` and Task 12's controller `Approve` handler both depend on these exact names.

- [ ] **Step 1: Write `vendorbill/approval.go`**

```go
// vendorbill/approval.go — AD-6: the configuration-driven approval gate, an
// exact structural mirror of purchaseorder/approval.go.
package vendorbill

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/workflow"
)

// Approval status values stored in vendor_bill.vendor_bill_approval_status (AD-6).
const (
	approvalNone     = "none"     // no approvers configured for the current status
	approvalPending  = "pending"  // gated: awaiting the required sign-offs
	approvalApproved = "approved" // enough configured approvers have signed off
)

// ErrNotApprover is returned when a caller who is not a configured approver
// for the vendor bill's current status tries to approve it. Maps to 403.
var ErrNotApprover = errors.New("you are not a configured approver for this vendor bill's current status")

// ErrApprovalRequired is returned when a vendor bill is asked to leave a
// status that still requires approval sign-off. Maps to HTTP 409.
var ErrApprovalRequired = errors.New("this vendor bill must be approved before it can leave its current status")

// ErrApprovalNotRequired is returned when an approval is submitted for a
// vendor bill whose current status has no configured approvers. Maps to 409.
var ErrApprovalNotRequired = errors.New("this vendor bill's current status does not require approval")

// activeApproverCount returns how many active approvers are configured for
// the VBIL record type at a status. Zero means no approval gate there.
func activeApproverCount(ctx context.Context, q workflow.Querier, recordTypeID, statusID int) (int, error) {
	var n int
	if err := q.QueryRow(ctx, `
		SELECT COUNT(*) FROM vendor_bill_approver
		WHERE record_type_id = $1 AND record_status_id = $2 AND is_active`,
		recordTypeID, statusID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count vendor bill approvers: %w", err)
	}
	return n, nil
}

// signOffCount returns how many distinct approvers have signed off on a
// vendor bill at a status.
func signOffCount(ctx context.Context, q workflow.Querier, vbInternalID, statusID int) (int, error) {
	var n int
	if err := q.QueryRow(ctx, `
		SELECT COUNT(*) FROM vendor_bill_approval
		WHERE vendor_bill_id = $1 AND record_status_id = $2`,
		vbInternalID, statusID).Scan(&n); err != nil {
		return 0, fmt.Errorf("count vendor bill approvals: %w", err)
	}
	return n, nil
}

// isConfiguredApprover reports whether an employee is an active configured
// approver for the VBIL record type at a status.
func isConfiguredApprover(ctx context.Context, q workflow.Querier, recordTypeID, statusID, employeeID int) (bool, error) {
	var exists bool
	if err := q.QueryRow(ctx, `
		SELECT EXISTS(SELECT 1 FROM vendor_bill_approver
			WHERE record_type_id = $1 AND record_status_id = $2 AND approver_employee_id = $3 AND is_active)`,
		recordTypeID, statusID, employeeID).Scan(&exists); err != nil {
		return false, fmt.Errorf("check vendor bill approver: %w", err)
	}
	return exists, nil
}

// Approve records one approver's sign-off on a vendor bill at its current
// status (AD-6). Requires the caller to be a configured approver for that
// status, is idempotent per (bill, status, approver), and flips the header's
// approval_status to 'approved' once the sign-off count reaches the
// configured approver count.
func Approve(ctx context.Context, pool *pgxpool.Pool, uuid string, approverEmployeeID int) (*VendorBill, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin approve vendor bill: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var internalID, curStatusID int
	err = tx.QueryRow(ctx, `
		SELECT vendor_bill_id, vendor_bill_status FROM vendor_bill
		WHERE vendor_bill_uuid = $1 AND vendor_bill_deleted_at IS NULL
		FOR UPDATE`, uuid).Scan(&internalID, &curStatusID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load vendor bill for approval: %w", err)
	}

	recordTypeID, err := recordTypeIDByCode(ctx, tx, vbilRecordTypeCode)
	if err != nil {
		return nil, fmt.Errorf("resolve VBIL record type: %w", err)
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
		INSERT INTO vendor_bill_approval (vendor_bill_id, record_status_id, approver_employee_id)
		VALUES ($1, $2, $3)
		ON CONFLICT (vendor_bill_id, record_status_id, approver_employee_id) DO NOTHING`,
		internalID, curStatusID, approverEmployeeID); err != nil {
		return nil, fmt.Errorf("record vendor bill approval: %w", err)
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
		UPDATE vendor_bill SET
			vendor_bill_approval_status = $2, vendor_bill_approved_by = $3, vendor_bill_updated_at = NOW()
		WHERE vendor_bill_id = $1`, internalID, newStatus, approvedBy); err != nil {
		return nil, fmt.Errorf("update vendor bill approval status: %w", err)
	}

	writeHistory(ctx, tx, internalID, "approve", &curStatusID, &curStatusID, approverEmployeeID)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit approve vendor bill: %w", err)
	}
	return Get(ctx, pool, uuid)
}
```

- [ ] **Step 2: Verify it compiles**

```bash
go build ./vendorbill/... && go vet ./vendorbill/...
```

Expected: both exit 0.

- [ ] **Step 3: Commit**

```bash
git add vendorbill/approval.go
git commit -m "feat(vendor-bill): add AD-6 approval gate"
```

---

## Task 9: Store — Transition

**Files:**
- Create: `vendorbill/store_transition.go`

**Interfaces:**
- Consumes: `ValidateTransition` (transitions.go); `recordTypeIDByCode`, `statusIDByCode`, `vbilRecordTypeCode`, `draftStatusCode`, `nullableInt`, `ErrNotFound`, `ClientError` (store.go); `writeHistory` (store_line_resolve.go); `activeApproverCount`, `approvalNone`, `approvalPending`, `approvalApproved`, `ErrApprovalRequired` (approval.go, Task 8); `Get` (store_get.go).
- Produces: `Transition(ctx, pool, uuid, toStatusCode string, actorEmployeeID int) (*VendorBill, error)` — Task 12's controller `Transition` handler calls this by name.

- [ ] **Step 1: Write `vendorbill/store_transition.go`**

```go
// vendorbill/store_transition.go
package vendorbill

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Transition moves a live vendor bill to toStatusCode, validating the move
// against the static transition map (AD-5), row-locking the bill to
// serialize concurrent transitions, enforcing the AD-6 approval gate, and
// writing a history row.
func Transition(ctx context.Context, pool *pgxpool.Pool, uuid, toStatusCode string, actorEmployeeID int) (*VendorBill, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transition: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var internalID, curStatusID int
	var curStatusCode, approvalStatus string
	err = tx.QueryRow(ctx, `
		SELECT vb.vendor_bill_id, vb.vendor_bill_status, rs.record_status_code, vb.vendor_bill_approval_status
		FROM vendor_bill vb JOIN lkp_record_status rs ON rs.record_status_id = vb.vendor_bill_status
		WHERE vb.vendor_bill_uuid = $1 AND vb.vendor_bill_deleted_at IS NULL
		FOR UPDATE OF vb`, uuid,
	).Scan(&internalID, &curStatusID, &curStatusCode, &approvalStatus)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load vendor bill for transition: %w", err)
	}
	if err := ValidateTransition(curStatusCode, toStatusCode); err != nil {
		return nil, err
	}

	recordTypeID, err := recordTypeIDByCode(ctx, tx, vbilRecordTypeCode)
	if err != nil {
		return nil, fmt.Errorf("resolve VBIL record type: %w", err)
	}
	toStatusID, err := statusIDByCode(ctx, tx, recordTypeID, toStatusCode)
	if err != nil {
		return nil, ClientError{Msg: "Unknown target status."}
	}

	// AD-6 approval gate: a vendor bill may not leave a status that has
	// configured approvers until it has been approved. Recalling to draft
	// (-> DRFT) is always allowed -- it is how a submitter withdraws a
	// pending bill for rework without an approver's sign-off.
	if toStatusCode != draftStatusCode {
		requiredHere, err := activeApproverCount(ctx, tx, recordTypeID, curStatusID)
		if err != nil {
			return nil, err
		}
		if requiredHere > 0 && approvalStatus != approvalApproved {
			return nil, ErrApprovalRequired
		}
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
		UPDATE vendor_bill SET
			vendor_bill_status = $2, vendor_bill_approval_status = $4, vendor_bill_approved_by = NULL,
			vendor_bill_updated_at = NOW(),
			vendor_bill_updated_by = $3, vendor_bill_record_version = vendor_bill_record_version + 1
		WHERE vendor_bill_id = $1`, internalID, toStatusID, nullableInt(actorEmployeeID), newApprovalStatus); err != nil {
		return nil, fmt.Errorf("transition vendor bill: %w", err)
	}

	writeHistory(ctx, tx, internalID, "transition", &curStatusID, &toStatusID, actorEmployeeID)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit transition: %w", err)
	}
	return Get(ctx, pool, uuid)
}
```

- [ ] **Step 2: Verify it compiles**

```bash
go build ./vendorbill/... && go vet ./vendorbill/...
```

Expected: both exit 0 — `approval.go` (Task 8) already supplies every symbol this file references.

- [ ] **Step 3: Commit**

```bash
git add vendorbill/store_transition.go
git commit -m "feat(vendor-bill): add Transition"
```

---

## Task 10: Balance + Settlement Ledger (AD-7, AD-15)

**Files:**
- Create: `vendorbill/balance.go`
- Create: `vendorbill/balance_test.go`
- Create: `vendorbill/store_payment.go`
- Modify: `vendorbill/store_get.go` (wire `loadPayments` into `Get`, deferred from Task 4)

**Interfaces:**
- Consumes: `round2` (calc.go); `ClientError`, `nullableInt` (store.go); `ErrNotFound` (store.go).
- Produces: `PayableStatuses map[string]bool`, `Locked{InternalID, VendorID int; StatusCode string; GrandTotal, AmountPaid float64}`, `(l Locked) BalanceDue() float64`, `LockForUpdate(ctx, tx pgx.Tx, billUUID string) (Locked, error)`, `DeriveStatus(currentCode string, amountPaid, grandTotal float64) string`, `RecomputeBalance(ctx, tx pgx.Tx, l Locked, action string, actorEmployeeID int) error` (balance.go); `loadPayments(ctx, pool, vbInternalID int) ([]BillPayment, error)`, `RecordPaymentInput`, `RecordPayment(ctx, pool, uuid string, in RecordPaymentInput, actorEmployeeID int) (*VendorBill, error)`, `RemovePayment(ctx, pool, billUUID, paymentUUID string, actorEmployeeID int) (*VendorBill, error)`, `ListPayments(ctx, pool, billUUID string) ([]BillPayment, error)` (store_payment.go) — Task 12/13's controllers call `RecordPayment`/`RemovePayment`/`ListPayments` by these exact names.

- [ ] **Step 1: Write `vendorbill/balance.go`**

```go
// vendorbill/balance.go — AD-7: the AP balance identity, the accounts-
// payable mirror of invoice/balance.go's AR identity.
package vendorbill

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// PayableStatuses are the only vendor bill statuses that accept a new
// settlement. A bill must be approved before anything can be paid against it.
var PayableStatuses = map[string]bool{"APPV": true, "PART": true, "ODUE": true}

// Locked is a row-locked live vendor bill, loaded inside a transaction by
// LockForUpdate. It carries the two inputs to the AP balance identity so
// callers can gate on the live balance without re-querying.
type Locked struct {
	InternalID int
	VendorID   int
	StatusCode string
	GrandTotal float64
	AmountPaid float64
}

// BalanceDue is the vendor bill's live outstanding balance: grand_total -
// amount_paid, floored at zero.
func (l Locked) BalanceDue() float64 {
	b := round2(l.GrandTotal - l.AmountPaid)
	if b < 0 {
		return 0
	}
	return b
}

// LockForUpdate loads and row-locks a live vendor bill by uuid inside tx.
//
// Lock order (AD-15): vendor_bill_payment < vendor_bill -- a fresh hierarchy
// that does not overlap the AR side's documented credit_memo < payment <
// invoice, so no cycle -- hence no deadlock -- is possible across the two.
func LockForUpdate(ctx context.Context, tx pgx.Tx, billUUID string) (Locked, error) {
	var l Locked
	err := tx.QueryRow(ctx, `
		SELECT vb.vendor_bill_id, vb.vendor_bill_vendor_id, rs.record_status_code,
		       vb.vendor_bill_grand_total, vb.vendor_bill_amount_paid
		FROM vendor_bill vb
		JOIN lkp_record_status rs ON rs.record_status_id = vb.vendor_bill_status
		WHERE vb.vendor_bill_uuid = $1 AND vb.vendor_bill_deleted_at IS NULL
		FOR UPDATE OF vb`, billUUID,
	).Scan(&l.InternalID, &l.VendorID, &l.StatusCode, &l.GrandTotal, &l.AmountPaid)
	if errors.Is(err, pgx.ErrNoRows) {
		return Locked{}, ClientError{Msg: "Unknown or deleted vendor bill."}
	}
	if err != nil {
		return Locked{}, fmt.Errorf("lock vendor bill: %w", err)
	}
	return l, nil
}

// DeriveStatus re-derives a vendor bill's status purely from what has been
// settled against it (AD-7).
//
// This intentionally does NOT go through CanTransition: that map is for
// user-directed transitions and has no path back out of PAID, or from PART
// to APPV -- moves an unapply legitimately needs.
func DeriveStatus(currentCode string, amountPaid, grandTotal float64) string {
	balanceDue := grandTotal - amountPaid
	switch {
	case balanceDue <= 0.005:
		return "PAID"
	case amountPaid > 0.005:
		return "PART"
	case currentCode == "PART" || currentCode == "PAID":
		return "APPV" // fully unapplied back to zero
	default:
		return currentCode
	}
}

// RecomputeBalance is the sole writer of a vendor bill's AP rollup (AD-7).
//
// It recomputes vendor_bill_amount_paid from the live vendor_bill_payment
// ledger, derives balance_due and status, and writes a vendor_bill_history
// row -- all inside tx. RecordPayment and RemovePayment both route through
// this so the balance identity exists in exactly one place.
func RecomputeBalance(ctx context.Context, tx pgx.Tx, l Locked, action string, actorEmployeeID int) error {
	var amountPaid float64
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(amount), 0) FROM vendor_bill_payment
		WHERE vendor_bill_id = $1 AND deleted_at IS NULL`,
		l.InternalID).Scan(&amountPaid); err != nil {
		return fmt.Errorf("sum vendor bill payments: %w", err)
	}
	amountPaid = round2(amountPaid)

	updated := Locked{GrandTotal: l.GrandTotal, AmountPaid: amountPaid}
	balanceDue := updated.BalanceDue()
	toCode := DeriveStatus(l.StatusCode, amountPaid, l.GrandTotal)

	var vbTypeID int
	if err := tx.QueryRow(ctx, `SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'VBIL'`).Scan(&vbTypeID); err != nil {
		return fmt.Errorf("resolve VBIL type: %w", err)
	}
	var fromStatusID, toStatusID int
	if err := tx.QueryRow(ctx, `SELECT record_status_id FROM lkp_record_status WHERE record_status_record_type = $1 AND record_status_code = $2`,
		vbTypeID, l.StatusCode).Scan(&fromStatusID); err != nil {
		return fmt.Errorf("resolve vendor bill from-status: %w", err)
	}
	if err := tx.QueryRow(ctx, `SELECT record_status_id FROM lkp_record_status WHERE record_status_record_type = $1 AND record_status_code = $2`,
		vbTypeID, toCode).Scan(&toStatusID); err != nil {
		return fmt.Errorf("resolve vendor bill to-status: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE vendor_bill SET vendor_bill_amount_paid = $1, vendor_bill_balance_due = $2,
			vendor_bill_status = $3, vendor_bill_updated_at = NOW(), vendor_bill_updated_by = $4,
			vendor_bill_record_version = vendor_bill_record_version + 1
		WHERE vendor_bill_id = $5`,
		amountPaid, balanceDue, toStatusID, nullableInt(actorEmployeeID), l.InternalID); err != nil {
		return fmt.Errorf("update vendor bill rollup: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO vendor_bill_history (vendor_bill_id, from_status_id, to_status_id, action, actor_employee_id)
		VALUES ($1, $2, $3, $4, $5)`,
		l.InternalID, fromStatusID, toStatusID, action, nullableInt(actorEmployeeID)); err != nil {
		return fmt.Errorf("insert vendor bill %s history: %w", action, err)
	}
	return nil
}
```

- [ ] **Step 2: Write `vendorbill/balance_test.go`**

```go
package vendorbill

import "testing"

func TestLockedBalanceDue(t *testing.T) {
	tests := []struct {
		name       string
		l          Locked
		want       float64
	}{
		{"unpaid", Locked{GrandTotal: 100, AmountPaid: 0}, 100},
		{"partially paid", Locked{GrandTotal: 100, AmountPaid: 40}, 60},
		{"fully paid", Locked{GrandTotal: 100, AmountPaid: 100}, 0},
		{"overpaid never negative", Locked{GrandTotal: 100, AmountPaid: 150}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.l.BalanceDue(); got != tt.want {
				t.Errorf("BalanceDue() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDeriveStatus(t *testing.T) {
	tests := []struct {
		name       string
		current    string
		amountPaid float64
		grandTotal float64
		want       string
	}{
		{"unpaid stays current", "APPV", 0, 100, "APPV"},
		{"partial payment", "APPV", 40, 100, "PART"},
		{"full payment", "APPV", 100, 100, "PAID"},
		{"full payment from PART", "PART", 100, 100, "PAID"},
		{"unapplied back to zero from PART", "PART", 0, 100, "APPV"},
		{"unapplied back to zero from PAID", "PAID", 0, 100, "APPV"},
		{"overdue with partial payment resolves to PART", "ODUE", 40, 100, "PART"},
		{"overdue fully paid resolves to PAID", "ODUE", 100, 100, "PAID"},
		{"overdue stays overdue while unpaid", "ODUE", 0, 100, "ODUE"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := DeriveStatus(tt.current, tt.amountPaid, tt.grandTotal); got != tt.want {
				t.Errorf("DeriveStatus(%q, %v, %v) = %q, want %q", tt.current, tt.amountPaid, tt.grandTotal, got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 3: Run the balance tests**

```bash
go test ./vendorbill/... -run "TestLockedBalanceDue|TestDeriveStatus" -v
```

Expected: all PASS.

- [ ] **Step 4: Write `vendorbill/store_payment.go`**

```go
// vendorbill/store_payment.go — AD-7: the bill-owned settlement ledger CRUD.
package vendorbill

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const paymentSelect = `
	SELECT vbp.vendor_bill_payment_uuid, vbp.amount, vbp.payment_method_id, COALESCE(pm.payment_method_name,''),
	       vbp.reference_number, vbp.memo, to_char(vbp.paid_at,'YYYY-MM-DD'), vbp.created_at
	FROM vendor_bill_payment vbp
	LEFT JOIN lkp_payment_method pm ON pm.payment_method_id = vbp.payment_method_id
	WHERE vbp.vendor_bill_id = $1 AND vbp.deleted_at IS NULL
	ORDER BY vbp.paid_at DESC, vbp.created_at DESC`

// loadPayments fetches a vendor bill's live settlement ledger by its
// internal id.
func loadPayments(ctx context.Context, pool *pgxpool.Pool, vbInternalID int) ([]BillPayment, error) {
	rows, err := pool.Query(ctx, paymentSelect, vbInternalID)
	if err != nil {
		return nil, fmt.Errorf("load vendor bill payments: %w", err)
	}
	defer rows.Close()
	out := []BillPayment{}
	for rows.Next() {
		var p BillPayment
		if err := rows.Scan(&p.ID, &p.Amount, &p.MethodID, &p.MethodName,
			&p.ReferenceNumber, &p.Memo, &p.PaidAt, &p.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan vendor bill payment: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// RecordPaymentInput is the request payload for POST /{uuid}/payment.
type RecordPaymentInput struct {
	Amount          float64 `json:"amount"`
	MethodID        *int    `json:"methodId,omitempty"`
	ReferenceNumber string  `json:"referenceNumber"`
	Memo            string  `json:"memo"`
	PaidAt          string  `json:"paidAt"` // "yyyy-mm-dd"; blank => CURRENT_DATE
}

// RecordPayment records one settlement against a vendor bill (AD-7, AD-15):
// row-locks the bill, rejects settlement outside PayableStatuses, rejects
// overpayment (never silently clamps -- matches payment.Apply's contract),
// inserts the ledger row, and recomputes the AP rollup, all inside tx.
func RecordPayment(ctx context.Context, pool *pgxpool.Pool, uuid string, in RecordPaymentInput, actorEmployeeID int) (*VendorBill, error) {
	if in.Amount <= 0 {
		return nil, ClientError{Msg: "amount must be positive."}
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin record vendor bill payment: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	l, err := LockForUpdate(ctx, tx, uuid)
	if err != nil {
		return nil, err
	}
	if !PayableStatuses[l.StatusCode] {
		return nil, ClientError{Msg: "A vendor bill can only be paid while APPV, PART, or ODUE (current status: " + l.StatusCode + ")."}
	}
	if in.Amount > l.BalanceDue()+0.005 {
		return nil, ClientError{Msg: fmt.Sprintf("Amount %.2f exceeds the outstanding balance of %.2f.", in.Amount, l.BalanceDue())}
	}

	if in.MethodID != nil {
		var exists bool
		if err := tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM lkp_payment_method WHERE payment_method_id = $1)`, *in.MethodID).Scan(&exists); err != nil {
			return nil, fmt.Errorf("check payment method: %w", err)
		}
		if !exists {
			return nil, ClientError{Msg: "Unknown payment method."}
		}
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO vendor_bill_payment (
			vendor_bill_id, payment_method_id, amount, reference_number, memo, paid_at, created_by
		) VALUES ($1,$2,$3,$4,$5, COALESCE(NULLIF($6,'')::date, CURRENT_DATE), $7)`,
		l.InternalID, in.MethodID, in.Amount, in.ReferenceNumber, in.Memo, in.PaidAt, nullableInt(actorEmployeeID),
	); err != nil {
		return nil, fmt.Errorf("insert vendor bill payment: %w", err)
	}

	if err := RecomputeBalance(ctx, tx, l, "payment", actorEmployeeID); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit record vendor bill payment: %w", err)
	}
	return Get(ctx, pool, uuid)
}

// RemovePayment soft-deletes a single ledger entry (the "unapply") and
// recomputes the AP rollup -- both inside tx.
func RemovePayment(ctx context.Context, pool *pgxpool.Pool, billUUID, paymentUUID string, actorEmployeeID int) (*VendorBill, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin remove vendor bill payment: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	l, err := LockForUpdate(ctx, tx, billUUID)
	if err != nil {
		return nil, err
	}

	tag, err := tx.Exec(ctx, `
		UPDATE vendor_bill_payment SET deleted_at = NOW()
		WHERE vendor_bill_payment_uuid = $1 AND vendor_bill_id = $2 AND deleted_at IS NULL`,
		paymentUUID, l.InternalID)
	if err != nil {
		return nil, fmt.Errorf("remove vendor bill payment: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ClientError{Msg: "Payment not found on this vendor bill."}
	}

	if err := RecomputeBalance(ctx, tx, l, "unapply_payment", actorEmployeeID); err != nil {
		return nil, err
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit remove vendor bill payment: %w", err)
	}
	return Get(ctx, pool, billUUID)
}

// ListPayments returns the live settlement ledger for a vendor bill -- a
// read, used by GET /{uuid}/payments.
func ListPayments(ctx context.Context, pool *pgxpool.Pool, billUUID string) ([]BillPayment, error) {
	var internalID int
	if err := pool.QueryRow(ctx,
		`SELECT vendor_bill_id FROM vendor_bill WHERE vendor_bill_uuid = $1 AND vendor_bill_deleted_at IS NULL`, billUUID,
	).Scan(&internalID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, ErrNotFound
		}
		return nil, fmt.Errorf("resolve vendor bill for payments: %w", err)
	}
	return loadPayments(ctx, pool, internalID)
}
```

- [ ] **Step 5: Wire `loadPayments` into `Get`**

In `vendorbill/store_get.go`, find the `Get` function (written in Task 4) and change:

```go
	lines, err := loadLines(ctx, pool, meta.internalID)
	if err != nil {
		return nil, err
	}
	b.Items = lines
	return b, nil
}
```

to:

```go
	lines, err := loadLines(ctx, pool, meta.internalID)
	if err != nil {
		return nil, err
	}
	b.Items = lines
	payments, err := loadPayments(ctx, pool, meta.internalID)
	if err != nil {
		return nil, err
	}
	b.Payments = payments
	return b, nil
}
```

- [ ] **Step 6: Verify it compiles and passes**

```bash
go build ./vendorbill/... && go vet ./vendorbill/... && go test ./vendorbill/...
```

Expected: build and vet exit 0; every pure-logic test from Tasks 2, 3, and this task passes.

- [ ] **Step 7: Commit**

```bash
git add vendorbill/balance.go vendorbill/balance_test.go vendorbill/store_payment.go vendorbill/store_get.go
git commit -m "feat(vendor-bill): add AP balance identity and settlement ledger"
```

---

## Task 11: Conversion from Purchase Order (AD-8)

**Files:**
- Create: `vendorbill/store_convert.go`

**Interfaces:**
- Consumes: `ClientError`, `colVal`, `buildInsert`, `nullableInt`, `isForeignKeyViolation`, `recordTypeIDByCode`, `statusIDByCode`, `vbilRecordTypeCode`, `draftStatusCode` (store.go); `writeHistory` (store_line_resolve.go); `ComputeLine`, `ComputeHeader`, `CalcLineInput`, `LineMoney` (calc.go); `FormatNumber` (numbering.go); `Get` (store_get.go).
- Produces: `ErrPurchaseOrderNotFound`, `ConvertFromPurchaseOrder(ctx, pool, poUUID string, actorEmployeeID int) (*VendorBill, error)` — Task 13's `controllers/purchaseorder_convert.go` calls this by name.

- [ ] **Step 1: Write `vendorbill/store_convert.go`**

```go
// vendorbill/store_convert.go — AD-8: a Purchase Order converts into a
// Vendor Bill. Lives here (the destination package), not in purchaseorder/ --
// the same convention purchaseorder.ConvertFromRequisition, salesorder.
// ConvertFromQuote, and invoice.ConvertFromSalesOrder already established:
// the destination-owning package performs the write, raw SQL against the
// source table, no import of the purchaseorder Go package.
//
// Unlike ConvertFromRequisition, a purchase order may convert MORE THAN ONCE
// -- vendors routinely bill a single order in installments -- so there is no
// idempotent-replay short-circuit here; every call creates a new bill. Only
// a received purchase order (RCVD or CLSD) may convert.
package vendorbill

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrPurchaseOrderNotFound is returned when the source purchase order uuid
// matches no live row.
var ErrPurchaseOrderNotFound = errors.New("purchase order not found")

// purchaseOrderSnapshot is the subset of a source purchase order's header
// the convert path copies verbatim.
type purchaseOrderSnapshot struct {
	internalID                                   int
	statusCode                                   string
	vendorID                                     int
	vendorName                                   string
	referenceNumber                              string
	paymentTermsID                               *int
	currencyID                                   *int
	salesTaxPercent                              float64
	memo, notes, internalNotes, termsConditions  string
	customFields                                 map[string]any
}

// loadPurchaseOrderSnapshot loads a live purchase order's header snapshot by
// external uuid inside tx (row is not locked -- a source document does not
// change once converted, and purchase order has no FOR UPDATE convention
// elsewhere for this read, mirroring loadRequisitionSnapshot).
func loadPurchaseOrderSnapshot(ctx context.Context, tx pgx.Tx, poUUID string) (*purchaseOrderSnapshot, error) {
	var s purchaseOrderSnapshot
	var customRaw []byte
	err := tx.QueryRow(ctx, `
		SELECT po.purchase_order_id, rs.record_status_code,
		       po.purchase_order_vendor_id, po.purchase_order_vendor_name,
		       po.purchase_order_reference_number, po.purchase_order_payment_terms, po.purchase_order_currency,
		       po.purchase_order_sales_tax_percent,
		       po.purchase_order_memo, po.purchase_order_notes, po.purchase_order_internal_notes, po.purchase_order_terms_conditions,
		       po.purchase_order_custom_fields
		FROM purchase_order po JOIN lkp_record_status rs ON rs.record_status_id = po.purchase_order_status
		WHERE po.purchase_order_uuid = $1 AND po.purchase_order_deleted_at IS NULL`, poUUID).Scan(
		&s.internalID, &s.statusCode,
		&s.vendorID, &s.vendorName,
		&s.referenceNumber, &s.paymentTermsID, &s.currencyID,
		&s.salesTaxPercent,
		&s.memo, &s.notes, &s.internalNotes, &s.termsConditions,
		&customRaw,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrPurchaseOrderNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load purchase order snapshot: %w", err)
	}
	s.customFields = map[string]any{}
	if len(customRaw) > 0 {
		_ = json.Unmarshal(customRaw, &s.customFields)
	}
	return &s, nil
}

// poSourceLine is one live purchase_order_item row's frozen values, copied
// verbatim (not re-priced) into the new bill's lines.
type poSourceLine struct {
	uuid                string
	lineNumber          int
	inventoryItemID     *int
	itemName, sku, desc string
	unitID              *int
	unitCode            string
	quantity            float64
	unitPrice           float64
	discountPercent     float64
	taxRateID           *int
	taxPercent          float64
}

// loadPurchaseOrderSourceLines loads a live purchase order's lines by its
// internal id.
func loadPurchaseOrderSourceLines(ctx context.Context, tx pgx.Tx, poInternalID int) ([]poSourceLine, error) {
	rows, err := tx.Query(ctx, `
		SELECT purchase_order_item_uuid, line_number, inventory_item_id,
		       item_name, sku, description, unit_id, COALESCE(unit_code,''),
		       quantity, unit_price, discount_percent, tax_rate_id, tax_percent
		FROM purchase_order_item
		WHERE purchase_order_id = $1 AND item_deleted_at IS NULL
		ORDER BY line_number`, poInternalID)
	if err != nil {
		return nil, fmt.Errorf("load purchase order lines: %w", err)
	}
	defer rows.Close()
	out := []poSourceLine{}
	for rows.Next() {
		var l poSourceLine
		if err := rows.Scan(
			&l.uuid, &l.lineNumber, &l.inventoryItemID,
			&l.itemName, &l.sku, &l.desc, &l.unitID, &l.unitCode,
			&l.quantity, &l.unitPrice, &l.discountPercent, &l.taxRateID, &l.taxPercent,
		); err != nil {
			return nil, fmt.Errorf("scan purchase order line: %w", err)
		}
		out = append(out, l)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) == 0 {
		return nil, ClientError{Msg: "Purchase order has no line items to convert."}
	}
	return out, nil
}

// insertConvertedLines bulk-inserts PO-sourced lines as vendor_bill_item
// rows, copied verbatim (never re-priced -- AD-8), each linked back to its
// source purchase_order_item for traceability (AD-3). Returns the
// {poItemUuid: billItemUuid} lineage map and the computed line money (for
// the header recompute).
func insertConvertedLines(ctx context.Context, tx pgx.Tx, vbInternalID int, lines []poSourceLine, actorEmployeeID int) (map[string]string, []LineMoney, error) {
	lineMap := make(map[string]string, len(lines))
	lineMoneys := make([]LineMoney, 0, len(lines))
	for _, l := range lines {
		money := ComputeLine(CalcLineInput{
			Quantity: l.quantity, UnitPrice: l.unitPrice,
			DiscountPercent: l.discountPercent, TaxPercent: l.taxPercent,
		})
		var srcPOItemInternalID int
		if err := tx.QueryRow(ctx,
			`SELECT purchase_order_item_id FROM purchase_order_item WHERE purchase_order_item_uuid = $1`, l.uuid,
		).Scan(&srcPOItemInternalID); err != nil {
			return nil, nil, fmt.Errorf("resolve source purchase order item: %w", err)
		}
		var newLineUUID string
		err := tx.QueryRow(ctx, `
			INSERT INTO vendor_bill_item (
				vendor_bill_id, line_number, inventory_item_id, purchase_order_item_id,
				item_name, sku, description, unit_id, unit_code,
				quantity, unit_price, discount_percent, tax_rate_id, tax_percent,
				line_subtotal, line_discount, line_tax, line_total,
				item_created_by
			) VALUES ($1,$2,$3,$4, $5,$6,$7,$8,$9, $10,$11,$12,$13,$14, $15,$16,$17,$18, $19)
			RETURNING vendor_bill_item_uuid`,
			vbInternalID, l.lineNumber, l.inventoryItemID, srcPOItemInternalID,
			l.itemName, l.sku, l.desc, l.unitID, l.unitCode,
			l.quantity, l.unitPrice, l.discountPercent, l.taxRateID, l.taxPercent,
			money.Subtotal, money.Discount, money.Tax, money.Total,
			nullableInt(actorEmployeeID),
		).Scan(&newLineUUID)
		if err != nil {
			return nil, nil, fmt.Errorf("insert converted vendor bill item: %w", err)
		}
		lineMap[l.uuid] = newLineUUID
		lineMoneys = append(lineMoneys, money)
	}
	return lineMap, lineMoneys, nil
}

// ConvertFromPurchaseOrder creates a new VendorBill as a snapshot copy of a
// live purchase order's header + lines: every line item is copied verbatim
// (not re-priced against current catalog data), header totals are
// recomputed from the copied lines via vendorbill's own calc, and the
// lineage is recorded in vendor_bill_conversion. Only a received purchase
// order (RCVD or CLSD) may convert.
func ConvertFromPurchaseOrder(ctx context.Context, pool *pgxpool.Pool, poUUID string, actorEmployeeID int) (*VendorBill, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin convert purchase order: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	src, err := loadPurchaseOrderSnapshot(ctx, tx, poUUID)
	if err != nil {
		return nil, err
	}
	if src.statusCode != "RCVD" && src.statusCode != "CLSD" {
		return nil, ClientError{Msg: "Only a received purchase order can be converted to a vendor bill."}
	}

	lines, err := loadPurchaseOrderSourceLines(ctx, tx, src.internalID)
	if err != nil {
		return nil, err
	}

	recordTypeID, err := recordTypeIDByCode(ctx, tx, vbilRecordTypeCode)
	if err != nil {
		return nil, fmt.Errorf("resolve VBIL record type: %w", err)
	}
	draftStatusID, err := statusIDByCode(ctx, tx, recordTypeID, draftStatusCode)
	if err != nil {
		return nil, fmt.Errorf("resolve DRFT status: %w", err)
	}

	cv := []colVal{
		{"record_type", recordTypeID, ""},
		{"vendor_bill_status", draftStatusID, ""},
		{"vendor_bill_vendor_id", src.vendorID, ""},
		{"vendor_bill_vendor_name", src.vendorName, ""},
		{"vendor_bill_purchase_order_id", src.internalID, ""},
		{"vendor_bill_reference_number", src.referenceNumber, ""},
		{"vendor_bill_date", "now", "::date"},
		{"vendor_bill_sales_tax_percent", src.salesTaxPercent, ""},
		{"vendor_bill_memo", src.memo, ""},
		{"vendor_bill_notes", src.notes, ""},
		{"vendor_bill_internal_notes", src.internalNotes, ""},
		{"vendor_bill_terms_conditions", src.termsConditions, ""},
		{"vendor_bill_owner_id", nullableInt(actorEmployeeID), ""},
		{"vendor_bill_payment_terms", src.paymentTermsID, ""},
		{"vendor_bill_currency", src.currencyID, ""},
		{"vendor_bill_custom_fields", src.customFields, ""},
		{"vendor_bill_created_by", nullableInt(actorEmployeeID), ""},
	}

	insertSQL, insertArgs := buildInsert("vendor_bill", cv, "vendor_bill_id, vendor_bill_uuid")
	var internalID int
	var newUUID string
	if err := tx.QueryRow(ctx, insertSQL, insertArgs...).Scan(&internalID, &newUUID); err != nil {
		if isForeignKeyViolation(err) {
			return nil, ClientError{Msg: "One of the referenced ids (payment terms or currency) does not exist."}
		}
		return nil, fmt.Errorf("insert converted vendor bill: %w", err)
	}

	number := FormatNumber(int64(internalID))
	if _, err := tx.Exec(ctx,
		`UPDATE vendor_bill SET vendor_bill_number = $1 WHERE vendor_bill_id = $2`, number, internalID); err != nil {
		return nil, fmt.Errorf("set vendor bill number: %w", err)
	}

	lineMap, lineMoneys, err := insertConvertedLines(ctx, tx, internalID, lines, actorEmployeeID)
	if err != nil {
		return nil, err
	}

	// Recompute and store header totals from the inserted lines (mirrors
	// Create -- the header row above was inserted with zero totals).
	// vendor_bill_balance_due starts equal to vendor_bill_grand_total: a
	// freshly converted bill is DRFT (not yet payable -- AD-7's
	// PayableStatuses gates APPV/PART/ODUE only), so amount_paid is 0 by
	// definition here.
	h := ComputeHeader(lineMoneys, 0, 0)
	if _, err := tx.Exec(ctx, `
		UPDATE vendor_bill SET
			vendor_bill_subtotal = $2, vendor_bill_discount_total = $3,
			vendor_bill_tax_total = $4, vendor_bill_grand_total = $5, vendor_bill_balance_due = $5
		WHERE vendor_bill_id = $1`, internalID, h.Subtotal, h.DiscountTotal, h.TaxTotal, h.GrandTotal); err != nil {
		return nil, fmt.Errorf("set converted vendor bill totals: %w", err)
	}

	writeHistory(ctx, tx, internalID, "create", nil, &draftStatusID, actorEmployeeID)

	snapshot := make(map[string]any, len(lineMap))
	for poItemUUID, vbItemUUID := range lineMap {
		snapshot[poItemUUID] = vbItemUUID
	}
	snapshotJSON, err := json.Marshal(snapshot)
	if err != nil {
		return nil, fmt.Errorf("marshal conversion snapshot: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO vendor_bill_conversion (purchase_order_id, vendor_bill_id, converted_by, snapshot)
		VALUES ($1, $2, $3, $4)`,
		src.internalID, internalID, nullableInt(actorEmployeeID), snapshotJSON); err != nil {
		return nil, fmt.Errorf("insert vendor_bill_conversion: %w", err)
	}

	// Mark the source purchase order's own history with the conversion.
	if _, err := tx.Exec(ctx, `
		INSERT INTO purchase_order_history (purchase_order_id, action, actor_employee_id, snapshot)
		VALUES ($1, 'convert', $2, jsonb_build_object('vendorBillId', $3::int, 'vendorBillUuid', $4::text))`,
		src.internalID, nullableInt(actorEmployeeID), internalID, newUUID); err != nil {
		return nil, fmt.Errorf("insert purchase order convert history: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit convert purchase order: %w", err)
	}
	return Get(ctx, pool, newUUID)
}
```

- [ ] **Step 2: Verify it compiles**

```bash
go build ./vendorbill/... && go vet ./vendorbill/...
```

Expected: both exit 0. This completes the `vendorbill` package — every store file now compiles together with no remaining forward references.

- [ ] **Step 3: Commit**

```bash
git add vendorbill/store_convert.go
git commit -m "feat(vendor-bill): add AD-8 conversion from Purchase Order"
```

---

## Task 12: Controller — Core CRUD + Lifecycle

**Files:**
- Create: `controllers/vendorbill.go`

**Interfaces:**
- Consumes: `vendorbill.ErrNotFound`, `vendorbill.ErrInvalidTransition`, `vendorbill.ErrApprovalRequired`, `vendorbill.ErrApprovalNotRequired`, `vendorbill.ErrNotApprover`, `vendorbill.IsClientError`, `vendorbill.CreateVendorBillInput`, `vendorbill.UpdateVendorBillInput`, `vendorbill.VendorBill`, `vendorbill.Get`, `vendorbill.Create`, `vendorbill.Update`, `vendorbill.SoftDelete`, `vendorbill.Search`, `vendorbill.Transition`, `vendorbill.Approve` (package `vendorbill`, all prior tasks); `authz.ResourceVendorBill`, `authz.Action*`, `authz.Scope*`, `authz.Check` (external `authz`); `middleware.GetUserFromContext` (external `middleware`); `tenancy.PoolFromContext` (external `tenancy`); `query.Request`, `query.InvalidFilterError` (external `query`); `recordInScope` (`controllers/scope.go`, pre-existing); `fail`, `writeJSON`, `logSecurityEvent`, `resolveEmployeeID` (pre-existing controller package helpers).
- Produces: `VendorBillOps` struct, `NewVendorBillOps() *VendorBillOps`, methods `List`, `Search`, `Create`, `Get`, `Update`, `Delete`, `Transition`, `Approve` — Task 14's `main.go` wiring calls these by name. Also produces unexported `authVB`, `authVBByUUID`, `vbFail` — Task 13's `vendorbill_audit.go`/`vendorbill_payments.go` call `h.authVBByUUID` and the package-level `vbFail`.

- [ ] **Step 1: Write `controllers/vendorbill.go`**

```go
// controllers/vendorbill.go
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
	"stonesuite-backend/tenancy"
	"stonesuite-backend/vendorbill"
)

// VendorBillOps handles the Vendor Bill endpoints: a dedicated relational
// module (header + line items + AD-6 approval + AD-7 settlement ledger),
// the accounts-payable mirror of Invoice -- a sibling of Purchase Order/Item
// Receipt, not served through the generic /api/tenant/crm/{workflowKey}
// JSONB router. Mirrors PurchaseOrderOps' auth/IDOR/error-mapping
// conventions.
//
// Routes:
//
//	GET    /api/tenant/vendor-bills                    — unfiltered list (cursor-paginated)
//	POST   /api/tenant/vendor-bills/search             — filter + sort + search + pagination
//	POST   /api/tenant/vendor-bills                    — create
//	GET    /api/tenant/vendor-bills/{uuid}             — get (+ items, + payments)
//	PATCH  /api/tenant/vendor-bills/{uuid}             — update (DRFT only)
//	DELETE /api/tenant/vendor-bills/{uuid}             — soft delete (DRFT/VOID only)
//	POST   /api/tenant/vendor-bills/{uuid}/transition  — status change
//	POST   /api/tenant/vendor-bills/{uuid}/approve     — approval sign-off
type VendorBillOps struct{}

// NewVendorBillOps constructs the handler group.
func NewVendorBillOps() *VendorBillOps { return &VendorBillOps{} }

// authVB resolves JWT + tenant pool + the vendor_bill:<action> RBAC grant
// for requests with no specific record yet (list/search/create).
func (h *VendorBillOps) authVB(w http.ResponseWriter, r *http.Request, action authz.Action) (*pgxpool.Pool, string, authz.Scope, bool) {
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
	decision, err := authz.Check(r.Context(), pool, payload.ID, authz.ResourceVendorBill, action)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, "", "", false
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", payload.ID, "resource", string(authz.ResourceVendorBill), "action", string(action))
		fail(w, http.StatusForbidden, "You do not have permission to "+string(action)+" vendor bills.")
		return nil, "", "", false
	}
	return pool, payload.ID, decision.Scope, true
}

// authVBByUUID resolves auth for a single-record action, then enforces the
// row-level IDOR guard. Denial returns 404 (not 403) so callers cannot
// enumerate ids outside their scope.
func (h *VendorBillOps) authVBByUUID(w http.ResponseWriter, r *http.Request, uuid string, action authz.Action) (*pgxpool.Pool, string, *vendorbill.VendorBill, bool) {
	pool, identityID, scope, ok := h.authVB(w, r, action)
	if !ok {
		return nil, "", nil, false
	}
	bill, err := vendorbill.Get(r.Context(), pool, uuid)
	if errors.Is(err, vendorbill.ErrNotFound) {
		fail(w, http.StatusNotFound, "Vendor bill not found.")
		return nil, "", nil, false
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load vendor bill.")
		return nil, "", nil, false
	}
	if scope != authz.ScopeAll {
		allowed, aerr := recordInScope(r.Context(), pool, scope, identityID, bill.OwnerUserID)
		if aerr != nil {
			fail(w, http.StatusInternalServerError, "Permission check failed.")
			return nil, "", nil, false
		}
		if !allowed {
			logSecurityEvent(r, "idor_denied",
				"identity", identityID, "record", uuid, "resource", string(authz.ResourceVendorBill),
				"action", string(action), "scope", string(scope))
			fail(w, http.StatusNotFound, "Vendor bill not found.")
			return nil, "", nil, false
		}
	}
	return pool, identityID, bill, true
}

// vbFail maps a store error to an HTTP response.
func vbFail(w http.ResponseWriter, err error, serverMsg string) {
	switch {
	case errors.Is(err, vendorbill.ErrNotFound):
		fail(w, http.StatusNotFound, "Vendor bill not found.")
	case errors.Is(err, vendorbill.ErrInvalidTransition),
		errors.Is(err, vendorbill.ErrApprovalRequired),
		errors.Is(err, vendorbill.ErrApprovalNotRequired):
		fail(w, http.StatusConflict, err.Error())
	case errors.Is(err, vendorbill.ErrNotApprover):
		fail(w, http.StatusForbidden, err.Error())
	case vendorbill.IsClientError(err):
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

// List GET /api/tenant/vendor-bills
func (h *VendorBillOps) List(w http.ResponseWriter, r *http.Request) {
	pool, identityID, scope, ok := h.authVB(w, r, authz.ActionRead)
	if !ok {
		return
	}
	req := query.Request{Cursor: r.URL.Query().Get("cursor"), Search: r.URL.Query().Get("search")}
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil {
		req.Limit = n
	}
	h.search(w, r, pool, identityID, scope, req)
}

// Search POST /api/tenant/vendor-bills/search
func (h *VendorBillOps) Search(w http.ResponseWriter, r *http.Request) {
	pool, identityID, scope, ok := h.authVB(w, r, authz.ActionRead)
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

func (h *VendorBillOps) search(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, identityID string, scope authz.Scope, req query.Request) {
	page, err := vendorbill.Search(r.Context(), pool, string(scope), identityID, req)
	if err != nil {
		vbFail(w, err, "Failed to search vendor bills.")
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

// Create POST /api/tenant/vendor-bills
func (h *VendorBillOps) Create(w http.ResponseWriter, r *http.Request) {
	pool, identityID, _, ok := h.authVB(w, r, authz.ActionCreate)
	if !ok {
		return
	}
	var in vendorbill.CreateVendorBillInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	bill, err := vendorbill.Create(r.Context(), pool, in, resolveEmployeeID(r, identityID))
	if err != nil {
		vbFail(w, err, "Failed to create vendor bill.")
		return
	}
	auditVB(r, pool, identityID, "create", bill.ID, nil, bill)
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "vendorBill": bill})
}

// ---- single record ------------------------------------------------------------

// Get GET /api/tenant/vendor-bills/{uuid}
func (h *VendorBillOps) Get(w http.ResponseWriter, r *http.Request) {
	_, _, bill, ok := h.authVBByUUID(w, r, r.PathValue("uuid"), authz.ActionRead)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "vendorBill": bill})
}

// Update PATCH /api/tenant/vendor-bills/{uuid}
func (h *VendorBillOps) Update(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, before, ok := h.authVBByUUID(w, r, uuid, authz.ActionUpdate)
	if !ok {
		return
	}
	var in vendorbill.UpdateVendorBillInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	after, err := vendorbill.Update(r.Context(), pool, uuid, in, resolveEmployeeID(r, identityID))
	if err != nil {
		vbFail(w, err, "Failed to update vendor bill.")
		return
	}
	auditVB(r, pool, identityID, "update", uuid, before, after)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "vendorBill": after})
}

// Delete DELETE /api/tenant/vendor-bills/{uuid}
func (h *VendorBillOps) Delete(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, before, ok := h.authVBByUUID(w, r, uuid, authz.ActionDelete)
	if !ok {
		return
	}
	if err := vendorbill.SoftDelete(r.Context(), pool, uuid, resolveEmployeeID(r, identityID)); err != nil {
		vbFail(w, err, "Failed to delete vendor bill.")
		return
	}
	auditVBDelete(r, pool, identityID, uuid, before)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "message": "Vendor bill deleted."})
}

// Transition POST /api/tenant/vendor-bills/{uuid}/transition  body {"toStatusCode":"..."}
func (h *VendorBillOps) Transition(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, _, ok := h.authVBByUUID(w, r, uuid, authz.ActionTransition)
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
	bill, err := vendorbill.Transition(r.Context(), pool, uuid, req.ToStatusCode, resolveEmployeeID(r, identityID))
	if err != nil {
		vbFail(w, err, "Failed to apply transition.")
		return
	}
	auditVB(r, pool, identityID, "transition", uuid, nil, bill)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "vendorBill": bill})
}

// Approve POST /api/tenant/vendor-bills/{uuid}/approve
func (h *VendorBillOps) Approve(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, _, ok := h.authVBByUUID(w, r, uuid, authz.ActionTransition)
	if !ok {
		return
	}
	bill, err := vendorbill.Approve(r.Context(), pool, uuid, resolveEmployeeID(r, identityID))
	if err != nil {
		if errors.Is(err, vendorbill.ErrNotApprover) {
			logSecurityEvent(r, "approval_denied", "identity", identityID, "record", uuid)
		}
		vbFail(w, err, "Failed to approve vendor bill.")
		return
	}
	auditVB(r, pool, identityID, "approve", uuid, nil, bill)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "vendorBill": bill})
}
```

Note: `Create`/`Update`/`Delete`/`Transition`/`Approve` call `auditVB`/`auditVBDelete`, defined in **Task 13's** `controllers/vendorbill_audit.go` — this is a forward reference within the same `controllers` package (Go compiles a package as a whole, so this is fine as long as Task 13 lands before the package is built; unlike the `vendorbill` package's earlier tasks, there is no intermediate state where `controllers` is built alone).

- [ ] **Step 2: Commit (package will not build standalone until Task 13 lands — see note above)**

```bash
git add controllers/vendorbill.go
git commit -m "feat(vendor-bill): add core CRUD + lifecycle controller (pairs with Task 13's audit helpers)"
```

---

## Task 13: Controller — Audit, Payments, Convert

**Files:**
- Create: `controllers/vendorbill_audit.go`
- Create: `controllers/vendorbill_payments.go`
- Create: `controllers/purchaseorder_convert.go`

**Interfaces:**
- Consumes: `VendorBillOps`, `h.authVBByUUID`, `vbFail` (`controllers/vendorbill.go`, Task 12); `vendorbill.VendorBill`, `vendorbill.RecordPaymentInput`, `vendorbill.RecordPayment`, `vendorbill.RemovePayment`, `vendorbill.ListPayments`, `vendorbill.ConvertFromPurchaseOrder`, `vendorbill.ErrPurchaseOrderNotFound`, `vendorbill.IsClientError` (package `vendorbill`); `PurchaseOrderOps`, `h.authPOByUUID` (`controllers/purchaseorder.go`, pre-existing); `workflow.UserIDByIdentity`, `workflow.LogAuditFull` (external `workflow`); `loadAuditEntries`, `appVersion`, `clientIP`, `fail`, `writeJSON`, `logSecurityEvent`, `resolveEmployeeID` (pre-existing controller package helpers).
- Produces: `auditVB`, `auditVBDelete` (package-private, consumed by Task 12's `vendorbill.go`); `VendorBillOps.Audit`, `VendorBillOps.RecordPayment`, `VendorBillOps.Payments`, `VendorBillOps.RemovePayment`; `PurchaseOrderOps.ConvertToBill` — Task 14's `main.go` wiring calls all of these by name.

- [ ] **Step 1: Write `controllers/vendorbill_audit.go`**

```go
// controllers/vendorbill_audit.go
package controllers

import (
	"log"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/vendorbill"
	"stonesuite-backend/workflow"
)

// vbSnapshot flattens a vendor bill into a JSON-able map for the audit
// trail, mirroring poSnapshot for the Vendor Bill shape.
func vbSnapshot(b *vendorbill.VendorBill) map[string]any {
	if b == nil {
		return nil
	}
	return map[string]any{
		"id":               b.ID,
		"vendorBillNumber": b.Number,
		"status":           b.StatusName,
		"vendorId":         b.Vendor.ID,
		"grandTotal":       b.GrandTotal,
		"balanceDue":       b.BalanceDue,
	}
}

// auditVB records a Vendor Bill mutation in the unified audit_logs table.
// Best-effort: failures are logged, never returned, mirroring auditPO.
func auditVB(r *http.Request, pool *pgxpool.Pool, identityID, action, recordID string, oldBill, newBill *vendorbill.VendorBill) {
	ctx := r.Context()
	actorUserID, _ := workflow.UserIDByIdentity(ctx, pool, identityID)
	if err := workflow.LogAuditFull(ctx, pool, actorUserID, action, "vendor_bill", recordID, "vendor_bill",
		vbSnapshot(oldBill), vbSnapshot(newBill), nil,
		clientIP(r), r.Header.Get("X-Session-Id"), appVersion); err != nil {
		log.Printf("vendorbill: audit %s %s: %v", action, recordID, err)
	}
}

// auditVBDelete is the delete-specific variant, mirroring auditPODelete.
func auditVBDelete(r *http.Request, pool *pgxpool.Pool, identityID, recordID string, oldBill *vendorbill.VendorBill) {
	ctx := r.Context()
	actorUserID, _ := workflow.UserIDByIdentity(ctx, pool, identityID)
	if err := workflow.LogAuditFull(ctx, pool, actorUserID, "delete", "vendor_bill", recordID, "vendor_bill",
		vbSnapshot(oldBill), nil, nil,
		clientIP(r), r.Header.Get("X-Session-Id"), appVersion); err != nil {
		log.Printf("vendorbill: audit delete %s: %v", recordID, err)
	}
}

// Audit GET /api/tenant/vendor-bills/{uuid}/audit
// Returns the unified audit trail for a single vendor bill (most recent first).
func (h *VendorBillOps) Audit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, _, _, ok := h.authVBByUUID(w, r, id, authz.ActionRead)
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

- [ ] **Step 2: Write `controllers/vendorbill_payments.go`**

```go
// controllers/vendorbill_payments.go
package controllers

import (
	"encoding/json"
	"net/http"

	"stonesuite-backend/authz"
	"stonesuite-backend/vendorbill"
)

// RecordPayment POST /api/tenant/vendor-bills/{uuid}/payment
// body {"amount":500.00,"methodId":3,"referenceNumber":"CHK-1042","memo":"","paidAt":"2026-08-10"}
// Records one settlement against the bill (AD-7); recomputes amount_paid/
// balance_due and re-derives status. RBAC: vendor_bill:update (a payment
// mutates the bill's own AP rollup).
func (h *VendorBillOps) RecordPayment(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, before, ok := h.authVBByUUID(w, r, uuid, authz.ActionUpdate)
	if !ok {
		return
	}
	var in vendorbill.RecordPaymentInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	after, err := vendorbill.RecordPayment(r.Context(), pool, uuid, in, resolveEmployeeID(r, identityID))
	if err != nil {
		vbFail(w, err, "Failed to record payment.")
		return
	}
	auditVB(r, pool, identityID, "record_payment", uuid, before, after)
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "vendorBill": after})
}

// Payments GET /api/tenant/vendor-bills/{uuid}/payments
// AP reconciliation view of the bill's live settlement ledger. RBAC: vendor_bill:read.
func (h *VendorBillOps) Payments(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, _, _, ok := h.authVBByUUID(w, r, uuid, authz.ActionRead)
	if !ok {
		return
	}
	payments, err := vendorbill.ListPayments(r.Context(), pool, uuid)
	if err != nil {
		vbFail(w, err, "Failed to load payments.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "recordId": uuid, "payments": payments})
}

// RemovePayment DELETE /api/tenant/vendor-bills/{uuid}/payments/{paymentId}
// Soft-deletes one ledger entry (the "unapply") and recomputes the AP
// rollup. RBAC: vendor_bill:update.
func (h *VendorBillOps) RemovePayment(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	paymentID := r.PathValue("paymentId")
	pool, identityID, before, ok := h.authVBByUUID(w, r, uuid, authz.ActionUpdate)
	if !ok {
		return
	}
	after, err := vendorbill.RemovePayment(r.Context(), pool, uuid, paymentID, resolveEmployeeID(r, identityID))
	if err != nil {
		vbFail(w, err, "Failed to remove payment.")
		return
	}
	auditVB(r, pool, identityID, "unapply_payment", uuid, before, after)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "vendorBill": after})
}
```

- [ ] **Step 3: Write `controllers/purchaseorder_convert.go`**

```go
// controllers/purchaseorder_convert.go
package controllers

import (
	"errors"
	"net/http"

	"stonesuite-backend/authz"
	"stonesuite-backend/vendorbill"
)

// ConvertToBill POST /api/tenant/purchase-orders/{uuid}/convert-to-bill
//
// Creates a Vendor Bill as a snapshot copy of the live, received purchase
// order (AD-8 of the Vendor Bill design). Requires purchase_order:read on
// the source (IDOR-guarded, mirrors every other single-record purchase
// order action) and vendor_bill:create on the target -- a caller who can
// view a purchase order but cannot create vendor bills must not be able to
// spawn one via convert. Unlike RequisitionOps.Convert, this is NOT
// idempotent: a purchase order may be billed more than once (vendors
// routinely invoice in installments), so every call creates a new bill.
// Mirrors controllers/requisition_convert.go's Convert.
func (h *PurchaseOrderOps) ConvertToBill(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, _, ok := h.authPOByUUID(w, r, uuid, authz.ActionRead)
	if !ok {
		return
	}

	decision, err := authz.Check(r.Context(), pool, identityID, authz.ResourceVendorBill, authz.ActionCreate)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", identityID, "resource", string(authz.ResourceVendorBill), "action", string(authz.ActionCreate),
			"context", "purchase_order_convert_to_bill", "source_record", uuid)
		fail(w, http.StatusForbidden, "You do not have permission to create vendor bills.")
		return
	}

	bill, err := vendorbill.ConvertFromPurchaseOrder(r.Context(), pool, uuid, resolveEmployeeID(r, identityID))
	if err != nil {
		switch {
		case errors.Is(err, vendorbill.ErrPurchaseOrderNotFound):
			fail(w, http.StatusNotFound, "Purchase order not found.")
		default:
			if vendorbill.IsClientError(err) {
				fail(w, http.StatusBadRequest, err.Error())
				return
			}
			fail(w, http.StatusInternalServerError, "Failed to convert purchase order to vendor bill.")
		}
		return
	}

	auditVB(r, pool, identityID, "convert", bill.ID, nil, bill)
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "vendorBill": bill, "created": true})
}
```

- [ ] **Step 4: Verify the whole backend compiles**

```bash
go build ./... && go vet ./...
```

Expected: both exit 0. This is the first point where `controllers/vendorbill.go` (Task 12) and its audit/payment/convert dependents (this task) exist together — every forward reference from Task 12 is now resolved.

- [ ] **Step 5: Run the full test suite**

```bash
go test ./...
```

Expected: all PASS (dbtest-tagged tests are excluded at compile time and do not run here — Task 15 covers those).

- [ ] **Step 6: Commit**

```bash
git add controllers/vendorbill_audit.go controllers/vendorbill_payments.go controllers/purchaseorder_convert.go
git commit -m "feat(vendor-bill): add audit trail, settlement endpoints, and PO-to-bill conversion controller"
```

---

## Task 14: Wire Routes + Attachments Branch

**Files:**
- Modify: `main.go` (add Vendor Bill route block; add `ConvertToBill` route to the existing Purchase Order block)
- Modify: `workflow/attachments.go` (AD-11: one new branch in `ResolveRecordAccess`)

**Interfaces:**
- Consumes: `controllers.NewVendorBillOps`, `(*VendorBillOps){List,Search,Create,Get,Update,Delete,Transition,Approve,RecordPayment,Payments,RemovePayment,Audit}` (Tasks 12-13); `(*PurchaseOrderOps).ConvertToBill` (Task 13); `tenantChain` (pre-existing `main.go` helper).
- Produces: 13 live HTTP routes under `/api/tenant/vendor-bills*` + 1 under `/api/tenant/purchase-orders/{uuid}/convert-to-bill`; a fifth `RecordAccessInfo` branch in `workflow.ResolveRecordAccess` so the existing generic attachment endpoints serve vendor bill UUIDs.

- [ ] **Step 1: Add the `ConvertToBill` route to the existing Purchase Order block**

In `main.go`, find this block (it currently ends with the `poOps.Audit` line):

```go
		mux.Handle("POST /api/tenant/purchase-orders/{uuid}/approve", tenantChain(poOps.Approve))
		mux.Handle("GET /api/tenant/purchase-orders/{uuid}/audit", tenantChain(poOps.Audit))
```

Change it to:

```go
		mux.Handle("POST /api/tenant/purchase-orders/{uuid}/approve", tenantChain(poOps.Approve))
		mux.Handle("POST /api/tenant/purchase-orders/{uuid}/convert-to-bill", tenantChain(poOps.ConvertToBill))
		mux.Handle("GET /api/tenant/purchase-orders/{uuid}/audit", tenantChain(poOps.Audit))
```

- [ ] **Step 2: Add the Vendor Bill route block**

Immediately after the Item Receipt block's last line (`mux.Handle("GET /api/tenant/purchase-orders/{uuid}/receipts", tenantChain(irOps.ForPurchaseOrder))`) and before the `// Invoice:` comment, insert:

```go

		// Vendor Bill: dedicated v2 relational module (header + line items +
		// AD-6 approval + AD-7 settlement ledger), the accounts-payable mirror
		// of Invoice — the third Purchases document module, a sibling of
		// Purchase Order/Item Receipt. Not served through the generic JSONB
		// router. ConvertToBill (PO -> Vendor Bill) is registered on the
		// Purchase Order block above, not here — the route lives on the
		// source, mirroring Requisition -> Purchase Order.
		vbOps := controllers.NewVendorBillOps()
		mux.Handle("GET /api/tenant/vendor-bills", tenantChain(vbOps.List))
		mux.Handle("POST /api/tenant/vendor-bills/search", tenantChain(vbOps.Search))
		mux.Handle("POST /api/tenant/vendor-bills", tenantChain(vbOps.Create))
		mux.Handle("GET /api/tenant/vendor-bills/{uuid}", tenantChain(vbOps.Get))
		mux.Handle("PATCH /api/tenant/vendor-bills/{uuid}", tenantChain(vbOps.Update))
		mux.Handle("DELETE /api/tenant/vendor-bills/{uuid}", tenantChain(vbOps.Delete))
		mux.Handle("POST /api/tenant/vendor-bills/{uuid}/transition", tenantChain(vbOps.Transition))
		mux.Handle("POST /api/tenant/vendor-bills/{uuid}/approve", tenantChain(vbOps.Approve))
		mux.Handle("POST /api/tenant/vendor-bills/{uuid}/payment", tenantChain(vbOps.RecordPayment))
		mux.Handle("GET /api/tenant/vendor-bills/{uuid}/payments", tenantChain(vbOps.Payments))
		mux.Handle("DELETE /api/tenant/vendor-bills/{uuid}/payments/{paymentId}", tenantChain(vbOps.RemovePayment))
		mux.Handle("GET /api/tenant/vendor-bills/{uuid}/audit", tenantChain(vbOps.Audit))
```

- [ ] **Step 3: Add the AD-11 attachments branch**

In `workflow/attachments.go`, find `ResolveRecordAccess`'s `cash_transfer` branch (it currently ends with):

```go
	if err == nil {
		return RecordAccessInfo{WorkflowKey: "cash_transfer", OwnerUserID: ctOwnerUserID}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return RecordAccessInfo{}, fmt.Errorf("lookup cash transfer: %w", err)
	}

	return RecordAccessInfo{}, ErrRecordNotFound
}
```

Change it to:

```go
	if err == nil {
		return RecordAccessInfo{WorkflowKey: "cash_transfer", OwnerUserID: ctOwnerUserID}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return RecordAccessInfo{}, fmt.Errorf("lookup cash transfer: %w", err)
	}

	// vendor_bill: dedicated relational module (Vendor Bill spec AD-11), owner
	// resolved the same way (employee -> users.id); no team column. This is
	// the first document module wired into the generic attachment mechanism
	// -- attachments were explicitly requested for Vendor Bill, and the
	// mechanism (workflow_record_attachments, FK deliberately dropped in
	// migration 023) already supports any record UUID regardless of table.
	var vbOwnerUserID string
	err = q.QueryRow(ctx, `
		SELECT COALESCE(u.id::text,'')
		FROM vendor_bill vb
		LEFT JOIN employee e ON e.employee_id = vb.vendor_bill_owner_id
		LEFT JOIN users u ON u.id = e.employee_user_id
		WHERE vb.vendor_bill_uuid = $1::uuid AND vb.vendor_bill_deleted_at IS NULL`,
		recordID).Scan(&vbOwnerUserID)
	if err == nil {
		return RecordAccessInfo{WorkflowKey: "vendor_bill", OwnerUserID: vbOwnerUserID}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return RecordAccessInfo{}, fmt.Errorf("lookup vendor bill: %w", err)
	}

	return RecordAccessInfo{}, ErrRecordNotFound
}
```

No change is needed to `controllers/crm.go`'s `resourceForKey` — it already maps `"vendor_bill"` to `authz.ResourceVendorBill`.

- [ ] **Step 4: Verify the whole backend compiles and vets**

```bash
go build ./... && go vet ./...
```

Expected: both exit 0.

- [ ] **Step 5: Run the full test suite**

```bash
go test ./...
```

Expected: all PASS, including `controllers/rbac_catalog_drift_test.go` (asserts every `resourceForKey`-mapped resource has all 5 CRM actions in the catalog — `vendor_bill` already did before this plan started).

- [ ] **Step 6: Commit**

```bash
git add main.go workflow/attachments.go
git commit -m "feat(vendor-bill): wire routes and enable generic attachments (AD-11)"
```

---

## Task 15: DB-Backed Tests + Full Verification

**Files:**
- Create: `vendorbill/store_test.go`

**Interfaces:**
- Consumes: every exported function in the `vendorbill` package (Tasks 4-11); `os.Getenv`, `pgxpool.New` (stdlib/external, for the test-only DB connection).
- Produces: `TestCreateAndGet`, `TestUpdateReplacesLines`, `TestTransitionAndApprovalGate`, `TestRecordPaymentDerivesStatus`, `TestConvertFromPurchaseOrder` — dbtest-only, excluded from `go test ./...`, run only via `go test -tags dbtest ./vendorbill/...` against a real Postgres (CI's `schema-apply` job does this on every PR).

- [ ] **Step 1: Write `vendorbill/store_test.go`**

```go
//go:build dbtest

package vendorbill

import (
	"context"
	"os"
	"testing"

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

// seedVendor inserts a minimal live vendor and returns its uuid.
func seedVendor(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	ctx := context.Background()
	var typeID, statusID, uuid string
	if err := pool.QueryRow(ctx, `SELECT record_type_id::text FROM lkp_record_type WHERE record_type_code = 'VNDR'`).Scan(&typeID); err != nil {
		t.Fatalf("resolve VNDR type: %v", err)
	}
	if err := pool.QueryRow(ctx, `SELECT record_status_id::text FROM lkp_record_status WHERE record_status_record_type = $1 AND record_status_code = 'ACT_'`, typeID).Scan(&statusID); err != nil {
		t.Fatalf("resolve vendor ACT_ status: %v", err)
	}
	if err := pool.QueryRow(ctx, `
		INSERT INTO vendor (record_type, vendor_status, vendor_type, vendor_legal_name, vendor_created_by)
		VALUES ($1, $2, 'Organization', 'Test Vendor Inc', 1)
		RETURNING vendor_uuid::text`, typeID, statusID).Scan(&uuid); err != nil {
		t.Fatalf("seed vendor: %v", err)
	}
	return uuid
}

func TestCreateAndGet(t *testing.T) {
	pool := testPool(t)
	vendorUUID := seedVendor(t, pool)

	in := CreateVendorBillInput{
		VendorUUID: vendorUUID,
		vendorBillFields: vendorBillFields{
			VendorInvoiceNumber: "VND-INV-001",
			BillDate:            "2026-08-01",
			SalesTaxPercent:     8.25,
			Items: []LineInput{
				{LineNumber: 1, Description: "Consulting hours", Quantity: 10, UnitPrice: 100},
			},
		},
	}
	bill, err := Create(context.Background(), pool, in, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if bill.StatusCode != "DRFT" {
		t.Errorf("new bill status = %q, want DRFT", bill.StatusCode)
	}
	if bill.GrandTotal != 1082.50 {
		t.Errorf("GrandTotal = %v, want 1082.50", bill.GrandTotal)
	}
	if len(bill.Items) != 1 {
		t.Fatalf("Items len = %d, want 1", len(bill.Items))
	}

	got, err := Get(context.Background(), pool, bill.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Number != bill.Number {
		t.Errorf("Get().Number = %q, want %q", got.Number, bill.Number)
	}
}

func TestUpdateReplacesLines(t *testing.T) {
	pool := testPool(t)
	vendorUUID := seedVendor(t, pool)
	ctx := context.Background()

	bill, err := Create(ctx, pool, CreateVendorBillInput{
		VendorUUID: vendorUUID,
		vendorBillFields: vendorBillFields{
			BillDate: "2026-08-01",
			Items:    []LineInput{{LineNumber: 1, Description: "Line A", Quantity: 1, UnitPrice: 50}},
		},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	updated, err := Update(ctx, pool, bill.ID, UpdateVendorBillInput{
		vendorBillFields: vendorBillFields{
			BillDate: "2026-08-01",
			Items:    []LineInput{{LineNumber: 1, Description: "Line B", Quantity: 2, UnitPrice: 75}},
		},
	}, 1)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if len(updated.Items) != 1 || updated.Items[0].Description != "Line B" {
		t.Fatalf("Update did not replace lines: %+v", updated.Items)
	}
	if updated.GrandTotal != 150 {
		t.Errorf("GrandTotal = %v, want 150", updated.GrandTotal)
	}
}

func TestTransitionAndApprovalGate(t *testing.T) {
	pool := testPool(t)
	vendorUUID := seedVendor(t, pool)
	ctx := context.Background()

	bill, err := Create(ctx, pool, CreateVendorBillInput{
		VendorUUID: vendorUUID,
		vendorBillFields: vendorBillFields{
			BillDate: "2026-08-01",
			Items:    []LineInput{{LineNumber: 1, Description: "Line A", Quantity: 1, UnitPrice: 100}},
		},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	moved, err := Transition(ctx, pool, bill.ID, "PAPV", 1)
	if err != nil {
		t.Fatalf("Transition to PAPV: %v", err)
	}
	if moved.StatusCode != "PAPV" {
		t.Fatalf("status = %q, want PAPV", moved.StatusCode)
	}

	// No approvers configured for this tenant's VBIL/PAPV -> gate is open,
	// so APPV should succeed with no Approve call first.
	approved, err := Transition(ctx, pool, bill.ID, "APPV", 1)
	if err != nil {
		t.Fatalf("Transition to APPV with no configured approvers: %v", err)
	}
	if approved.StatusCode != "APPV" {
		t.Fatalf("status = %q, want APPV", approved.StatusCode)
	}

	if _, err := Transition(ctx, pool, bill.ID, "DRFT", 1); err != ErrInvalidTransition {
		t.Errorf("Transition APPV->DRFT = %v, want ErrInvalidTransition", err)
	}
}

func TestRecordPaymentDerivesStatus(t *testing.T) {
	pool := testPool(t)
	vendorUUID := seedVendor(t, pool)
	ctx := context.Background()

	bill, err := Create(ctx, pool, CreateVendorBillInput{
		VendorUUID: vendorUUID,
		vendorBillFields: vendorBillFields{
			BillDate: "2026-08-01",
			Items:    []LineInput{{LineNumber: 1, Description: "Line A", Quantity: 1, UnitPrice: 100}},
		},
	}, 1)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := Transition(ctx, pool, bill.ID, "PAPV", 1); err != nil {
		t.Fatalf("Transition to PAPV: %v", err)
	}
	if _, err := Transition(ctx, pool, bill.ID, "APPV", 1); err != nil {
		t.Fatalf("Transition to APPV: %v", err)
	}

	partial, err := RecordPayment(ctx, pool, bill.ID, RecordPaymentInput{Amount: 40, PaidAt: "2026-08-05"}, 1)
	if err != nil {
		t.Fatalf("RecordPayment (partial): %v", err)
	}
	if partial.StatusCode != "PART" {
		t.Errorf("status after partial payment = %q, want PART", partial.StatusCode)
	}
	if partial.BalanceDue != 60 {
		t.Errorf("BalanceDue = %v, want 60", partial.BalanceDue)
	}

	overpay, err := RecordPayment(ctx, pool, bill.ID, RecordPaymentInput{Amount: 1000, PaidAt: "2026-08-06"}, 1)
	if err == nil {
		t.Fatal("RecordPayment should reject an amount exceeding the outstanding balance")
	}
	if !IsClientError(err) {
		t.Errorf("overpay error = %v, want ClientError", err)
	}
	_ = overpay

	full, err := RecordPayment(ctx, pool, bill.ID, RecordPaymentInput{Amount: 60, PaidAt: "2026-08-07"}, 1)
	if err != nil {
		t.Fatalf("RecordPayment (final): %v", err)
	}
	if full.StatusCode != "PAID" {
		t.Errorf("status after full payment = %q, want PAID", full.StatusCode)
	}
	if full.BalanceDue != 0 {
		t.Errorf("BalanceDue = %v, want 0", full.BalanceDue)
	}
}

func TestConvertFromPurchaseOrder(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()

	var poUUID string
	err := pool.QueryRow(ctx, `
		SELECT po.purchase_order_uuid::text FROM purchase_order po
		JOIN lkp_record_status rs ON rs.record_status_id = po.purchase_order_status
		WHERE rs.record_status_code IN ('RCVD','CLSD') AND po.purchase_order_deleted_at IS NULL
		LIMIT 1`).Scan(&poUUID)
	if err != nil {
		t.Skip("no RCVD/CLSD purchase order fixture available; seed one to exercise this path")
	}

	bill, err := ConvertFromPurchaseOrder(ctx, pool, poUUID, 1)
	if err != nil {
		t.Fatalf("ConvertFromPurchaseOrder: %v", err)
	}
	if bill.PurchaseOrder == nil || bill.PurchaseOrder.ID != poUUID {
		t.Errorf("converted bill.PurchaseOrder = %+v, want ID %q", bill.PurchaseOrder, poUUID)
	}
	if len(bill.Items) == 0 {
		t.Error("converted bill has no lines")
	}

	// A second conversion of the same PO must succeed and create a DIFFERENT
	// bill (AD-8: installment billing, no idempotent short-circuit).
	second, err := ConvertFromPurchaseOrder(ctx, pool, poUUID, 1)
	if err != nil {
		t.Fatalf("second ConvertFromPurchaseOrder: %v", err)
	}
	if second.ID == bill.ID {
		t.Error("second conversion returned the same bill id; expected a new bill per AD-8")
	}
}
```

- [ ] **Step 2: Verify the package still compiles with the test file present (dbtest tag excluded by default)**

```bash
go build ./... && go vet ./...
```

Expected: both exit 0 — the `dbtest` build tag means this file is invisible to a normal build.

- [ ] **Step 3: Stand up a real Postgres and apply the schema twice**

```bash
docker run --rm -d -p 5433:5432 -e POSTGRES_PASSWORD=pg --name ss-dev-final pgvector/pgvector:pg16
psql "postgres://postgres:pg@localhost:5433/postgres" --single-transaction -v ON_ERROR_STOP=1 -f database/migrations/tenant/schema.sql
psql "postgres://postgres:pg@localhost:5433/postgres" --single-transaction -v ON_ERROR_STOP=1 -f database/migrations/tenant/schema.sql
```

Expected: both runs exit 0 (the schema is idempotent end-to-end, not just for this module's own tables).

- [ ] **Step 4: Run the dbtest suite**

```bash
TEST_DATABASE_URL="postgres://postgres:pg@localhost:5433/postgres" go test -tags dbtest ./vendorbill/... -v
```

Expected: `TestCreateAndGet`, `TestUpdateReplacesLines`, `TestTransitionAndApprovalGate`, `TestRecordPaymentDerivesStatus` PASS; `TestConvertFromPurchaseOrder` PASSes if a RCVD/CLSD purchase order fixture exists in the test database, else SKIPs cleanly (no PO module fixtures are seeded by this plan — seeding one is a fair follow-up, not a blocker).

- [ ] **Step 5: Tear down the test database**

```bash
docker stop ss-dev-final
```

- [ ] **Step 6: Run the complete non-DB verification one final time**

```bash
go build ./... && go vet ./... && go test ./...
```

Expected: all three exit 0 with no failures — this is the same command CI runs on every PR.

- [ ] **Step 7: Commit**

```bash
git add vendorbill/store_test.go
git commit -m "test(vendor-bill): add DB-backed store tests"
```

- [ ] **Step 8: Run the review agents**

Dispatch, in order: `module-drift-checker` (copy-paste leftovers, missing auth/scope/logging against the corrected skeleton), then `tenancy-security-reviewer` (multi-tenancy/RBAC/IDOR), then `migration-auditor` (idempotency/transaction-safety/non-destructiveness of the `schema.sql` diff). Fix any findings, committing each fix separately with a `fix(vendor-bill): ...` message.

---

## Self-Review

**Spec coverage** — every section of `docs/superpowers/specs/2026-08-10-vendor-bill-module-design.md` maps to a task:
- §2 AD-1..AD-15 → Tasks 1 (schema), 5-6 (AD-2/AD-3/AD-4/AD-12), 3+9 (AD-5/AD-6), 10 (AD-7/AD-15), 11 (AD-8), 2 (AD-9/AD-10), 14 (AD-11), 1 (AD-13), AD-14 (GL posting) is explicitly out of scope and no task implements it — correct, matches the spec.
- §3 schema (7 tables) → Task 1, verbatim.
- §4 package layout → Tasks 2-3, 4-11, one task per file group, matching the spec's file table exactly.
- §5 controllers/routes → Tasks 12-13-14, every route in the spec's table is wired.
- §6 RBAC → confirmed as "no change" in Task 14's notes; matches spec §6 exactly.
- §7 shared-file edits → Task 14 covers all three (`schema.sql` done in Task 1, `main.go`, `workflow/attachments.go`).
- §9 security invariants → threaded through every controller task (404-not-403, `logSecurityEvent`, scope ANDing, parameterization, nil-guard, transactions).
- §10 deliberate divergences → each one has a corresponding code comment at its point of implementation (Task 11's file doc comment, Task 14's attachments comment).

**Placeholder scan** — no TBD/TODO/"add appropriate handling" in any step; every step shows real, complete code.

**Type consistency** — traced across every task: `VendorBill`, `CreateVendorBillInput`, `UpdateVendorBillInput`, `LineInput`, `Line`, `BillPayment`, `Page` (Task 2) are the only types referenced by name in every later task, with no renames. `resolvedLine` (Task 5) is used identically in Tasks 5-6. `Locked`/`RecomputeBalance`/`DeriveStatus` (Task 10) match their call sites in Task 10's own `store_payment.go`. Function signatures for `Create`, `Update`, `SoftDelete`, `Search`, `Transition`, `Approve`, `RecordPayment`, `RemovePayment`, `ListPayments`, `ConvertFromPurchaseOrder` are declared once (Tasks 5-11) and called with matching argument order/types in Task 12-13's controllers.

**Build-state check** — the one genuine forward reference (`controllers/vendorbill.go` calling `auditVB`/`auditVBDelete`, defined one task later) is explicitly called out in Task 12's closing note, since Go compiles a package as a whole and `go build ./controllers/...` only succeeds once both files exist — this is not a defect, just a same-package split across two tasks. Every *cross-package* boundary (`vendorbill` package itself) was re-sequenced during drafting specifically to keep `go build ./vendorbill/...` green at the end of every single task (Task 8 moved before Task 9 for exactly this reason).

---

## Execution Handoff

Plan complete and saved to `docs/superpowers/plans/2026-08-10-vendor-bill-module.md`. Two execution options:

**1. Subagent-Driven** — a fresh subagent per task, review between tasks, fast iteration on independent pieces.

**2. Inline Execution** — execute tasks in this session using `executing-plans`, batch execution with checkpoints.

**Which approach?**
