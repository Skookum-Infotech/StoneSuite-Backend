# Payments Module Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. This plan assumes the Invoice module (`invoice/`, `controllers/invoice*.go`, `invoice`/`invoice_item`/`invoice_history` tables) is **already implemented and merged on this branch** — Payments FKs into `invoice` and its legacy payment endpoint is rewired here. Verify with `ls invoice/` and `grep -n "CREATE TABLE IF NOT EXISTS invoice " database/migrations/tenant/schema.sql` before starting; if missing, stop.

**Goal:** Add a production-grade Payment module (header + invoice-application ledger + status lifecycle + listing/search) that makes `payment_application` the source of truth for AR balances, replacing the direct-write `invoice.RecordPayment` path.

**Spec (authoritative):** `docs/superpowers/specs/2026-07-13-payments-module-design.md` — cite section numbers (e.g. "spec §8").

**Architecture:** New sibling package `payment/` mirrors `invoice/`'s file shape exactly (types/calc/transitions/numbering/store/resolver/search + controller). The novel piece is `payment/apply.go`: a transactional Apply/Unapply pair that recomputes both `payment` and `invoice` rollup columns and re-derives invoice status directly from the new balance (bypassing `invoice.CanTransition`, which was never designed for the backward moves an unapply can require — see Task 3.3 for why).

**Tech Stack:** Go (net/http, pgx/pgxpool), PostgreSQL (per-tenant DB), `testify`.

## Global Constraints (identical to the Invoice plan — see CLAUDE.md)

- No `tenant_id`/`ss_tenant_id` column on any tenant-DB table.
- Migrations idempotent + append-only (`CREATE TABLE IF NOT EXISTS`, `ON CONFLICT DO NOTHING`). Never DROP/rename/ALTER-to-add-column.
- Every `/api/tenant/` route: `tenantChain` (RequireAuth → rate limit → TenantResolver) + RBAC `authz.Check` before any write + scope filtering on lists + single-record IDOR guard returning **404** (not 403), logged via `logSecurityEvent(r, "idor_denied", ...)`.
- Filter × scope ANDed, never OR; field keys resolved via whitelist `FieldResolver` only; all values parameterized; keyset pagination only.
- Money `DECIMAL(15,2)`.
- Response envelope `{ success, message?, ... }` via `controllers.writeJSON`/`fail`.
- Conventional Commits; `go build ./... && go vet ./... && go test ./...` green before each commit.
- Integration tests: `//go:build dbtest` + `TEST_DATABASE_URL`, `t.Skip` when unset (mirror `invoice/store_test.go`). Pure-function/resolver/handler-auth tests carry no build tag.
- Files over 300 lines: split them.
- `ResourcePayment` is **already seeded** in `authz/catalog.go` (create/read/update/delete/transition) — no RBAC catalog change needed this module.

## File Structure

**Created:**
- `payment/types.go` — DTOs (Payment, Application, CreatePaymentInput, ApplicationInput, UpdatePaymentInput, Page)
- `payment/money.go` — `round2` helper (spec §8 math is small enough to not need a separate calc.go)
- `payment/transitions.go` — status transition map (spec §7)
- `payment/numbering.go` — `PYMT-000001` formatter
- `payment/store.go` — `Get`, row scanning, `typeIDByCode`/`statusIDByCode`/`statusCodeByID`, `ErrNotFound`, `ClientError`, `nullableInt`
- `payment/store_create.go` — `Create` (header + optional inline `applications[]`)
- `payment/store_update.go` — `Update` (non-monetary fields only), `SoftDelete` (blocked on live applications)
- `payment/apply.go` — `Apply`, `Unapply`, `deriveInvoiceStatus`, invoice-side recompute helpers (spec §8)
- `payment/store_transition.go` — `Transition` (VOID cascades every live application through `Unapply`)
- `payment/quickpay.go` — `QuickPay(ctx, pool, invoiceUUID, amount, actorEmployeeID)` for the legacy endpoint (spec AD-5)
- `payment/resolver.go` — `resolver` implementing `query.FieldResolver` + `SortResolver` + `SearchResolver`
- `payment/search.go` — `Search` (keyset)
- `payment/*_test.go` — unit + `dbtest`-tagged integration tests
- `controllers/payment.go` — `PaymentOps` CRUD + list/search handlers (mirrors `controllers/invoice.go`)
- `controllers/payment_transition.go` — `Transition`, `Apply`, `Unapply` handlers
- `controllers/payment_audit.go` — `auditPayment` helper + `Audit` handler (mirrors `controllers/invoice_audit.go`)
- `controllers/invoice_payments.go` — `InvoiceOps.Payments` handler (list live applications against one invoice)

**Modified:**
- `database/migrations/tenant/schema.sql` — append `lkp_payment_method`, `payment`, `payment_application`, `payment_history` + indexes (spec §5), after the invoice block
- `main.go` — register payment routes + the invoice-payments listing route
- `controllers/invoice_transition.go` — rewrite `RecordPayment` handler body to call `payment.QuickPay` instead of `invoice.RecordPayment`
- `invoice/store_transition.go` — remove the `RecordPayment` function (superseded)
- `invoice/store.go` — remove the now-unused `payableStatuses` var
- `invoice/store_test.go` — remove `TestRecordPayment`/`TestRecordPayment_RejectedBeforeSent` (moved to `payment/`); rewrite `TestUpdate_RejectsBelowAmountPaid` to seed `amount_paid` via raw SQL instead of the removed `RecordPayment`

---

## Phase 1 — Database schema

### Task 1.1: Payment tables + indexes
- [ ] **Step 1:** Read spec §5 (all subsections) and §6. Use the **add-migration** skill to append the following to `database/migrations/tenant/schema.sql`, placed immediately after the `invoice_history` block / its indexes (FK ordering: `payment_application` → `invoice`):

```sql
-- ── Payments module ─────────────────────────────────────────────────

CREATE TABLE IF NOT EXISTS lkp_payment_method (
    payment_method_id          SERIAL       PRIMARY KEY,
    payment_method_name        VARCHAR(50)  NOT NULL,
    payment_method_code        VARCHAR(10)  NOT NULL,
    payment_method_is_active   BOOLEAN      NOT NULL DEFAULT TRUE,
    payment_method_is_system   BOOLEAN      NOT NULL DEFAULT FALSE,
    payment_method_created_at  TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    payment_method_created_by  INTEGER      NOT NULL REFERENCES employee(employee_id),
    payment_method_deleted_at  TIMESTAMP        NULL,
    payment_method_deleted_by  INTEGER          NULL REFERENCES employee(employee_id),
    payment_method_record_version INTEGER   NOT NULL DEFAULT 1,
    CONSTRAINT uq_payment_method_code UNIQUE (payment_method_code)
);

INSERT INTO lkp_payment_method (payment_method_name, payment_method_code, payment_method_is_active, payment_method_is_system, payment_method_created_by) VALUES
    ('Check',       'CHK_', TRUE, TRUE, 1),
    ('Cash',        'CASH', TRUE, TRUE, 1),
    ('Credit Card', 'CC__', TRUE, TRUE, 1),
    ('ACH',         'ACH_', TRUE, TRUE, 1),
    ('Wire',        'WIRE', TRUE, TRUE, 1),
    ('Other',       'OTHR', TRUE, TRUE, 1)
ON CONFLICT (payment_method_code) DO NOTHING;

CREATE TABLE IF NOT EXISTS payment (
    payment_id                  SERIAL        PRIMARY KEY,
    payment_uuid                 UUID          NOT NULL DEFAULT gen_random_uuid(),
    payment_number                VARCHAR(20)      NULL,

    record_type                   INTEGER       NOT NULL REFERENCES lkp_record_type(record_type_id),
    payment_status                 INTEGER       NOT NULL REFERENCES lkp_record_status(record_status_id),

    payment_customer_id            INTEGER       NOT NULL REFERENCES customer(customer_id),

    payment_method                  INTEGER       NOT NULL REFERENCES lkp_payment_method(payment_method_id),
    payment_reference_number        VARCHAR(50)   NOT NULL DEFAULT '',
    payment_date                     DATE          NOT NULL DEFAULT CURRENT_DATE,
    payment_currency                 INTEGER           NULL REFERENCES lkp_currency(currency_id),
    payment_memo                      TEXT          NOT NULL DEFAULT '',
    payment_internal_notes            TEXT          NOT NULL DEFAULT '',

    payment_amount                     DECIMAL(15,2) NOT NULL,
    payment_applied_total               DECIMAL(15,2) NOT NULL DEFAULT 0,
    payment_unapplied_amount             DECIMAL(15,2) NOT NULL DEFAULT 0,

    payment_owner_id                      INTEGER           NULL REFERENCES employee(employee_id),

    payment_custom_fields                  JSONB        NOT NULL DEFAULT '{}',
    payment_created_at                      TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    payment_created_by                       INTEGER          NULL REFERENCES employee(employee_id),
    payment_updated_at                        TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    payment_updated_by                         INTEGER          NULL REFERENCES employee(employee_id),
    payment_deleted_at                          TIMESTAMP        NULL,
    payment_deleted_by                           INTEGER          NULL REFERENCES employee(employee_id),
    payment_record_version                        INTEGER      NOT NULL DEFAULT 1,

    CONSTRAINT uq_payment_uuid       UNIQUE (payment_uuid),
    CONSTRAINT uq_payment_number     UNIQUE (payment_number),
    CONSTRAINT chk_payment_amount_pos      CHECK (payment_amount > 0),
    CONSTRAINT chk_payment_applied_nonneg  CHECK (payment_applied_total >= 0 AND payment_unapplied_amount >= 0),
    CONSTRAINT chk_payment_applied_le_amt  CHECK (payment_applied_total <= payment_amount),
    CONSTRAINT chk_payment_soft_delete     CHECK (
        (payment_deleted_at IS NULL AND payment_deleted_by IS NULL) OR
        (payment_deleted_at IS NOT NULL AND payment_deleted_by IS NOT NULL)
    )
);

CREATE TABLE IF NOT EXISTS payment_application (
    application_id             SERIAL        PRIMARY KEY,
    application_uuid            UUID          NOT NULL DEFAULT gen_random_uuid(),
    payment_id                   INTEGER       NOT NULL REFERENCES payment(payment_id) ON DELETE CASCADE,
    invoice_id                    INTEGER       NOT NULL REFERENCES invoice(invoice_id),

    application_amount             DECIMAL(15,2) NOT NULL,

    application_created_at          TIMESTAMP     NOT NULL DEFAULT CURRENT_TIMESTAMP,
    application_created_by           INTEGER          NULL REFERENCES employee(employee_id),
    application_deleted_at            TIMESTAMP        NULL,
    application_deleted_by             INTEGER          NULL REFERENCES employee(employee_id),
    application_record_version          INTEGER      NOT NULL DEFAULT 1,

    CONSTRAINT uq_payment_application_uuid UNIQUE (application_uuid),
    CONSTRAINT chk_pay_app_amount_pos      CHECK (application_amount > 0),
    CONSTRAINT chk_pay_app_soft_delete     CHECK (
        (application_deleted_at IS NULL AND application_deleted_by IS NULL) OR
        (application_deleted_at IS NOT NULL AND application_deleted_by IS NOT NULL)
    )
);

CREATE UNIQUE INDEX IF NOT EXISTS uq_pay_app_live_pair
    ON payment_application (payment_id, invoice_id) WHERE application_deleted_at IS NULL;

CREATE TABLE IF NOT EXISTS payment_history (
    payment_history_id        SERIAL       PRIMARY KEY,
    payment_id                 INTEGER      NOT NULL REFERENCES payment(payment_id) ON DELETE CASCADE,
    from_status_id               INTEGER          NULL REFERENCES lkp_record_status(record_status_id),
    to_status_id                  INTEGER          NULL REFERENCES lkp_record_status(record_status_id),
    action                          VARCHAR(32)  NOT NULL DEFAULT 'transition',
    actor_employee_id                INTEGER          NULL REFERENCES employee(employee_id),
    snapshot                          JSONB        NOT NULL DEFAULT '{}',
    at                                 TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE INDEX IF NOT EXISTS idx_pay_customer      ON payment (payment_customer_id) WHERE payment_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_pay_status         ON payment (payment_status)      WHERE payment_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_pay_date            ON payment (payment_date)        WHERE payment_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_pay_owner            ON payment (payment_owner_id)    WHERE payment_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_pay_created_id      ON payment (payment_created_at, payment_id)  WHERE payment_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_pay_updated_id      ON payment (payment_updated_at, payment_id)  WHERE payment_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_pay_date_id          ON payment (payment_date, payment_id)         WHERE payment_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_pay_amount_id         ON payment (payment_amount, payment_id)       WHERE payment_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_pay_unapplied_id       ON payment (payment_unapplied_amount, payment_id) WHERE payment_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_pay_status_created       ON payment (payment_status, payment_created_at, payment_id) WHERE payment_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_pay_custom_gin            ON payment USING GIN (payment_custom_fields);

CREATE INDEX IF NOT EXISTS idx_pay_app_payment  ON payment_application (payment_id) WHERE application_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_pay_app_invoice  ON payment_application (invoice_id) WHERE application_deleted_at IS NULL;

CREATE INDEX IF NOT EXISTS idx_pay_history_payment ON payment_history (payment_id);
```

- [ ] **Step 2:** `go build ./...` — confirm nothing else references these table names yet that would break (it shouldn't; this is a pure append).
- [ ] **Step 3:** If `TEST_DATABASE_URL` is reachable, apply the schema twice to verify idempotency (see how `invoice`'s dbtest files connect — `payment.testPool(t)` in Task 2.6 will use the same env var). Otherwise, visually confirm every statement is `IF NOT EXISTS`/`ON CONFLICT DO NOTHING`.
- [ ] **Step 4:** Dispatch the **migration-auditor** agent on the diff. Fix findings.
- [ ] **Step 5:** Commit — `git commit -m "feat(payment): add payment, payment_application, payment_history tables"`.

---

## Phase 2 — Payment pure domain logic (TDD-first)

### Task 2.1: Money rounding helper
- [ ] **Step 1:** Write a failing test in `payment/money_test.go`:

```go
package payment

import "testing"

func TestRound2(t *testing.T) {
	tests := []struct {
		in   float64
		want float64
	}{
		{1071.005, 1071.01},
		{1071.004, 1071.00},
		{0, 0},
		{-5.005, -5.0}, // math.Round rounds half away from zero toward positive; not used for negative amounts in this module but pin the behavior
	}
	for _, tt := range tests {
		if got := round2(tt.in); got != tt.want {
			t.Errorf("round2(%v) = %v, want %v", tt.in, got, tt.want)
		}
	}
}
```

- [ ] **Step 2:** Run `go test ./payment/... -run TestRound2 -v` → FAIL (package/function don't exist yet).
- [ ] **Step 3:** Implement `payment/money.go`:

```go
package payment

import "math"

func round2(x float64) float64 { return math.Round(x*100) / 100 }
```

- [ ] **Step 4:** Run `go test ./payment/... -run TestRound2 -v` → PASS.
- [ ] **Step 5:** Commit — `git commit -m "feat(payment): add money rounding helper"`.

### Task 2.2: Status transition map
- [ ] **Step 1:** Write a failing test `payment/transitions_test.go` covering every edge of spec §7's map:

```go
package payment

import "testing"

func TestTransitions(t *testing.T) {
	tests := []struct {
		from, to string
		want     bool
	}{
		{"PEND", "APPV", true},
		{"PEND", "VOID", true},
		{"PEND", "DEPO", false},
		{"APPV", "DEPO", true},
		{"APPV", "VOID", true},
		{"APPV", "PEND", false},
		{"DEPO", "VOID", false},
		{"DEPO", "APPV", false},
		{"VOID", "PEND", false},
	}
	for _, tt := range tests {
		t.Run(tt.from+"->"+tt.to, func(t *testing.T) {
			if got := CanTransition(tt.from, tt.to); got != tt.want {
				t.Errorf("CanTransition(%q, %q) = %v, want %v", tt.from, tt.to, got, tt.want)
			}
			err := ValidateTransition(tt.from, tt.to)
			if tt.want && err != nil {
				t.Errorf("ValidateTransition(%q, %q) returned error: %v", tt.from, tt.to, err)
			}
			if !tt.want && err == nil {
				t.Errorf("ValidateTransition(%q, %q) expected error, got nil", tt.from, tt.to)
			}
		})
	}
}
```

- [ ] **Step 2:** Run `go test ./payment/... -run TestTransitions -v` → FAIL.
- [ ] **Step 3:** Implement `payment/transitions.go`:

```go
package payment

import "errors"

// ErrInvalidTransition is returned when a status change is not permitted.
var ErrInvalidTransition = errors.New("invalid payment status transition")

// allowedTransitions maps a status code to the set of codes reachable from it
// (spec §7). Terminal states (DEPO, VOID) map to an empty set.
var allowedTransitions = map[string]map[string]bool{
	"PEND": {"APPV": true, "VOID": true},
	"APPV": {"DEPO": true, "VOID": true},
	"DEPO": {},
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

- [ ] **Step 4:** Run to PASS.
- [ ] **Step 5:** Commit — `git commit -m "feat(payment): add status transition validation"`.

### Task 2.3: Payment-number formatting
- [ ] **Step 1:** Write a failing test `payment/numbering_test.go` (mirror `invoice/numbering_test.go`):

```go
package payment

import "testing"

func TestFormatNumber(t *testing.T) {
	for in, want := range map[int64]string{1: "PYMT-000001", 42: "PYMT-000042", 1234567: "PYMT-1234567"} {
		if got := FormatNumber(in); got != want {
			t.Errorf("FormatNumber(%d) = %s, want %s", in, got, want)
		}
	}
}
```

- [ ] **Step 2:** Run → FAIL.
- [ ] **Step 3:** Implement `payment/numbering.go`:

```go
package payment

import "fmt"

const numberPrefix = "PYMT"

// FormatNumber renders the human-readable document number from the row's
// serial PK, zero-padded to 6 digits: PYMT-000001.
func FormatNumber(serialID int64) string {
	return fmt.Sprintf("%s-%06d", numberPrefix, serialID)
}
```

- [ ] **Step 4:** Run to PASS.
- [ ] **Step 5:** Commit — `git commit -m "feat(payment): add payment-number formatting"`.

### Task 2.4: Types
- [ ] **Step 1:** Write `payment/types_test.go` asserting JSON keys on the wire-facing structs (mirror `invoice/types_test.go`'s style — marshal a populated struct, assert the expected keys are present and `-` fields are absent):

```go
package payment

import (
	"encoding/json"
	"testing"
	"time"
)

func TestPayment_JSONShape(t *testing.T) {
	p := Payment{
		ID: "abc", Number: "PYMT-000001", StatusCode: "PEND", StatusName: "Pending",
		Customer: CustomerRef{ID: "cust-1", Name: "Acme"},
		OwnerUserID: "user-should-not-serialize",
		MethodID: 1, MethodName: "Check",
		Amount: 100, AppliedTotal: 40, UnappliedAmount: 60,
		PaymentDate: time.Now(), CustomFields: map[string]any{}, Applications: []Application{},
	}
	b, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, key := range []string{"id", "paymentNumber", "statusCode", "status", "customer", "methodId", "method", "amount", "appliedTotal", "unappliedAmount", "applications"} {
		if _, ok := m[key]; !ok {
			t.Errorf("expected JSON key %q, got keys %v", key, m)
		}
	}
	if _, ok := m["OwnerUserID"]; ok {
		t.Error("OwnerUserID must not serialize (json:\"-\")")
	}
}
```

- [ ] **Step 2:** Run → FAIL (types don't exist).
- [ ] **Step 3:** Implement `payment/types.go`:

```go
package payment

import "time"

// CustomerRef is the flattened {id, name} for "who paid" navigation.
type CustomerRef struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

// Application is one live payment_application row, joined with its invoice's
// display fields.
type Application struct {
	ID            string    `json:"id"`
	InvoiceID     string    `json:"invoiceId"`
	InvoiceNumber string    `json:"invoiceNumber"`
	Amount        float64   `json:"amount"`
	CreatedAt     time.Time `json:"createdAt"`
}

// Payment is the payment header + its live applications.
type Payment struct {
	ID     string `json:"id"`
	Number string `json:"paymentNumber"`

	StatusCode string `json:"statusCode"`
	StatusName string `json:"status"`

	Customer CustomerRef `json:"customer"`

	OwnerUserID     string `json:"-"`
	OwnerEmployeeID *int   `json:"ownerEmployeeId,omitempty"`

	MethodID   int    `json:"methodId"`
	MethodName string `json:"method"`

	ReferenceNumber string    `json:"referenceNumber"`
	PaymentDate     time.Time `json:"paymentDate"`
	CurrencyID      *int      `json:"currencyId,omitempty"`
	Memo            string    `json:"memo"`
	InternalNotes   string    `json:"internalNotes"`

	Amount          float64 `json:"amount"`
	AppliedTotal    float64 `json:"appliedTotal"`
	UnappliedAmount float64 `json:"unappliedAmount"`

	CustomFields map[string]any `json:"customFields"`
	Applications []Application  `json:"applications"`

	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
	RecordVersion int       `json:"recordVersion"`
}

// ApplicationInput is one entry of a create/apply request.
type ApplicationInput struct {
	InvoiceUUID string  `json:"invoiceUuid"`
	Amount      float64 `json:"amount"`
}

// CreatePaymentInput is the request payload for POST /api/tenant/payments.
type CreatePaymentInput struct {
	CustomerUUID    string             `json:"customerUuid"`
	MethodID        int                `json:"methodId"`
	ReferenceNumber string             `json:"referenceNumber"`
	PaymentDate     *time.Time         `json:"paymentDate,omitempty"`
	CurrencyID      *int               `json:"currencyId,omitempty"`
	OwnerEmployeeID *int               `json:"ownerEmployeeId,omitempty"`
	Amount          float64            `json:"amount"`
	Memo            string             `json:"memo"`
	InternalNotes   string             `json:"internalNotes"`
	CustomFields    map[string]any     `json:"customFields"`
	Applications    []ApplicationInput `json:"applications"`
}

// UpdatePaymentInput is the request payload for PATCH /api/tenant/payments/{uuid}.
// Notice it has no Amount field (spec AD-10: amount is immutable post-creation).
type UpdatePaymentInput struct {
	MethodID        int            `json:"methodId"`
	ReferenceNumber string         `json:"referenceNumber"`
	PaymentDate     *time.Time     `json:"paymentDate,omitempty"`
	CurrencyID      *int           `json:"currencyId,omitempty"`
	OwnerEmployeeID *int           `json:"ownerEmployeeId,omitempty"`
	Memo            string         `json:"memo"`
	InternalNotes   string         `json:"internalNotes"`
	CustomFields    map[string]any `json:"customFields"`
}

// Page is one page of payments plus keyset-pagination state.
type Page struct {
	Records    []Payment `json:"records"`
	NextCursor string    `json:"nextCursor"`
	HasMore    bool      `json:"hasMore"`
}
```

- [ ] **Step 4:** Run to PASS.
- [ ] **Step 5:** Commit — `git commit -m "feat(payment): add payment/application/input types"`.

---

## Phase 3 — Payment store

### Task 3.1: Store — `Get` + row scanning + shared helpers
- [ ] **Step 1:** Read `invoice/store.go` before writing this — match its `headerSelect`/`scanInvoice`/`typeIDByCode`/`statusIDByCode` shape exactly, substituting `payment`/`Payment`.
- [ ] **Step 2:** Implement `payment/store.go`:

```go
package payment

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// ErrNotFound is returned when a payment id matches no live row.
var ErrNotFound = errors.New("payment not found")

// ClientError marks a caller-fault error (maps to HTTP 400).
type ClientError struct{ Msg string }

func (e ClientError) Error() string { return e.Msg }

const headerSelect = `
	SELECT p.payment_uuid, p.payment_number,
	       COALESCE(rs.record_status_code,''), COALESCE(rs.record_status_name,''),
	       c.customer_uuid, COALESCE(c.customer_name,''),
	       COALESCE(ou.id::text,''), p.payment_owner_id,
	       p.payment_method, COALESCE(pm.payment_method_name,''),
	       p.payment_reference_number, p.payment_date, p.payment_currency,
	       p.payment_memo, p.payment_internal_notes,
	       p.payment_amount, p.payment_applied_total, p.payment_unapplied_amount,
	       p.payment_custom_fields, p.payment_created_at, p.payment_updated_at, p.payment_record_version,
	       p.payment_id, p.payment_status, p.payment_customer_id
	FROM payment p
	JOIN lkp_record_status rs ON rs.record_status_id = p.payment_status
	JOIN customer c ON c.customer_id = p.payment_customer_id
	JOIN lkp_payment_method pm ON pm.payment_method_id = p.payment_method
	LEFT JOIN employee oe ON oe.employee_id = p.payment_owner_id
	LEFT JOIN users ou ON ou.id = oe.employee_user_id`

type paymentMeta struct {
	internalID int
	statusID   int
	customerID int
}

func scanPayment(row pgx.Row) (*Payment, paymentMeta, error) {
	var (
		p      Payment
		ownerEmpID *int
		currencyID *int
		custom     map[string]any
		meta       paymentMeta
	)
	err := row.Scan(
		&p.ID, &p.Number,
		&p.StatusCode, &p.StatusName,
		&p.Customer.ID, &p.Customer.Name,
		&p.OwnerUserID, &ownerEmpID,
		&p.MethodID, &p.MethodName,
		&p.ReferenceNumber, &p.PaymentDate, &currencyID,
		&p.Memo, &p.InternalNotes,
		&p.Amount, &p.AppliedTotal, &p.UnappliedAmount,
		&custom, &p.CreatedAt, &p.UpdatedAt, &p.RecordVersion,
		&meta.internalID, &meta.statusID, &meta.customerID,
	)
	if err != nil {
		return nil, paymentMeta{}, err
	}
	p.OwnerEmployeeID = ownerEmpID
	p.CurrencyID = currencyID
	if custom == nil {
		custom = map[string]any{}
	}
	p.CustomFields = custom
	p.Applications = []Application{}
	return &p, meta, nil
}

const applicationSelect = `
	SELECT pa.application_uuid, i.invoice_uuid, COALESCE(i.invoice_number,''),
	       pa.application_amount, pa.application_created_at
	FROM payment_application pa
	JOIN invoice i ON i.invoice_id = pa.invoice_id
	WHERE pa.payment_id = $1 AND pa.application_deleted_at IS NULL
	ORDER BY pa.application_created_at ASC`

func loadApplications(ctx context.Context, pool *pgxpool.Pool, internalID int) ([]Application, error) {
	rows, err := pool.Query(ctx, applicationSelect, internalID)
	if err != nil {
		return nil, fmt.Errorf("load payment applications: %w", err)
	}
	defer rows.Close()
	out := []Application{}
	for rows.Next() {
		var a Application
		if err := rows.Scan(&a.ID, &a.InvoiceID, &a.InvoiceNumber, &a.Amount, &a.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan payment application: %w", err)
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// Get loads a single live payment (header + applications) by its external uuid.
func Get(ctx context.Context, pool *pgxpool.Pool, id string) (*Payment, error) {
	p, meta, err := scanPayment(pool.QueryRow(ctx, headerSelect+`
		WHERE p.payment_uuid = $1 AND p.payment_deleted_at IS NULL`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get payment: %w", err)
	}
	apps, err := loadApplications(ctx, pool, meta.internalID)
	if err != nil {
		return nil, err
	}
	p.Applications = apps
	return p, nil
}

func typeIDByCode(ctx context.Context, pool *pgxpool.Pool, code string) (int, error) {
	var id int
	if err := pool.QueryRow(ctx,
		`SELECT record_type_id FROM lkp_record_type WHERE record_type_code = $1`, code).Scan(&id); err != nil {
		return 0, fmt.Errorf("resolve record type %s: %w", code, err)
	}
	return id, nil
}

func statusIDByCode(ctx context.Context, pool *pgxpool.Pool, typeID int, code string) (int, error) {
	var id int
	if err := pool.QueryRow(ctx,
		`SELECT record_status_id FROM lkp_record_status WHERE record_status_record_type = $1 AND record_status_code = $2`,
		typeID, code).Scan(&id); err != nil {
		return 0, fmt.Errorf("resolve status %s: %w", code, err)
	}
	return id, nil
}

func statusCodeByID(ctx context.Context, pool *pgxpool.Pool, statusID int) (string, error) {
	var code string
	if err := pool.QueryRow(ctx,
		`SELECT record_status_code FROM lkp_record_status WHERE record_status_id = $1`, statusID).Scan(&code); err != nil {
		return "", fmt.Errorf("resolve status code: %w", err)
	}
	return code, nil
}

func nullableInt(v int) any {
	if v == 0 {
		return nil
	}
	return v
}
```

- [ ] **Step 3:** `go build ./payment/...` → compiles (no tests yet reference `Get` end-to-end; that comes with Create in Task 3.2). Commit — `git commit -m "feat(payment): add store scanning + lookup helpers"`.

### Task 3.2: Store — `Create` (header + optional inline applications)
- [ ] **Step 1:** Write failing `dbtest`-tagged integration tests in `payment/store_test.go`. Start the file with the shared test harness (mirror `invoice/store_test.go`'s `testPool`):

```go
//go:build dbtest

package payment

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/invoice"
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

// seedCustomer inserts a minimal live customer.
func seedCustomer(t *testing.T, pool *pgxpool.Pool) string {
	t.Helper()
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	var custTypeID int
	if err := pool.QueryRow(ctx, `SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'CUST'`).Scan(&custTypeID); err != nil {
		t.Fatalf("resolve CUST record type: %v", err)
	}
	var custUUID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO customer (record_type, customer_name, customer_created_by)
		VALUES ($1, $2, 1) RETURNING customer_uuid`, custTypeID, "Test Customer "+suffix).Scan(&custUUID); err != nil {
		t.Fatalf("seed customer: %v", err)
	}
	return custUUID
}

// seedSentInvoice creates a customer + a 100.00 invoice already transitioned
// to SENT (the only status Apply accepts payment against).
func seedSentInvoice(t *testing.T, pool *pgxpool.Pool, amount float64) (custUUID, invUUID string) {
	t.Helper()
	ctx := context.Background()
	suffix := fmt.Sprintf("%d", time.Now().UnixNano())
	var itemUUID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO inventory_item (inventory_item_sku, inventory_item_name, inventory_item_unit_id, inventory_item_unit_price, inventory_item_created_by)
		VALUES ($1, 'Test Item', 1, $2, 1) RETURNING inventory_item_uuid`, "TEST-SKU-"+suffix, amount).Scan(&itemUUID); err != nil {
		t.Fatalf("seed inventory item: %v", err)
	}
	custUUID = seedCustomer(t, pool)
	inv, err := invoice.Create(ctx, pool, invoice.CreateInvoiceInput{
		CustomerUUID: custUUID,
		Items:        []invoice.InvoiceLineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1, UnitPrice: amount}},
	}, 1)
	if err != nil {
		t.Fatalf("seed invoice: %v", err)
	}
	for _, st := range []string{"PAPV", "APPV", "SENT"} {
		if inv, err = invoice.Transition(ctx, pool, inv.ID, st, 1); err != nil {
			t.Fatalf("transition invoice to %s: %v", st, err)
		}
	}
	return custUUID, inv.ID
}

func firstMethodID(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var id int
	if err := pool.QueryRow(context.Background(),
		`SELECT payment_method_id FROM lkp_payment_method WHERE payment_method_code = 'CHK_'`).Scan(&id); err != nil {
		t.Fatalf("resolve payment method: %v", err)
	}
	return id
}

func TestCreate_HeaderOnly(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID := seedCustomer(t, pool)
	methodID := firstMethodID(t, pool)

	p, err := Create(ctx, pool, CreatePaymentInput{
		CustomerUUID: custUUID, MethodID: methodID, Amount: 500,
	}, 1)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if !strings.HasPrefix(p.Number, "PYMT-") {
		t.Fatalf("expected PYMT- prefixed number, got %q", p.Number)
	}
	if p.StatusCode != "PEND" {
		t.Fatalf("new payment must start PEND, got %s", p.StatusCode)
	}
	if p.AppliedTotal != 0 || p.UnappliedAmount != 500 {
		t.Fatalf("expected 0 applied / 500 unapplied, got applied=%v unapplied=%v", p.AppliedTotal, p.UnappliedAmount)
	}
}

func TestCreate_WithInlineApplication(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID, invUUID := seedSentInvoice(t, pool, 100)
	methodID := firstMethodID(t, pool)

	p, err := Create(ctx, pool, CreatePaymentInput{
		CustomerUUID: custUUID, MethodID: methodID, Amount: 150,
		Applications: []ApplicationInput{{InvoiceUUID: invUUID, Amount: 100}},
	}, 1)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if p.AppliedTotal != 100 || p.UnappliedAmount != 50 {
		t.Fatalf("expected applied=100 unapplied=50, got applied=%v unapplied=%v", p.AppliedTotal, p.UnappliedAmount)
	}
	if len(p.Applications) != 1 || p.Applications[0].InvoiceID != invUUID {
		t.Fatalf("expected 1 application against %s, got %+v", invUUID, p.Applications)
	}
	inv, err := invoice.Get(ctx, pool, invUUID)
	if err != nil {
		t.Fatalf("get invoice: %v", err)
	}
	if inv.AmountPaid != 100 || inv.BalanceDue != 0 || inv.StatusCode != "PAID" {
		t.Fatalf("expected invoice fully paid, got paid=%v balance=%v status=%s", inv.AmountPaid, inv.BalanceDue, inv.StatusCode)
	}
}

func TestCreate_UnknownCustomer_IsClientError(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	methodID := firstMethodID(t, pool)
	_, err := Create(ctx, pool, CreatePaymentInput{
		CustomerUUID: "00000000-0000-0000-0000-000000000000", MethodID: methodID, Amount: 10,
	}, 1)
	if _, ok := err.(ClientError); !ok {
		t.Fatalf("expected ClientError, got %T: %v", err, err)
	}
}
```

- [ ] **Step 2:** Run `go test ./payment/... -tags dbtest -run TestCreate -v` (skips cleanly without `TEST_DATABASE_URL`; with it set, expect FAIL — `Create` doesn't exist yet).
- [ ] **Step 3:** Implement `payment/store_create.go`. It resolves the customer, inserts the header, assigns the number, writes `payment_history` action='create', and — **after that transaction commits** — applies each inline application by calling `Apply` (Task 3.3) once per entry, sequentially. Document the two-phase trade-off inline (mirrors the accepted QuickPay trade-off in spec AD-5/§8): a later application's failure does not roll back the header or earlier successful applications.

```go
package payment

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func resolveCustomer(ctx context.Context, pool *pgxpool.Pool, customerUUID string) (int, error) {
	var id int
	err := pool.QueryRow(ctx,
		`SELECT customer_id FROM customer WHERE customer_uuid = $1 AND customer_deleted_at IS NULL`, customerUUID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ClientError{Msg: "Unknown or deleted customer."}
	}
	if err != nil {
		return 0, fmt.Errorf("resolve customer: %w", err)
	}
	return id, nil
}

func resolveMethod(ctx context.Context, pool *pgxpool.Pool, methodID int) error {
	var exists bool
	err := pool.QueryRow(ctx,
		`SELECT TRUE FROM lkp_payment_method WHERE payment_method_id = $1 AND payment_method_deleted_at IS NULL AND payment_method_is_active`,
		methodID).Scan(&exists)
	if errors.Is(err, pgx.ErrNoRows) {
		return ClientError{Msg: "Unknown or inactive payment method."}
	}
	if err != nil {
		return fmt.Errorf("resolve payment method: %w", err)
	}
	return nil
}

// Create inserts a new payment header inside one transaction (resolves the
// customer + method, validates custom fields, inserts the row, assigns the
// payment number, writes the 'create' history row), then — once that
// transaction has committed — applies each inline application sequentially
// by calling Apply. Applications are NOT part of the header's transaction: a
// later application failing does not roll back the header or earlier
// successful applications (mirrors the QuickPay trade-off, spec AD-5/§8).
// New payments start at PEND.
func Create(ctx context.Context, pool *pgxpool.Pool, in CreatePaymentInput, actorEmployeeID int) (*Payment, error) {
	if in.CustomerUUID == "" {
		return nil, ClientError{Msg: "customerUuid is required."}
	}
	if in.Amount <= 0 {
		return nil, ClientError{Msg: "amount must be positive."}
	}
	custID, err := resolveCustomer(ctx, pool, in.CustomerUUID)
	if err != nil {
		return nil, err
	}
	if err := resolveMethod(ctx, pool, in.MethodID); err != nil {
		return nil, err
	}

	custom := in.CustomFields
	if custom == nil {
		custom = map[string]any{}
	}
	if err := validateCustom(ctx, pool, custom); err != nil {
		return nil, err
	}

	typeID, err := typeIDByCode(ctx, pool, "PYMT")
	if err != nil {
		return nil, err
	}
	pendStatusID, err := statusIDByCode(ctx, pool, typeID, "PEND")
	if err != nil {
		return nil, err
	}

	ownerEmp := in.OwnerEmployeeID
	if ownerEmp == nil && actorEmployeeID != 0 {
		ownerEmp = &actorEmployeeID
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin create payment: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var newID int
	var newUUID string
	err = tx.QueryRow(ctx, `
		INSERT INTO payment (
			record_type, payment_status, payment_customer_id,
			payment_method, payment_reference_number, payment_date, payment_currency,
			payment_memo, payment_internal_notes,
			payment_amount, payment_applied_total, payment_unapplied_amount,
			payment_owner_id, payment_custom_fields, payment_created_by, payment_updated_by
		) VALUES (
			$1,$2,$3, $4,$5,COALESCE($6, CURRENT_DATE),$7, $8,$9,
			$10,0,$10,
			$11,$12,$13,$13
		) RETURNING payment_id, payment_uuid`,
		typeID, pendStatusID, custID,
		in.MethodID, in.ReferenceNumber, in.PaymentDate, in.CurrencyID,
		in.Memo, in.InternalNotes,
		in.Amount,
		ownerEmp, custom, nullableInt(actorEmployeeID),
	).Scan(&newID, &newUUID)
	if err != nil {
		return nil, fmt.Errorf("insert payment: %w", err)
	}

	number := FormatNumber(int64(newID))
	if _, err := tx.Exec(ctx, `UPDATE payment SET payment_number = $1 WHERE payment_id = $2`, number, newID); err != nil {
		return nil, fmt.Errorf("set payment number: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO payment_history (payment_id, from_status_id, to_status_id, action, actor_employee_id)
		VALUES ($1, NULL, $2, 'create', $3)`, newID, pendStatusID, nullableInt(actorEmployeeID)); err != nil {
		return nil, fmt.Errorf("insert payment create history: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit create payment: %w", err)
	}

	for _, app := range in.Applications {
		if _, err := Apply(ctx, pool, newUUID, app.InvoiceUUID, app.Amount, actorEmployeeID); err != nil {
			return nil, fmt.Errorf("apply inline application to invoice %s: %w", app.InvoiceUUID, err)
		}
	}
	return Get(ctx, pool, newUUID)
}
```

- [ ] **Step 4:** Also add `validateCustom` to `payment/store.go` (append, don't create a new file — it's a 15-line helper):

```go
// validateCustom validates in.CustomFields against the "payment" workflow's
// field definitions, if one has been seeded. No-ops when it hasn't (mirrors
// invoice.validateCustom).
func validateCustom(ctx context.Context, pool *pgxpool.Pool, custom map[string]any) error {
	if custom == nil {
		return nil
	}
	wf, err := workflow.GetWorkflowByKey(ctx, pool, "payment")
	if errors.Is(err, workflow.ErrWorkflowNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load payment workflow: %w", err)
	}
	def, err := workflow.LoadDefinition(ctx, pool, wf.ID)
	if err != nil {
		return fmt.Errorf("load payment field definitions: %w", err)
	}
	if err := workflow.ValidateCustomFieldsPartial(def.Fields, custom); err != nil {
		return ClientError{Msg: err.Error()}
	}
	return nil
}
```
  Add `"stonesuite-backend/workflow"` to `payment/store.go`'s import block.
- [ ] **Step 5:** This test file depends on `Apply` (Task 3.3), which doesn't exist yet — `go build ./payment/...` will fail until Task 3.3 lands. That's expected; **do not skip ahead**, just note the dependency and proceed directly to Task 3.3 before attempting to run these tests.
- [ ] **Step 6:** Commit the store_create.go + store.go changes now (tests will go green once Task 3.3 lands) — `git commit -m "feat(payment): add transactional create with inline applications"`.

### Task 3.3: `Apply` / `Unapply` — the AR recompute core
- [ ] **Step 1:** Read spec §8 in full before writing this. This is the one piece of business logic with no sibling-module precedent to copy: it must (a) lock both the `payment` and `invoice` rows, in that fixed order (payment first, then invoice — every caller of Apply/Unapply/the void-cascade must use this same order to avoid lock-ordering deadlocks between concurrent calls), (b) cap the application amount, (c) recompute both rollups, (d) re-derive invoice status **directly from the new balance**, bypassing `invoice.CanTransition`.

  **Why bypass `invoice.CanTransition`:** `invoice/transitions.go`'s map treats `PAID` as terminal (no outgoing moves at all, not even to `VOID`) and explicitly rejects `PART -> SENT`. That map governs *user-directed* transitions (`POST /invoices/{uuid}/transition`) and was correct for that purpose — nothing before this module ever needed to move an invoice's status *backward*. `Unapply` needs exactly that: reversing a payment can take an invoice from `PAID` back to `PART`, or from `PART` back to `SENT`. So this module derives invoice status purely from the recomputed balance and writes it with a raw `UPDATE`, the same way `invoice.RecordPayment` already bypassed the general-purpose transition map for its own forward-only auto-transitions (it called `statusIDByCode` directly, not `Transition`). This is a derived system rollup, not a user transition — the distinction the invoice module itself already drew.

- [ ] **Step 2:** Write failing `dbtest` tests, appended to `payment/store_test.go` (or a new `payment/apply_test.go` if `store_test.go` is approaching 300 lines — check with `wc -l` first):

```go
func TestApply_CapsAtInvoiceBalance(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID, invUUID := seedSentInvoice(t, pool, 100)
	methodID := firstMethodID(t, pool)
	p, err := Create(ctx, pool, CreatePaymentInput{CustomerUUID: custUUID, MethodID: methodID, Amount: 200}, 1)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := Apply(ctx, pool, p.ID, invUUID, 150, 1); err == nil {
		t.Fatal("expected error applying more than the invoice's balance_due (100)")
	}
	p2, err := Apply(ctx, pool, p.ID, invUUID, 100, 1)
	if err != nil {
		t.Fatalf("apply within balance: %v", err)
	}
	if p2.AppliedTotal != 100 || p2.UnappliedAmount != 100 {
		t.Fatalf("applied=%v unapplied=%v, want 100/100", p2.AppliedTotal, p2.UnappliedAmount)
	}
}

func TestApply_RejectsCrossCustomerInvoice(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	_, invUUID := seedSentInvoice(t, pool, 100) // invoice belongs to its own customer
	otherCust := seedCustomer(t, pool)          // payment belongs to a different customer
	methodID := firstMethodID(t, pool)
	p, err := Create(ctx, pool, CreatePaymentInput{CustomerUUID: otherCust, MethodID: methodID, Amount: 100}, 1)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := Apply(ctx, pool, p.ID, invUUID, 50, 1); err == nil {
		t.Fatal("expected error applying to an invoice of a different customer")
	}
}

func TestApply_RejectsUnsentInvoice(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID := seedCustomer(t, pool)
	methodID := firstMethodID(t, pool)
	// Invoice left at DRFT (never transitioned to SENT).
	var itemUUID string
	if err := pool.QueryRow(ctx, `
		INSERT INTO inventory_item (inventory_item_sku, inventory_item_name, inventory_item_unit_id, inventory_item_unit_price, inventory_item_created_by)
		VALUES ('DRFT-SKU', 'Item', 1, 50, 1) RETURNING inventory_item_uuid`).Scan(&itemUUID); err != nil {
		t.Fatalf("seed item: %v", err)
	}
	inv, err := invoice.Create(ctx, pool, invoice.CreateInvoiceInput{
		CustomerUUID: custUUID, Items: []invoice.InvoiceLineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1, UnitPrice: 50}},
	}, 1)
	if err != nil {
		t.Fatalf("create invoice: %v", err)
	}
	p, err := Create(ctx, pool, CreatePaymentInput{CustomerUUID: custUUID, MethodID: methodID, Amount: 50}, 1)
	if err != nil {
		t.Fatalf("create payment: %v", err)
	}
	if _, err := Apply(ctx, pool, p.ID, inv.ID, 50, 1); err == nil {
		t.Fatal("expected error applying to a DRFT invoice")
	}
}

func TestUnapply_RestoresBalanceAndRevertsStatus(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID, invUUID := seedSentInvoice(t, pool, 100)
	methodID := firstMethodID(t, pool)
	p, err := Create(ctx, pool, CreatePaymentInput{CustomerUUID: custUUID, MethodID: methodID, Amount: 100}, 1)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := Apply(ctx, pool, p.ID, invUUID, 100, 1); err != nil {
		t.Fatalf("apply: %v", err)
	}
	inv, _ := invoice.Get(ctx, pool, invUUID)
	if inv.StatusCode != "PAID" {
		t.Fatalf("expected PAID after full apply, got %s", inv.StatusCode)
	}

	p2, err := Unapply(ctx, pool, p.ID, invUUID, 1)
	if err != nil {
		t.Fatalf("unapply: %v", err)
	}
	if p2.AppliedTotal != 0 || p2.UnappliedAmount != 100 {
		t.Fatalf("applied=%v unapplied=%v, want 0/100", p2.AppliedTotal, p2.UnappliedAmount)
	}
	inv2, _ := invoice.Get(ctx, pool, invUUID)
	if inv2.AmountPaid != 0 || inv2.BalanceDue != 100 || inv2.StatusCode != "SENT" {
		t.Fatalf("expected invoice reverted to SENT/0/100, got status=%s paid=%v balance=%v", inv2.StatusCode, inv2.AmountPaid, inv2.BalanceDue)
	}
}

func TestApply_ReapplyIncreasesExistingRow(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID, invUUID := seedSentInvoice(t, pool, 100)
	methodID := firstMethodID(t, pool)
	p, err := Create(ctx, pool, CreatePaymentInput{CustomerUUID: custUUID, MethodID: methodID, Amount: 100}, 1)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := Apply(ctx, pool, p.ID, invUUID, 40, 1); err != nil {
		t.Fatalf("apply 1: %v", err)
	}
	p2, err := Apply(ctx, pool, p.ID, invUUID, 60, 1)
	if err != nil {
		t.Fatalf("apply 2: %v", err)
	}
	if len(p2.Applications) != 1 {
		t.Fatalf("expected exactly 1 live application row (merged), got %d", len(p2.Applications))
	}
	if p2.Applications[0].Amount != 100 {
		t.Fatalf("expected merged application amount 100, got %v", p2.Applications[0].Amount)
	}
}
```

- [ ] **Step 3:** Run `go test ./payment/... -tags dbtest -run 'TestApply|TestUnapply' -v` → FAIL (`Apply`/`Unapply` don't exist).
- [ ] **Step 4:** Implement `payment/apply.go`:

```go
package payment

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// invoicePayableStatuses are the only invoice statuses Apply accepts new
// money against — identical to the gate the retired invoice.RecordPayment
// enforced (spec §8, §11).
var invoicePayableStatuses = map[string]bool{"SENT": true, "PART": true, "ODUE": true}

type lockedInvoice struct {
	internalID int
	customerID int
	statusCode string
	grandTotal float64
	amountPaid float64
}

// lockInvoiceForUpdate loads + row-locks a live invoice by uuid inside tx.
// Callers must already hold the payment row's lock first (fixed lock order:
// payment before invoice) to keep Apply/Unapply/void-cascade deadlock-free.
func lockInvoiceForUpdate(ctx context.Context, tx pgx.Tx, invoiceUUID string) (lockedInvoice, error) {
	var li lockedInvoice
	err := tx.QueryRow(ctx, `
		SELECT i.invoice_id, i.invoice_customer_id, rs.record_status_code, i.invoice_grand_total, i.invoice_amount_paid
		FROM invoice i
		JOIN lkp_record_status rs ON rs.record_status_id = i.invoice_status
		WHERE i.invoice_uuid = $1 AND i.invoice_deleted_at IS NULL
		FOR UPDATE OF i`, invoiceUUID,
	).Scan(&li.internalID, &li.customerID, &li.statusCode, &li.grandTotal, &li.amountPaid)
	if errors.Is(err, pgx.ErrNoRows) {
		return lockedInvoice{}, ClientError{Msg: "Unknown or deleted invoice."}
	}
	if err != nil {
		return lockedInvoice{}, fmt.Errorf("lock invoice: %w", err)
	}
	return li, nil
}

type lockedPayment struct {
	internalID int
	customerID int
	statusCode string
	amount     float64
}

func lockPaymentForUpdate(ctx context.Context, tx pgx.Tx, paymentUUID string) (lockedPayment, error) {
	var lp lockedPayment
	err := tx.QueryRow(ctx, `
		SELECT p.payment_id, p.payment_customer_id, rs.record_status_code, p.payment_amount
		FROM payment p
		JOIN lkp_record_status rs ON rs.record_status_id = p.payment_status
		WHERE p.payment_uuid = $1 AND p.payment_deleted_at IS NULL
		FOR UPDATE OF p`, paymentUUID,
	).Scan(&lp.internalID, &lp.customerID, &lp.statusCode, &lp.amount)
	if errors.Is(err, pgx.ErrNoRows) {
		return lockedPayment{}, ErrNotFound
	}
	if err != nil {
		return lockedPayment{}, fmt.Errorf("lock payment: %w", err)
	}
	return lp, nil
}

// recomputePayment recomputes and stores payment_applied_total/unapplied_amount
// from the live payment_application rows, inside tx.
func recomputePayment(ctx context.Context, tx pgx.Tx, internalID int, amount float64, actorEmployeeID int) error {
	var applied float64
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(application_amount), 0) FROM payment_application
		WHERE payment_id = $1 AND application_deleted_at IS NULL`, internalID).Scan(&applied); err != nil {
		return fmt.Errorf("sum payment applications: %w", err)
	}
	applied = round2(applied)
	unapplied := round2(amount - applied)
	if _, err := tx.Exec(ctx, `
		UPDATE payment SET payment_applied_total = $1, payment_unapplied_amount = $2,
			payment_updated_at = NOW(), payment_updated_by = $3, payment_record_version = payment_record_version + 1
		WHERE payment_id = $4`, applied, unapplied, nullableInt(actorEmployeeID), internalID); err != nil {
		return fmt.Errorf("update payment rollup: %w", err)
	}
	return nil
}

// deriveInvoiceStatus re-derives an invoice's status purely from its
// recomputed balance (spec §8 Apply/Unapply steps). This intentionally does
// NOT go through invoice.CanTransition: that map is for user-directed
// transitions and has no path back out of PAID, or from PART to SENT — moves
// an Unapply legitimately needs. See Task 3.3 step 1 for the full rationale.
func deriveInvoiceStatus(currentCode string, amountPaid, grandTotal float64) string {
	balanceDue := grandTotal - amountPaid
	switch {
	case balanceDue <= 0.005:
		return "PAID"
	case amountPaid > 0.005:
		return "PART"
	case currentCode == "PART" || currentCode == "PAID":
		return "SENT" // fully unapplied back to zero; ODUE re-flagging is a separate concern
	default:
		return currentCode
	}
}

// recomputeInvoice recomputes invoice_amount_paid/balance_due from live
// payment_application rows (across all payments), re-derives status, and
// writes both plus an invoice_history row, inside tx.
func recomputeInvoice(ctx context.Context, tx pgx.Tx, li lockedInvoice, action string, actorEmployeeID int) error {
	var amountPaid float64
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(pa.application_amount), 0)
		FROM payment_application pa
		JOIN payment p ON p.payment_id = pa.payment_id
		WHERE pa.invoice_id = $1 AND pa.application_deleted_at IS NULL AND p.payment_deleted_at IS NULL`,
		li.internalID).Scan(&amountPaid); err != nil {
		return fmt.Errorf("sum invoice applications: %w", err)
	}
	amountPaid = round2(amountPaid)
	balanceDue := round2(li.grandTotal - amountPaid)
	if balanceDue < 0 {
		balanceDue = 0
	}

	toCode := deriveInvoiceStatus(li.statusCode, amountPaid, li.grandTotal)
	var invTypeID int
	if err := tx.QueryRow(ctx, `SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'INVC'`).Scan(&invTypeID); err != nil {
		return fmt.Errorf("resolve INVC type: %w", err)
	}
	var fromStatusID, toStatusID int
	if err := tx.QueryRow(ctx, `SELECT record_status_id FROM lkp_record_status WHERE record_status_record_type = $1 AND record_status_code = $2`,
		invTypeID, li.statusCode).Scan(&fromStatusID); err != nil {
		return fmt.Errorf("resolve invoice from-status: %w", err)
	}
	if err := tx.QueryRow(ctx, `SELECT record_status_id FROM lkp_record_status WHERE record_status_record_type = $1 AND record_status_code = $2`,
		invTypeID, toCode).Scan(&toStatusID); err != nil {
		return fmt.Errorf("resolve invoice to-status: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE invoice SET invoice_amount_paid = $1, invoice_balance_due = $2, invoice_status = $3,
			invoice_updated_at = NOW(), invoice_updated_by = $4, invoice_record_version = invoice_record_version + 1
		WHERE invoice_id = $5`, amountPaid, balanceDue, toStatusID, nullableInt(actorEmployeeID), li.internalID); err != nil {
		return fmt.Errorf("update invoice rollup: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO invoice_history (invoice_id, from_status_id, to_status_id, action, actor_employee_id)
		VALUES ($1, $2, $3, $4, $5)`, li.internalID, fromStatusID, toStatusID, action, nullableInt(actorEmployeeID)); err != nil {
		return fmt.Errorf("insert invoice %s history: %w", action, err)
	}
	return nil
}

// Apply allocates amount of paymentUUID's unapplied balance to invoiceUUID.
// Caps at min(payment.unapplied_amount, invoice.balance_due); rejects (400)
// rather than clamping if amount exceeds that cap (spec AD-8). Rejects (409)
// if the payment is VOID or the invoice isn't in a payable status, and (400)
// on a customer mismatch.
func Apply(ctx context.Context, pool *pgxpool.Pool, paymentUUID, invoiceUUID string, amount float64, actorEmployeeID int) (*Payment, error) {
	if amount <= 0 {
		return nil, ClientError{Msg: "amount must be positive."}
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin apply: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	lp, err := lockPaymentForUpdate(ctx, tx, paymentUUID) // lock order: payment first
	if err != nil {
		return nil, err
	}
	if lp.statusCode == "VOID" {
		return nil, ClientError{Msg: "Cannot apply a voided payment."}
	}
	li, err := lockInvoiceForUpdate(ctx, tx, invoiceUUID) // then invoice
	if err != nil {
		return nil, err
	}
	if li.customerID != lp.customerID {
		return nil, ClientError{Msg: "Invoice belongs to a different customer than the payment."}
	}
	if !invoicePayableStatuses[li.statusCode] {
		return nil, ClientError{Msg: "Cannot apply payment to a " + li.statusCode + " invoice; it must be sent first."}
	}

	var applied float64
	if err := tx.QueryRow(ctx, `SELECT COALESCE(SUM(application_amount),0) FROM payment_application WHERE payment_id = $1 AND application_deleted_at IS NULL`, lp.internalID).Scan(&applied); err != nil {
		return nil, fmt.Errorf("sum payment applications: %w", err)
	}
	unapplied := round2(lp.amount - applied)
	invoiceBalance := round2(li.grandTotal - li.amountPaid)
	capAmt := unapplied
	if invoiceBalance < capAmt {
		capAmt = invoiceBalance
	}
	if amount > capAmt+0.001 {
		return nil, ClientError{Msg: "Amount exceeds available balance."}
	}

	var existingID int
	err = tx.QueryRow(ctx, `SELECT application_id FROM payment_application WHERE payment_id = $1 AND invoice_id = $2 AND application_deleted_at IS NULL`,
		lp.internalID, li.internalID).Scan(&existingID)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		if _, err := tx.Exec(ctx, `
			INSERT INTO payment_application (payment_id, invoice_id, application_amount, application_created_by)
			VALUES ($1,$2,$3,$4)`, lp.internalID, li.internalID, round2(amount), nullableInt(actorEmployeeID)); err != nil {
			return nil, fmt.Errorf("insert payment application: %w", err)
		}
	case err != nil:
		return nil, fmt.Errorf("check existing application: %w", err)
	default:
		if _, err := tx.Exec(ctx, `
			UPDATE payment_application SET application_amount = application_amount + $1, application_record_version = application_record_version + 1
			WHERE application_id = $2`, round2(amount), existingID); err != nil {
			return nil, fmt.Errorf("increase payment application: %w", err)
		}
	}

	if err := recomputePayment(ctx, tx, lp.internalID, lp.amount, actorEmployeeID); err != nil {
		return nil, err
	}
	if err := recomputeInvoice(ctx, tx, li, "payment", actorEmployeeID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO payment_history (payment_id, from_status_id, to_status_id, action, actor_employee_id)
		SELECT payment_id, payment_status, payment_status, 'apply', $2 FROM payment WHERE payment_id = $1`,
		lp.internalID, nullableInt(actorEmployeeID)); err != nil {
		return nil, fmt.Errorf("insert payment apply history: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit apply: %w", err)
	}
	return Get(ctx, pool, paymentUUID)
}

// Unapply reverses the live application between paymentUUID and invoiceUUID
// (soft-deletes it), recomputing both rollups. No invoice-status gate: a
// reversal must be possible regardless of the invoice's current status.
func Unapply(ctx context.Context, pool *pgxpool.Pool, paymentUUID, invoiceUUID string, actorEmployeeID int) (*Payment, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin unapply: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	lp, err := lockPaymentForUpdate(ctx, tx, paymentUUID)
	if err != nil {
		return nil, err
	}
	li, err := lockInvoiceForUpdate(ctx, tx, invoiceUUID)
	if err != nil {
		return nil, err
	}

	tag, err := tx.Exec(ctx, `
		UPDATE payment_application SET application_deleted_at = NOW(), application_deleted_by = $1
		WHERE payment_id = $2 AND invoice_id = $3 AND application_deleted_at IS NULL`,
		nullableInt(actorEmployeeID), lp.internalID, li.internalID)
	if err != nil {
		return nil, fmt.Errorf("unapply: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return nil, ClientError{Msg: "No live application between this payment and invoice."}
	}

	if err := recomputePayment(ctx, tx, lp.internalID, lp.amount, actorEmployeeID); err != nil {
		return nil, err
	}
	if err := recomputeInvoice(ctx, tx, li, "unapply", actorEmployeeID); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO payment_history (payment_id, from_status_id, to_status_id, action, actor_employee_id)
		SELECT payment_id, payment_status, payment_status, 'unapply', $2 FROM payment WHERE payment_id = $1`,
		lp.internalID, nullableInt(actorEmployeeID)); err != nil {
		return nil, fmt.Errorf("insert payment unapply history: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit unapply: %w", err)
	}
	return Get(ctx, pool, paymentUUID)
}
```

- [ ] **Step 5:** Run `go test ./payment/... -tags dbtest -run 'TestApply|TestUnapply|TestCreate' -v` → PASS (this also unblocks Task 3.2's tests).
- [ ] **Step 6:** Commit — `git commit -m "feat(payment): add apply/unapply with invoice AR rollup recompute"`.

### Task 3.4: Store — `Update` / `SoftDelete`
- [ ] **Step 1:** Write failing `dbtest` tests appended to `payment/store_test.go`:

```go
func TestUpdate_NonMonetaryFieldsOnly(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID := seedCustomer(t, pool)
	methodID := firstMethodID(t, pool)
	p, err := Create(ctx, pool, CreatePaymentInput{CustomerUUID: custUUID, MethodID: methodID, Amount: 100}, 1)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	updated, err := Update(ctx, pool, p.ID, UpdatePaymentInput{MethodID: methodID, ReferenceNumber: "Check #99", Memo: "updated"}, 1)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.ReferenceNumber != "Check #99" || updated.Amount != 100 {
		t.Fatalf("expected reference updated and amount unchanged, got %+v", updated)
	}
}

func TestSoftDelete_BlockedWithLiveApplications(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID, invUUID := seedSentInvoice(t, pool, 100)
	methodID := firstMethodID(t, pool)
	p, err := Create(ctx, pool, CreatePaymentInput{CustomerUUID: custUUID, MethodID: methodID, Amount: 100}, 1)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := Apply(ctx, pool, p.ID, invUUID, 50, 1); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if err := SoftDelete(ctx, pool, p.ID, 1); err == nil {
		t.Fatal("expected delete to be blocked while a live application exists")
	}
	if _, err := Unapply(ctx, pool, p.ID, invUUID, 1); err != nil {
		t.Fatalf("unapply: %v", err)
	}
	if err := SoftDelete(ctx, pool, p.ID, 1); err != nil {
		t.Fatalf("expected delete to succeed once unapplied: %v", err)
	}
	if _, err := Get(ctx, pool, p.ID); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}
```

- [ ] **Step 2:** Run → FAIL.
- [ ] **Step 3:** Implement `payment/store_update.go`:

```go
package payment

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func internalIDByUUID(ctx context.Context, pool *pgxpool.Pool, id string) (int, error) {
	var internalID int
	err := pool.QueryRow(ctx,
		`SELECT payment_id FROM payment WHERE payment_uuid = $1 AND payment_deleted_at IS NULL`, id).Scan(&internalID)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ErrNotFound
	}
	if err != nil {
		return 0, fmt.Errorf("resolve payment: %w", err)
	}
	return internalID, nil
}

// Update edits non-monetary fields only (spec AD-10: amount is immutable
// post-creation — void + recreate to correct it).
func Update(ctx context.Context, pool *pgxpool.Pool, id string, in UpdatePaymentInput, actorEmployeeID int) (*Payment, error) {
	internalID, err := internalIDByUUID(ctx, pool, id)
	if err != nil {
		return nil, err
	}
	if err := resolveMethod(ctx, pool, in.MethodID); err != nil {
		return nil, err
	}
	custom := in.CustomFields
	if custom == nil {
		custom = map[string]any{}
	}
	if err := validateCustom(ctx, pool, custom); err != nil {
		return nil, err
	}
	_, err = pool.Exec(ctx, `
		UPDATE payment SET
			payment_method = $1, payment_reference_number = $2, payment_date = COALESCE($3, payment_date),
			payment_currency = $4, payment_owner_id = COALESCE($5, payment_owner_id),
			payment_memo = $6, payment_internal_notes = $7, payment_custom_fields = $8,
			payment_updated_at = NOW(), payment_updated_by = $9, payment_record_version = payment_record_version + 1
		WHERE payment_id = $10`,
		in.MethodID, in.ReferenceNumber, in.PaymentDate, in.CurrencyID, in.OwnerEmployeeID,
		in.Memo, in.InternalNotes, custom, nullableInt(actorEmployeeID), internalID)
	if err != nil {
		return nil, fmt.Errorf("update payment: %w", err)
	}
	return Get(ctx, pool, id)
}

const systemEmployeeID = 1

// SoftDelete marks a payment deleted (paired deleted_at/deleted_by). Blocked
// (409-mapped ClientError) while any live application references it — must
// Unapply (or Transition to VOID, which cascades) first (spec AD-11).
func SoftDelete(ctx context.Context, pool *pgxpool.Pool, id string, actorEmployeeID int) error {
	internalID, err := internalIDByUUID(ctx, pool, id)
	if err != nil {
		return err
	}
	var liveApplications int
	if err := pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM payment_application WHERE payment_id = $1 AND application_deleted_at IS NULL`,
		internalID).Scan(&liveApplications); err != nil {
		return fmt.Errorf("count live applications: %w", err)
	}
	if liveApplications > 0 {
		return ClientError{Msg: "Cannot delete a payment with live applications; unapply or void it first."}
	}
	deletedBy := actorEmployeeID
	if deletedBy == 0 {
		deletedBy = systemEmployeeID
	}
	tag, err := pool.Exec(ctx, `
		UPDATE payment SET payment_deleted_at = NOW(), payment_deleted_by = $1
		WHERE payment_uuid = $2 AND payment_deleted_at IS NULL`, deletedBy, id)
	if err != nil {
		return fmt.Errorf("delete payment: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}
```

- [ ] **Step 4:** Run to PASS.
- [ ] **Step 5:** Commit — `git commit -m "feat(payment): add update and guarded soft-delete"`.

### Task 3.5: Store — `Transition` (VOID cascades unapply)
- [ ] **Step 1:** Write failing `dbtest` tests appended to `payment/store_test.go`:

```go
func TestTransition_HappyPath(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID := seedCustomer(t, pool)
	methodID := firstMethodID(t, pool)
	p, err := Create(ctx, pool, CreatePaymentInput{CustomerUUID: custUUID, MethodID: methodID, Amount: 100}, 1)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	p, err = Transition(ctx, pool, p.ID, "APPV", 1)
	if err != nil {
		t.Fatalf("transition to APPV: %v", err)
	}
	if p.StatusCode != "APPV" {
		t.Fatalf("expected APPV, got %s", p.StatusCode)
	}
}

func TestTransition_VoidCascadesUnapply(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID, invUUID := seedSentInvoice(t, pool, 100)
	methodID := firstMethodID(t, pool)
	p, err := Create(ctx, pool, CreatePaymentInput{CustomerUUID: custUUID, MethodID: methodID, Amount: 100}, 1)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := Apply(ctx, pool, p.ID, invUUID, 100, 1); err != nil {
		t.Fatalf("apply: %v", err)
	}
	p2, err := Transition(ctx, pool, p.ID, "VOID", 1)
	if err != nil {
		t.Fatalf("void: %v", err)
	}
	if p2.StatusCode != "VOID" || p2.AppliedTotal != 0 || p2.UnappliedAmount != 100 {
		t.Fatalf("expected voided/0/100, got status=%s applied=%v unapplied=%v", p2.StatusCode, p2.AppliedTotal, p2.UnappliedAmount)
	}
	inv, _ := invoice.Get(ctx, pool, invUUID)
	if inv.StatusCode != "SENT" || inv.AmountPaid != 0 {
		t.Fatalf("expected invoice reverted to SENT/0, got status=%s paid=%v", inv.StatusCode, inv.AmountPaid)
	}
	// A voided payment can no longer be applied.
	if _, err := Apply(ctx, pool, p.ID, invUUID, 10, 1); err == nil {
		t.Fatal("expected error applying a voided payment")
	}
}
```

- [ ] **Step 2:** Run → FAIL.
- [ ] **Step 3:** Implement `payment/store_transition.go`:

```go
package payment

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Transition moves a payment to toStatusCode after validating the move
// against the static transition map. Moving to VOID first reverses every
// live application on this payment (spec AD-9) — each reversal is its own
// Unapply-shaped step inside the same transaction as the status change.
func Transition(ctx context.Context, pool *pgxpool.Pool, id, toStatusCode string, actorEmployeeID int) (*Payment, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transition: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var internalID, curStatusID, typeID int
	var curStatusCode string
	err = tx.QueryRow(ctx, `
		SELECT p.payment_id, p.payment_status, p.record_type, rs.record_status_code
		FROM payment p
		JOIN lkp_record_status rs ON rs.record_status_id = p.payment_status
		WHERE p.payment_uuid = $1 AND p.payment_deleted_at IS NULL
		FOR UPDATE OF p`, id,
	).Scan(&internalID, &curStatusID, &typeID, &curStatusCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("resolve payment for transition: %w", err)
	}
	if err := ValidateTransition(curStatusCode, toStatusCode); err != nil {
		return nil, err
	}
	toStatusID, err := statusIDByCode(ctx, pool, typeID, toStatusCode)
	if err != nil {
		return nil, ClientError{Msg: "Unknown target status: " + toStatusCode}
	}

	if toStatusCode == "VOID" {
		rows, err := tx.Query(ctx, `SELECT invoice_id FROM payment_application WHERE payment_id = $1 AND application_deleted_at IS NULL`, internalID)
		if err != nil {
			return nil, fmt.Errorf("list live applications: %w", err)
		}
		var invoiceInternalIDs []int
		for rows.Next() {
			var iid int
			if err := rows.Scan(&iid); err != nil {
				rows.Close()
				return nil, fmt.Errorf("scan application invoice id: %w", err)
			}
			invoiceInternalIDs = append(invoiceInternalIDs, iid)
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("list live applications: %w", err)
		}

		for _, invInternalID := range invoiceInternalIDs {
			var li lockedInvoice
			li.internalID = invInternalID
			if err := tx.QueryRow(ctx, `
				SELECT rs.record_status_code, i.invoice_grand_total, i.invoice_amount_paid
				FROM invoice i JOIN lkp_record_status rs ON rs.record_status_id = i.invoice_status
				WHERE i.invoice_id = $1 FOR UPDATE OF i`, invInternalID,
			).Scan(&li.statusCode, &li.grandTotal, &li.amountPaid); err != nil {
				return nil, fmt.Errorf("lock invoice for void cascade: %w", err)
			}
			if _, err := tx.Exec(ctx, `
				UPDATE payment_application SET application_deleted_at = NOW(), application_deleted_by = $1
				WHERE payment_id = $2 AND invoice_id = $3 AND application_deleted_at IS NULL`,
				nullableInt(actorEmployeeID), internalID, invInternalID); err != nil {
				return nil, fmt.Errorf("cascade-unapply: %w", err)
			}
			if err := recomputeInvoice(ctx, tx, li, "unapply", actorEmployeeID); err != nil {
				return nil, err
			}
		}
		if len(invoiceInternalIDs) > 0 {
			// Every live application on this payment was just reversed above, so
			// the payment's own rollup needs recomputing too. recomputePayment
			// needs the payment's amount (not yet in scope here), so load it first.
			var amt float64
			if err := tx.QueryRow(ctx, `SELECT payment_amount FROM payment WHERE payment_id = $1`, internalID).Scan(&amt); err != nil {
				return nil, fmt.Errorf("reload payment amount: %w", err)
			}
			if err := recomputePayment(ctx, tx, internalID, amt, actorEmployeeID); err != nil {
				return nil, err
			}
		}
	}

	if _, err := tx.Exec(ctx, `
		UPDATE payment SET payment_status = $1, payment_updated_at = NOW(),
			payment_updated_by = $2, payment_record_version = payment_record_version + 1
		WHERE payment_id = $3`, toStatusID, nullableInt(actorEmployeeID), internalID); err != nil {
		return nil, fmt.Errorf("update payment status: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO payment_history (payment_id, from_status_id, to_status_id, action, actor_employee_id)
		VALUES ($1, $2, $3, 'transition', $4)`, internalID, curStatusID, toStatusID, nullableInt(actorEmployeeID)); err != nil {
		return nil, fmt.Errorf("insert payment transition history: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit transition: %w", err)
	}
	return Get(ctx, pool, id)
}
```

- [ ] **Step 4:** Run to PASS.
- [ ] **Step 5:** Commit — `git commit -m "feat(payment): add transition with void-cascade unapply"`.

### Task 3.6: `QuickPay` (legacy-endpoint wrapper)
- [ ] **Step 1:** Write a failing `dbtest` test appended to `payment/store_test.go`:

```go
func TestQuickPay(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	_, invUUID := seedSentInvoice(t, pool, 100)

	inv, err := QuickPay(ctx, pool, invUUID, 40, 1)
	if err != nil {
		t.Fatalf("quickpay 1: %v", err)
	}
	if inv.AmountPaid != 40 || inv.BalanceDue != 60 || inv.StatusCode != "PART" {
		t.Fatalf("expected paid=40 balance=60 status=PART, got paid=%v balance=%v status=%s", inv.AmountPaid, inv.BalanceDue, inv.StatusCode)
	}

	inv, err = QuickPay(ctx, pool, invUUID, 60, 1)
	if err != nil {
		t.Fatalf("quickpay 2: %v", err)
	}
	if inv.AmountPaid != 100 || inv.BalanceDue != 0 || inv.StatusCode != "PAID" {
		t.Fatalf("expected paid=100 balance=0 status=PAID, got paid=%v balance=%v status=%s", inv.AmountPaid, inv.BalanceDue, inv.StatusCode)
	}

	if _, err := QuickPay(ctx, pool, invUUID, 10, 1); err == nil {
		t.Fatal("expected error overpaying a PAID invoice")
	}
}
```

- [ ] **Step 2:** Run → FAIL.
- [ ] **Step 3:** Implement `payment/quickpay.go`. It needs the invoice's customer UUID and a "generic" payment method to create a payment against — resolve method by the seeded `OTHR` code, and the customer via a join on `invoice`:

```go
package payment

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/invoice"
)

// QuickPay is the legacy-endpoint wrapper behind POST /invoices/{uuid}/payment
// (spec AD-5). It creates a payment at status APPV (skipping PEND — this
// single-call endpoint implies the money is already confirmed) and applies it
// to the given invoice in one call, reusing Apply for the balance math and
// "no silent clamp" overpay rejection. Returns the updated invoice, matching
// the pre-existing response shape callers of this endpoint expect.
func QuickPay(ctx context.Context, pool *pgxpool.Pool, invoiceUUID string, amount float64, actorEmployeeID int) (*invoice.Invoice, error) {
	if amount <= 0 {
		return nil, ClientError{Msg: "amount must be positive."}
	}
	var customerUUID string
	err := pool.QueryRow(ctx, `
		SELECT c.customer_uuid FROM invoice i JOIN customer c ON c.customer_id = i.invoice_customer_id
		WHERE i.invoice_uuid = $1 AND i.invoice_deleted_at IS NULL`, invoiceUUID).Scan(&customerUUID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ClientError{Msg: "Unknown or deleted invoice."}
	}
	if err != nil {
		return nil, fmt.Errorf("resolve invoice customer: %w", err)
	}

	var methodID int
	if err := pool.QueryRow(ctx, `SELECT payment_method_id FROM lkp_payment_method WHERE payment_method_code = 'OTHR'`).Scan(&methodID); err != nil {
		return nil, fmt.Errorf("resolve default payment method: %w", err)
	}

	p, err := Create(ctx, pool, CreatePaymentInput{
		CustomerUUID: customerUUID, MethodID: methodID, Amount: amount,
		Memo: "Quick payment via invoice", ReferenceNumber: "",
	}, actorEmployeeID)
	if err != nil {
		return nil, err
	}
	typeID, err := typeIDByCode(ctx, pool, "PYMT")
	if err != nil {
		return nil, err
	}
	appvStatusID, err := statusIDByCode(ctx, pool, typeID, "APPV")
	if err != nil {
		return nil, err
	}
	if _, err := pool.Exec(ctx, `UPDATE payment SET payment_status = $1 WHERE payment_uuid = $2`, appvStatusID, p.ID); err != nil {
		return nil, fmt.Errorf("promote quickpay to APPV: %w", err)
	}

	if _, err := Apply(ctx, pool, p.ID, invoiceUUID, amount, actorEmployeeID); err != nil {
		return nil, err
	}
	return invoice.Get(ctx, pool, invoiceUUID)
}
```

- [ ] **Step 4:** Run to PASS.
- [ ] **Step 5:** Commit — `git commit -m "feat(payment): add QuickPay wrapper for the legacy invoice payment endpoint"`.

### Task 3.7: Resolver (FieldResolver + SortResolver + SearchResolver)
- [ ] **Step 1:** Write failing resolver tests `payment/resolver_test.go` (mirror `invoice/resolver_test.go`):

```go
package payment

import (
	"strings"
	"testing"

	"stonesuite-backend/query"
)

func TestResolver_Resolve(t *testing.T) {
	r := resolver{}
	expr, dt, ok := r.Resolve("document_number")
	if !ok || dt != query.TypeString || !strings.Contains(expr, "payment_number") {
		t.Errorf("expected valid string expression for document_number, got %q %v %v", expr, dt, ok)
	}
	expr, dt, ok = r.Resolve("cf:xyz_123")
	if !ok || dt != query.TypeString || !strings.Contains(expr, "xyz_123") {
		t.Errorf("expected valid custom field expression, got %q %v %v", expr, dt, ok)
	}
	if _, _, ok = r.Resolve("cf:xyz'"); ok {
		t.Error("expected invalid custom field to be rejected")
	}
	if _, _, ok = r.Resolve("nope"); ok {
		t.Error("expected unknown field to be rejected")
	}
}

func TestResolver_SortExpr(t *testing.T) {
	r := resolver{}
	expr, dt, ok := r.SortExpr("amount")
	if !ok || dt != query.TypeNumber || expr != "p.payment_amount" {
		t.Errorf("expected valid sort expression for amount, got %q %v %v", expr, dt, ok)
	}
	if _, _, ok := r.SortExpr("cf:xyz"); ok {
		t.Error("expected custom field to be rejected for sorting")
	}
}

func TestResolver_SearchPredicate(t *testing.T) {
	r := resolver{}
	pred := r.SearchPredicate("$1")
	if !strings.Contains(pred, "p.payment_number ILIKE") || !strings.Contains(pred, "c.customer_name ILIKE") {
		t.Errorf("search predicate missing expected conditions: %s", pred)
	}
}
```

- [ ] **Step 2:** Run → FAIL.
- [ ] **Step 3:** Implement `payment/resolver.go` per spec §10's mapping table exactly:

```go
package payment

import (
	"regexp"
	"strings"

	"stonesuite-backend/query"
)

var validCustomKey = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

type resolver struct{}

type resolved struct {
	expr string
	dt   query.DataType
}

var systemFields = map[string]resolved{
	"id":                {"p.payment_uuid::text", query.TypeString},
	"document_number":   {"COALESCE(p.payment_number,'')", query.TypeString},
	"record_number":     {"COALESCE(p.payment_number,'')", query.TypeString},
	"customer_id":       {"p.payment_customer_id::text", query.TypeString},
	"status":            {"p.payment_status::text", query.TypeString},
	"method_id":         {"p.payment_method::text", query.TypeString},
	"reference_number":  {"p.payment_reference_number", query.TypeString},
	"payment_date":      {"p.payment_date", query.TypeDate},
	"currency_id":       {"p.payment_currency::text", query.TypeString},
	"amount":            {"p.payment_amount", query.TypeNumber},
	"applied_total":     {"p.payment_applied_total", query.TypeNumber},
	"unapplied_amount":  {"p.payment_unapplied_amount", query.TypeNumber},
	"owner_id":          {"p.payment_owner_id::text", query.TypeString},
	"created_by":        {"p.payment_created_by::text", query.TypeString},
	"updated_by":        {"p.payment_updated_by::text", query.TypeString},
	"created_at":        {"p.payment_created_at", query.TypeDate},
	"updated_at":        {"p.payment_updated_at", query.TypeDate},
}

func (resolver) Resolve(key string) (string, query.DataType, bool) {
	if s, ok := systemFields[key]; ok {
		return s.expr, s.dt, true
	}
	if k, ok := strings.CutPrefix(key, "cf:"); ok && validCustomKey.MatchString(k) {
		return "p.payment_custom_fields->>'" + k + "'", query.TypeString, true
	}
	return "", "", false
}

var _ query.FieldResolver = resolver{}

var sortFields = map[string]resolved{
	"document_number":  {"COALESCE(p.payment_number,'')", query.TypeString},
	"record_number":    {"COALESCE(p.payment_number,'')", query.TypeString},
	"payment_date":     {"p.payment_date", query.TypeDate},
	"amount":           {"p.payment_amount", query.TypeNumber},
	"unapplied_amount": {"p.payment_unapplied_amount", query.TypeNumber},
	"status":           {"p.payment_status", query.TypeNumber},
	"customer_id":      {"p.payment_customer_id", query.TypeNumber},
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
		"p.payment_number ILIKE '%'||" + ph + "||'%'" +
		" OR p.payment_reference_number ILIKE '%'||" + ph + "||'%'" +
		" OR p.payment_memo ILIKE '%'||" + ph + "||'%'" +
		" OR EXISTS (SELECT 1 FROM customer c WHERE c.customer_id = p.payment_customer_id" +
		"   AND (c.customer_name ILIKE '%'||" + ph + "||'%' OR c.customer_doc_num ILIKE '%'||" + ph + "||'%'))" +
		")"
}

var _ query.SearchResolver = resolver{}
```

- [ ] **Step 4:** Run to PASS.
- [ ] **Step 5:** Commit — `git commit -m "feat(payment): add field/sort/search resolver"`.

### Task 3.8: Store — `Search` (scope + filter + keyset)
- [ ] **Step 1:** Write failing `dbtest` tests appended to `payment/store_test.go`:

```go
// customerInternalID resolves a customer's internal serial id from its uuid —
// the "customer_id" filter key compares against that id cast to text (see
// systemFields["customer_id"] in resolver.go), matching how
// invoice/search_test.go resolves the same filter.
func customerInternalID(t *testing.T, pool *pgxpool.Pool, custUUID string) string {
	t.Helper()
	var id int
	if err := pool.QueryRow(context.Background(),
		`SELECT customer_id FROM customer WHERE customer_uuid = $1`, custUUID).Scan(&id); err != nil {
		t.Fatalf("resolve customer internal id: %v", err)
	}
	return strconv.Itoa(id)
}

func TestSearch_FilterAndSort(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID := seedCustomer(t, pool)
	methodID := firstMethodID(t, pool)
	if _, err := Create(ctx, pool, CreatePaymentInput{CustomerUUID: custUUID, MethodID: methodID, Amount: 300}, 1); err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := Create(ctx, pool, CreatePaymentInput{CustomerUUID: custUUID, MethodID: methodID, Amount: 100}, 1); err != nil {
		t.Fatalf("create: %v", err)
	}
	page, err := Search(ctx, pool, "all", "", query.Request{
		Filters: []query.Clause{{Field: "customer_id", Op: query.OpEq, Value: customerInternalID(t, pool, custUUID)}},
		Sort:    []query.SortKey{{Field: "amount", Dir: query.DirDesc}},
	})
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(page.Records) < 2 {
		t.Fatalf("expected at least 2 records, got %d", len(page.Records))
	}
	if page.Records[0].Amount < page.Records[1].Amount {
		t.Fatalf("expected DESC order by amount, got %v then %v", page.Records[0].Amount, page.Records[1].Amount)
	}
}

func TestSearch_UnknownFieldIsInvalidFilterError(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	_, err := Search(ctx, pool, "all", "", query.Request{Filters: []query.Clause{{Field: "nope", Op: query.OpEq, Value: "x"}}})
	if _, ok := err.(*query.InvalidFilterError); !ok {
		t.Fatalf("expected *query.InvalidFilterError, got %T: %v", err, err)
	}
}
```

  This test file's build tag header (Task 3.2 Step 1) already imports `pgxpool`; add `"strconv"` and `"stonesuite-backend/query"` to that same import block for these two tests.
- [ ] **Step 2:** Run → FAIL.
- [ ] **Step 3:** Implement `payment/search.go` mirroring `invoice/search.go`'s `Search`/`sortValue` shape exactly, substituting `payment`/`Payment`/`paymentMeta`:

```go
package payment

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/query"
	"stonesuite-backend/workflow"
)

// Search lists live payments with server-side filter/sort/global-search +
// keyset pagination. Note: Search returns headers only (Applications is
// always an empty slice on each record).
func Search(ctx context.Context, pool *pgxpool.Pool, scope, actorIdentityID string, req query.Request) (Page, error) {
	where := []string{"p.payment_deleted_at IS NULL"}
	args := []any{}
	nextIdx := 1
	if scope == string(authz.ScopeOwn) || scope == string(authz.ScopeTeam) {
		empID, found := workflow.EmployeeIDByIdentity(ctx, pool, actorIdentityID)
		if !found {
			return Page{}, nil
		}
		where = append(where, fmt.Sprintf("p.payment_owner_id = $%d", nextIdx))
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

	q := headerSelect + " WHERE " + strings.Join(where, " AND ") +
		" ORDER BY " + built.OrderBy + " LIMIT " + strconv.Itoa(built.EffLimit+1)

	rows, err := pool.Query(ctx, q, args...)
	if err != nil {
		return Page{}, fmt.Errorf("search payments: %w", err)
	}
	defer rows.Close()
	out := []Payment{}
	metas := []paymentMeta{}
	for rows.Next() {
		p, meta, err := scanPayment(rows)
		if err != nil {
			return Page{}, fmt.Errorf("scan payment: %w", err)
		}
		out = append(out, *p)
		metas = append(metas, meta)
	}
	if err := rows.Err(); err != nil {
		return Page{}, fmt.Errorf("search payments: %w", err)
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

func sortValue(p Payment, meta paymentMeta, field string) any {
	switch field {
	case "updated_at":
		return p.UpdatedAt
	case "document_number", "record_number":
		return p.Number
	case "payment_date":
		return p.PaymentDate
	case "amount":
		return p.Amount
	case "unapplied_amount":
		return p.UnappliedAmount
	case "status":
		return meta.statusID
	case "customer_id":
		return meta.customerID
	default: // created_at (default)
		return p.CreatedAt
	}
}
```

- [ ] **Step 4:** Run to PASS. Dispatch the **filter-invariant-checker** agent on `payment/resolver.go` + `payment/search.go` before committing.
- [ ] **Step 5:** Commit — `git commit -m "feat(payment): add keyset search with scope + filter + sort + global search"`.

---

## Phase 4 — HTTP layer

### Task 4.1: `PaymentOps` CRUD + list handlers
- [ ] **Step 1:** Write failing handler tests `controllers/payment_test.go` (mirror `controllers/invoice_test.go`):

```go
package controllers

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"stonesuite-backend/payment"
	"stonesuite-backend/query"

	"github.com/stretchr/testify/assert"
)

func TestPaymentOps_RequiresAuth(t *testing.T) {
	h := NewPaymentOps()
	handlers := map[string]http.HandlerFunc{
		"Create":     h.Create,
		"Get":        h.Get,
		"Update":     h.Update,
		"Delete":     h.Delete,
		"List":       h.List,
		"Search":     h.Search,
		"Transition": h.Transition,
		"Apply":      h.Apply,
		"Unapply":    h.Unapply,
		"Audit":      h.Audit,
	}
	for name, fn := range handlers {
		t.Run(name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/tenant/payments", nil)
			req.SetPathValue("uuid", "does-not-matter")
			rr := httptest.NewRecorder()
			fn(rr, req)
			assert.Equal(t, http.StatusUnauthorized, rr.Code, "%s must require auth", name)
		})
	}
}

func TestPaymentFail_MapsStoreErrorsToHTTPStatus(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
	}{
		{"not found", payment.ErrNotFound, http.StatusNotFound},
		{"invalid transition", payment.ErrInvalidTransition, http.StatusConflict},
		{"client error", payment.ClientError{Msg: "bad input"}, http.StatusBadRequest},
		{"invalid filter", &query.InvalidFilterError{Field: "x", Msg: "unknown field"}, http.StatusBadRequest},
		{"other", errors.New("boom"), http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			paymentFail(rr, tt.err, "server error")
			assert.Equal(t, tt.wantStatus, rr.Code)
		})
	}
}
```

- [ ] **Step 2:** Run → FAIL.
- [ ] **Step 3:** Implement `controllers/payment.go`, mirroring `controllers/invoice.go`'s `InvoiceOps` file-for-file (same `authPayment`/`authPaymentByUUID` shape as `authInvoice`/`authInvoiceByUUID`, same `paymentFail` shape as `invoiceFail`):

```go
package controllers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/middleware"
	"stonesuite-backend/models"
	"stonesuite-backend/payment"
	"stonesuite-backend/query"
	"stonesuite-backend/tenancy"
)

type PaymentOps struct{}

func NewPaymentOps() *PaymentOps { return &PaymentOps{} }

func (h *PaymentOps) authPayment(w http.ResponseWriter, r *http.Request, action authz.Action) (*pgxpool.Pool, string, authz.Scope, bool) {
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
	decision, err := authz.Check(r.Context(), pool, payload.ID, authz.ResourcePayment, action)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, "", "", false
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", payload.ID, "resource", string(authz.ResourcePayment), "action", string(action))
		fail(w, http.StatusForbidden, "You do not have permission to "+string(action)+" payments.")
		return nil, "", "", false
	}
	return pool, payload.ID, decision.Scope, true
}

func (h *PaymentOps) authPaymentByUUID(w http.ResponseWriter, r *http.Request, uuid string, action authz.Action) (*pgxpool.Pool, string, authz.Scope, bool) {
	pool, identityID, scope, ok := h.authPayment(w, r, action)
	if !ok {
		return nil, "", "", false
	}
	if scope == authz.ScopeAll {
		return pool, identityID, scope, true
	}
	p, err := payment.Get(r.Context(), pool, uuid)
	if errors.Is(err, payment.ErrNotFound) {
		fail(w, http.StatusNotFound, "Payment not found.")
		return nil, "", "", false
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load payment.")
		return nil, "", "", false
	}
	allowed, aerr := recordInScope(r.Context(), pool, scope, identityID, p.OwnerUserID, "")
	if aerr != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, "", "", false
	}
	if !allowed {
		logSecurityEvent(r, "idor_denied",
			"identity", identityID, "record", uuid, "resource", string(authz.ResourcePayment),
			"action", string(action), "scope", string(scope))
		fail(w, http.StatusNotFound, "Payment not found.")
		return nil, "", "", false
	}
	return pool, identityID, scope, true
}

func paymentFail(w http.ResponseWriter, err error, serverMsg string) {
	switch {
	case errors.Is(err, payment.ErrNotFound):
		fail(w, http.StatusNotFound, "Payment not found.")
	case errors.Is(err, payment.ErrInvalidTransition):
		fail(w, http.StatusConflict, err.Error())
	default:
		var ce payment.ClientError
		if errors.As(err, &ce) {
			fail(w, http.StatusBadRequest, ce.Error())
			return
		}
		var ife *query.InvalidFilterError
		if errors.As(err, &ife) {
			fail(w, http.StatusBadRequest, ife.Error())
			return
		}
		fail(w, http.StatusInternalServerError, serverMsg)
	}
}

func (h *PaymentOps) Create(w http.ResponseWriter, r *http.Request) {
	pool, identityID, _, ok := h.authPayment(w, r, authz.ActionCreate)
	if !ok {
		return
	}
	var in payment.CreatePaymentInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	empID := resolveEmployeeID(r, identityID)
	p, err := payment.Create(r.Context(), pool, in, empID)
	if err != nil {
		paymentFail(w, err, "Failed to create payment.")
		return
	}
	auditPayment(r, pool, empID, "create", p.ID, nil, p)
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "payment": p})
}

func (h *PaymentOps) Get(w http.ResponseWriter, r *http.Request) {
	pool, _, _, ok := h.authPaymentByUUID(w, r, r.PathValue("uuid"), authz.ActionRead)
	if !ok {
		return
	}
	p, err := payment.Get(r.Context(), pool, r.PathValue("uuid"))
	if err != nil {
		paymentFail(w, err, "Failed to load payment.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "payment": p})
}

func (h *PaymentOps) Update(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, identityID, _, ok := h.authPaymentByUUID(w, r, id, authz.ActionUpdate)
	if !ok {
		return
	}
	var in payment.UpdatePaymentInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	before, _ := payment.Get(r.Context(), pool, id)
	empID := resolveEmployeeID(r, identityID)
	after, err := payment.Update(r.Context(), pool, id, in, empID)
	if err != nil {
		paymentFail(w, err, "Failed to update payment.")
		return
	}
	auditPayment(r, pool, empID, "update", id, before, after)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "payment": after})
}

func (h *PaymentOps) Delete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, identityID, _, ok := h.authPaymentByUUID(w, r, id, authz.ActionDelete)
	if !ok {
		return
	}
	before, _ := payment.Get(r.Context(), pool, id)
	empID := resolveEmployeeID(r, identityID)
	if err := payment.SoftDelete(r.Context(), pool, id, empID); err != nil {
		paymentFail(w, err, "Failed to delete payment.")
		return
	}
	auditPayment(r, pool, empID, "delete", id, before, nil)
	writeJSON(w, http.StatusOK, models.APIResponse{Success: true, Message: "Payment deleted."})
}

func (h *PaymentOps) List(w http.ResponseWriter, r *http.Request) {
	pool, identityID, scope, ok := h.authPayment(w, r, authz.ActionRead)
	if !ok {
		return
	}
	req := query.Request{Cursor: r.URL.Query().Get("cursor")}
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil {
			req.Limit = n
		}
	}
	page, err := payment.Search(r.Context(), pool, string(scope), identityID, req)
	if err != nil {
		paymentFail(w, err, "Failed to list payments.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "scope": scope, "records": page.Records,
		"nextCursor": page.NextCursor, "hasMore": page.HasMore,
	})
}

func (h *PaymentOps) Search(w http.ResponseWriter, r *http.Request) {
	pool, identityID, scope, ok := h.authPayment(w, r, authz.ActionRead)
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
	page, err := payment.Search(r.Context(), pool, string(scope), identityID, req)
	if err != nil {
		paymentFail(w, err, "Failed to search payments.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "scope": scope, "records": page.Records,
		"nextCursor": page.NextCursor, "hasMore": page.HasMore,
	})
}
```

- [ ] **Step 4:** Run `go build ./... && go test ./controllers/ -run Payment -v` → PASS.
- [ ] **Step 5:** Commit — `git commit -m "feat(payment): add CRUD + list/search HTTP handlers"`.

### Task 4.2: Transition / Apply / Unapply handlers
- [ ] **Step 1:** Handler tests are already covered by `TestPaymentOps_RequiresAuth` (Task 4.1) for the auth-required case. Add one focused test to `controllers/payment_test.go` for request-body validation without a DB (mirrors how `controllers/invoice_test.go` keeps DB-dependent happy paths in the `dbtest` integration layer, not here):

```go
func TestPaymentOps_Apply_RequiresInvoiceUuidAndAmount(t *testing.T) {
	h := NewPaymentOps()
	req := httptest.NewRequest(http.MethodPost, "/api/tenant/payments/x/apply", strings.NewReader(`{}`))
	req.SetPathValue("uuid", "does-not-matter")
	rr := httptest.NewRecorder()
	h.Apply(rr, req)
	// No auth context on the request, so this must still 401 before even
	// reaching body validation — confirms auth precedes body parsing.
	assert.Equal(t, http.StatusUnauthorized, rr.Code)
}
```
  (Add `"strings"` to the test file's imports.)
- [ ] **Step 2:** Implement `controllers/payment_transition.go`:

```go
package controllers

import (
	"encoding/json"
	"net/http"

	"stonesuite-backend/authz"
	"stonesuite-backend/payment"
)

type payTransitionRequest struct {
	ToStatusCode string `json:"toStatusCode"`
}

func (h *PaymentOps) Transition(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, identityID, _, ok := h.authPaymentByUUID(w, r, id, authz.ActionTransition)
	if !ok {
		return
	}
	var req payTransitionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.ToStatusCode == "" {
		fail(w, http.StatusBadRequest, "toStatusCode is required.")
		return
	}
	empID := resolveEmployeeID(r, identityID)
	p, err := payment.Transition(r.Context(), pool, id, req.ToStatusCode, empID)
	if err != nil {
		paymentFail(w, err, "Failed to transition payment.")
		return
	}
	auditPayment(r, pool, empID, "transition", id, nil, p)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "payment": p})
}

type payApplyRequest struct {
	InvoiceUUID string  `json:"invoiceUuid"`
	Amount      float64 `json:"amount"`
}

// Apply applies part of a payment's unapplied balance to an invoice. This
// mutates the target invoice's AR balance, so it requires invoice:update
// scope on that specific invoice in addition to payment:update + IDOR on the
// payment (spec §9) — a caller who can edit their own payment but can't see
// the target invoice must not be able to move money onto it.
func (h *PaymentOps) Apply(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, identityID, _, ok := h.authPaymentByUUID(w, r, id, authz.ActionUpdate)
	if !ok {
		return
	}
	var req payApplyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.InvoiceUUID == "" || req.Amount <= 0 {
		fail(w, http.StatusBadRequest, "invoiceUuid and a positive amount are required.")
		return
	}
	if !h.invoiceInScopeForUpdate(w, r, pool, identityID, req.InvoiceUUID) {
		return
	}
	empID := resolveEmployeeID(r, identityID)
	p, err := payment.Apply(r.Context(), pool, id, req.InvoiceUUID, req.Amount, empID)
	if err != nil {
		paymentFail(w, err, "Failed to apply payment.")
		return
	}
	auditPayment(r, pool, empID, "apply", id, nil, p)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "payment": p})
}

type payUnapplyRequest struct {
	InvoiceUUID string `json:"invoiceUuid"`
}

func (h *PaymentOps) Unapply(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, identityID, _, ok := h.authPaymentByUUID(w, r, id, authz.ActionUpdate)
	if !ok {
		return
	}
	var req payUnapplyRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.InvoiceUUID == "" {
		fail(w, http.StatusBadRequest, "invoiceUuid is required.")
		return
	}
	if !h.invoiceInScopeForUpdate(w, r, pool, identityID, req.InvoiceUUID) {
		return
	}
	empID := resolveEmployeeID(r, identityID)
	p, err := payment.Unapply(r.Context(), pool, id, req.InvoiceUUID, empID)
	if err != nil {
		paymentFail(w, err, "Failed to unapply payment.")
		return
	}
	auditPayment(r, pool, empID, "unapply", id, nil, p)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "payment": p})
}
```

- [ ] **Step 3:** Add the `invoiceInScopeForUpdate` helper to `controllers/payment.go` (append — it's an IDOR guard against the *invoice* side of an apply/unapply call, distinct from `authPaymentByUUID`'s guard on the payment side):

```go
// invoiceInScopeForUpdate checks the caller holds invoice:update and that the
// target invoice is within their scope, writing the response and returning
// false on denial (404 on scope denial, per the IDOR convention). Used by
// Apply/Unapply because those endpoints mutate an invoice's AR balance as a
// side effect of a payment-side action.
func (h *PaymentOps) invoiceInScopeForUpdate(w http.ResponseWriter, r *http.Request, pool *pgxpool.Pool, identityID, invoiceUUID string) bool {
	decision, err := authz.Check(r.Context(), pool, identityID, authz.ResourceInvoice, authz.ActionUpdate)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return false
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", identityID, "resource", string(authz.ResourceInvoice), "action", string(authz.ActionUpdate))
		fail(w, http.StatusForbidden, "You do not have permission to update invoices.")
		return false
	}
	if decision.Scope == authz.ScopeAll {
		return true
	}
	inv, err := invoice.Get(r.Context(), pool, invoiceUUID)
	if errors.Is(err, invoice.ErrNotFound) {
		fail(w, http.StatusNotFound, "Invoice not found.")
		return false
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load invoice.")
		return false
	}
	allowed, aerr := recordInScope(r.Context(), pool, decision.Scope, identityID, inv.OwnerUserID, "")
	if aerr != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return false
	}
	if !allowed {
		logSecurityEvent(r, "idor_denied",
			"identity", identityID, "record", invoiceUUID, "resource", string(authz.ResourceInvoice),
			"action", "update", "scope", string(decision.Scope))
		fail(w, http.StatusNotFound, "Invoice not found.")
		return false
	}
	return true
}
```
  Add `"stonesuite-backend/invoice"` to `controllers/payment.go`'s import block.
- [ ] **Step 4:** Run `go build ./... && go test ./controllers/ -run Payment -v` → PASS.
- [ ] **Step 5:** Commit — `git commit -m "feat(payment): add transition/apply/unapply HTTP handlers"`.

### Task 4.3: Audit handler
- [ ] **Step 1:** Implement `controllers/payment_audit.go`, mirroring `controllers/invoice_audit.go` exactly:

```go
package controllers

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/payment"
	"stonesuite-backend/workflow"
)

func paymentSnapshot(p *payment.Payment) map[string]any {
	if p == nil {
		return nil
	}
	return map[string]any{
		"id":              p.ID,
		"number":          p.Number,
		"statusCode":      p.StatusCode,
		"customerId":      p.Customer.ID,
		"ownerUserId":     p.OwnerUserID,
		"amount":          p.Amount,
		"appliedTotal":    p.AppliedTotal,
		"unappliedAmount": p.UnappliedAmount,
		"applicationCount": len(p.Applications),
		"customFields":    p.CustomFields,
	}
}

func auditPayment(r *http.Request, pool *pgxpool.Pool, actorEmployeeID int, action, paymentID string, oldPayment, newPayment *payment.Payment) {
	ctx := r.Context()
	if err := workflow.LogAuditFull(ctx, pool, "", action, string(authz.ResourcePayment), paymentID, "payment",
		paymentSnapshot(oldPayment), paymentSnapshot(newPayment), map[string]any{"employee_id": actorEmployeeID},
		clientIP(r), r.Header.Get("X-Session-Id"), appVersion); err != nil {
		log.Printf("payment: audit %s %s: %v", action, paymentID, err)
	}
}

type payAuditEntry struct {
	Action     string         `json:"action"`
	ActorName  string         `json:"actorName"`
	IPAddress  string         `json:"ipAddress"`
	AppVersion string         `json:"appVersion"`
	OldValue   map[string]any `json:"oldValue,omitempty"`
	NewValue   map[string]any `json:"newValue,omitempty"`
	At         time.Time      `json:"at"`
}

func (h *PaymentOps) Audit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, _, _, ok := h.authPaymentByUUID(w, r, id, authz.ActionRead)
	if !ok {
		return
	}
	rows, err := pool.Query(r.Context(), `
		SELECT al.action,
		       COALESCE(u.full_name, u.email, ''),
		       COALESCE(host(al.ip_address),''), COALESCE(al.app_version,''),
		       al.old_value, al.new_value, al.created_at
		FROM audit_logs al
		LEFT JOIN users u ON u.id = al.actor_user_id
		WHERE al.resource_id = $1 AND al.resource = $2
		ORDER BY al.created_at DESC
		LIMIT 200`, id, string(authz.ResourcePayment))
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load audit trail.")
		return
	}
	defer rows.Close()
	entries := []payAuditEntry{}
	for rows.Next() {
		var (
			e              payAuditEntry
			oldRaw, newRaw []byte
		)
		if err := rows.Scan(&e.Action, &e.ActorName,
			&e.IPAddress, &e.AppVersion, &oldRaw, &newRaw, &e.At); err != nil {
			fail(w, http.StatusInternalServerError, "Failed to read audit trail.")
			return
		}
		if len(oldRaw) > 0 {
			_ = json.Unmarshal(oldRaw, &e.OldValue)
		}
		if len(newRaw) > 0 {
			_ = json.Unmarshal(newRaw, &e.NewValue)
		}
		entries = append(entries, e)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "recordId": id, "audit": entries,
	})
}
```

- [ ] **Step 2:** `go build ./...` → PASS.
- [ ] **Step 3:** Commit — `git commit -m "feat(payment): add audit trail handler"`.

### Task 4.4: `GET /invoices/{uuid}/payments` (AR reconciliation view)
- [ ] **Step 1:** Write a failing handler test in `controllers/invoice_test.go` (append to the existing `handlers` map in `TestInvoiceOps_RequiresAuth`):

```go
"Payments": h.Payments,
```
  (Add this line to the `handlers` map literal in that test — it will 401 like every other entry, no new test function needed.)
- [ ] **Step 2:** Run → FAIL (`h.Payments` doesn't exist).
- [ ] **Step 3:** Implement `controllers/invoice_payments.go`:

```go
package controllers

import (
	"net/http"
	"time"

	"stonesuite-backend/authz"
)

// invoicePaymentEntry is one live payment_application row against this
// invoice, flattened for the AR reconciliation view.
type invoicePaymentEntry struct {
	PaymentID     string    `json:"paymentId"`
	PaymentNumber string    `json:"paymentNumber"`
	Amount        float64   `json:"amount"`
	AppliedAt     time.Time `json:"appliedAt"`
}

// Payments lists the live payment applications against one invoice — an AR
// reconciliation view, not a mutation. Uses the invoice's own IDOR guard
// (authInvoiceByUUID) since this is invoice-centric access, not payment.
func (h *InvoiceOps) Payments(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, _, _, ok := h.authInvoiceByUUID(w, r, id, authz.ActionRead)
	if !ok {
		return
	}
	rows, err := pool.Query(r.Context(), `
		SELECT p.payment_uuid, COALESCE(p.payment_number,''), pa.application_amount, pa.application_created_at
		FROM payment_application pa
		JOIN payment p ON p.payment_id = pa.payment_id
		JOIN invoice i ON i.invoice_id = pa.invoice_id
		WHERE i.invoice_uuid = $1 AND pa.application_deleted_at IS NULL AND p.payment_deleted_at IS NULL
		ORDER BY pa.application_created_at DESC`, id)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load payments for invoice.")
		return
	}
	defer rows.Close()
	entries := []invoicePaymentEntry{}
	for rows.Next() {
		var e invoicePaymentEntry
		if err := rows.Scan(&e.PaymentID, &e.PaymentNumber, &e.Amount, &e.AppliedAt); err != nil {
			fail(w, http.StatusInternalServerError, "Failed to read payments for invoice.")
			return
		}
		entries = append(entries, e)
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "recordId": id, "payments": entries})
}
```

- [ ] **Step 4:** Run `go build ./... && go test ./controllers/ -run Invoice -v` → PASS.
- [ ] **Step 5:** Commit — `git commit -m "feat(payment): add invoice-centric payments listing endpoint"`.

### Task 4.5: Rewire the legacy `RecordPayment` handler
- [ ] **Step 1:** Read `controllers/invoice_transition.go`'s current `RecordPayment` handler (calls `invoice.RecordPayment` directly). Replace its body to call `payment.QuickPay` instead, keeping the route, request shape, and response envelope unchanged:

```go
type recordPaymentRequest struct {
	Amount float64 `json:"amount"`
}

// RecordPayment is the legacy quick-pay endpoint (spec AD-5): it now delegates
// to payment.QuickPay, which creates a Payment + one payment_application
// under the hood, instead of writing invoice_amount_paid directly. Path,
// request, and response shape are unchanged for API compatibility.
func (h *InvoiceOps) RecordPayment(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, identityID, _, ok := h.authInvoiceByUUID(w, r, id, authz.ActionUpdate)
	if !ok {
		return
	}
	var req recordPaymentRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Amount <= 0 {
		fail(w, http.StatusBadRequest, "amount is required and must be positive.")
		return
	}
	empID := resolveEmployeeID(r, identityID)
	inv, err := payment.QuickPay(r.Context(), pool, id, req.Amount, empID)
	if err != nil {
		invoiceFail(w, err, "Failed to record payment.")
		return
	}
	auditInvoice(r, pool, empID, "payment", id, nil, inv)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "invoice": inv})
}
```
  Add `"stonesuite-backend/payment"` to `controllers/invoice_transition.go`'s import block.
- [ ] **Step 2:** `payment.QuickPay` can now return a `payment.ClientError` (e.g. "Amount exceeds available balance.") in addition to the `invoice.ClientError`/`invoice.ErrNotFound`/`*query.InvalidFilterError` cases `invoiceFail` already maps. In `controllers/invoice.go`, add a case to `invoiceFail`'s `default` branch:

```go
func invoiceFail(w http.ResponseWriter, err error, serverMsg string) {
	switch {
	case errors.Is(err, invoice.ErrNotFound):
		fail(w, http.StatusNotFound, "Invoice not found.")
	case errors.Is(err, invoice.ErrInvalidTransition):
		fail(w, http.StatusConflict, err.Error())
	default:
		var ce invoice.ClientError
		if errors.As(err, &ce) {
			fail(w, http.StatusBadRequest, ce.Error())
			return
		}
		var pce payment.ClientError
		if errors.As(err, &pce) {
			fail(w, http.StatusBadRequest, pce.Error())
			return
		}
		var ife *query.InvalidFilterError
		if errors.As(err, &ife) {
			fail(w, http.StatusBadRequest, ife.Error())
			return
		}
		fail(w, http.StatusInternalServerError, serverMsg)
	}
}
```
  (This adds the new `var pce payment.ClientError` block; everything else in the function is unchanged. `controllers/invoice.go` already imports `"stonesuite-backend/invoice"`; add `"stonesuite-backend/payment"` to its import block too.)
- [ ] **Step 3:** `go build ./...` → PASS.
- [ ] **Step 4:** Run `go test ./controllers/ -run Invoice -v` → PASS (the existing `TestInvoiceOps_RequiresAuth`/`TestInvoiceFail_MapsStoreErrorsToHTTPStatus` tests still cover the unchanged auth/error-mapping contract).
- [ ] **Step 5:** Commit — `git commit -m "feat(payment): rewire legacy invoice payment endpoint through QuickPay"`.

### Task 4.6: Route registration
- [ ] **Step 1:** In `main.go`, immediately after the existing invoice routes block (the one ending `mux.Handle("GET /api/tenant/invoices/{uuid}/audit", ...)`), add:

```go
// Payment: dedicated v2 relational module, sibling of invoice. Its
// payment_application ledger is now the source of truth for invoice AR
// balances (spec docs/superpowers/specs/2026-07-13-payments-module-design.md).
payOps := controllers.NewPaymentOps()
mux.Handle("GET /api/tenant/payments", tenantChain(payOps.List))
mux.Handle("POST /api/tenant/payments/search", tenantChain(payOps.Search))
mux.Handle("POST /api/tenant/payments", tenantChain(payOps.Create))
mux.Handle("GET /api/tenant/payments/{uuid}", tenantChain(payOps.Get))
mux.Handle("PATCH /api/tenant/payments/{uuid}", tenantChain(payOps.Update))
mux.Handle("DELETE /api/tenant/payments/{uuid}", tenantChain(payOps.Delete))
mux.Handle("POST /api/tenant/payments/{uuid}/transition", tenantChain(payOps.Transition))
mux.Handle("POST /api/tenant/payments/{uuid}/apply", tenantChain(payOps.Apply))
mux.Handle("POST /api/tenant/payments/{uuid}/unapply", tenantChain(payOps.Unapply))
mux.Handle("GET /api/tenant/payments/{uuid}/audit", tenantChain(payOps.Audit))
mux.Handle("GET /api/tenant/invoices/{uuid}/payments", tenantChain(invOps.Payments))
```
  (`invOps` is already declared earlier in this same block from the invoice routes — reuse it, don't redeclare.)
- [ ] **Step 2:** `go build ./...` → PASS.
- [ ] **Step 3:** Commit — `git commit -m "feat(payment): register payment routes"`.

---

## Phase 5 — Retire the superseded invoice code path

### Task 5.1: Remove `invoice.RecordPayment` and the now-unused `payableStatuses`
- [ ] **Step 1:** In `invoice/store_transition.go`, delete the entire `RecordPayment` function (everything from `// RecordPayment adds to amount_paid...` through its closing `}`). Keep `Transition` — it is unrelated and still used for user-directed invoice status changes.
- [ ] **Step 2:** In `invoice/store.go`, delete the `payableStatuses` var declaration (it was only referenced inside the now-deleted `RecordPayment`). Run `go build ./invoice/...` to confirm nothing else references it — if the build fails on an unrelated reference, stop and investigate before deleting further.
- [ ] **Step 3:** In `invoice/store_test.go`, delete `TestRecordPayment` and `TestRecordPayment_RejectedBeforeSent` in full (their coverage now lives in `payment/store_test.go`'s `TestQuickPay` + `TestApply_RejectsUnsentInvoice`). Rewrite `TestUpdate_RejectsBelowAmountPaid` to seed `amount_paid` via a direct SQL update instead of the removed `RecordPayment` call:

```go
// C3: Update cannot reduce the total below the amount already paid.
func TestUpdate_RejectsBelowAmountPaid(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	custUUID, itemUUID := seedCustomerAndItem(t, pool)

	inv, err := Create(ctx, pool, CreateInvoiceInput{
		CustomerUUID: custUUID,
		Items:        []InvoiceLineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1, UnitPrice: 100}},
	}, 1)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	for _, st := range []string{"PAPV", "APPV", "SENT"} {
		if inv, err = Transition(ctx, pool, inv.ID, st, 1); err != nil {
			t.Fatalf("transition to %s: %v", st, err)
		}
	}
	// amount_paid is now written by the payment module, not this package —
	// seed it directly to test Update's own guard in isolation.
	if _, err := pool.Exec(ctx, `UPDATE invoice SET invoice_amount_paid = 60 WHERE invoice_uuid = $1`, inv.ID); err != nil {
		t.Fatalf("seed amount_paid: %v", err)
	}
	// Reduce the total to 10, below the 60 already paid → ClientError, not a 500.
	_, err = Update(ctx, pool, inv.ID, UpdateInvoiceInput{
		Items: []InvoiceLineInput{{LineNumber: 1, InventoryItemUUID: itemUUID, Quantity: 1, UnitPrice: 10}},
	}, 1)
	if _, ok := err.(ClientError); !ok {
		t.Fatalf("expected ClientError reducing total below amount paid, got %T: %v", err, err)
	}
}
```

- [ ] **Step 4:** Run `go build ./... && go vet ./... && go test ./...` → PASS (dbtest-gated tests skip without `TEST_DATABASE_URL`; if it's set, they must pass too).
- [ ] **Step 5:** Commit — `git commit -m "refactor(invoice): remove superseded RecordPayment now that payment_application is authoritative"`.

---

## Phase 6 — Verification & security review

### Task 6.1: Full build, vet, test
- [ ] **Step 1:** `go build ./... && go vet ./... && go test ./...` → all PASS.
- [ ] **Step 2:** If `TEST_DATABASE_URL` is available, run `go test ./... -tags dbtest -v` and confirm every `payment/` and updated `invoice/` integration test passes.

### Task 6.2: Dispatch review agents (run all, per the original request)
- [ ] **Step 1:** Dispatch **migration-auditor** on the final `database/migrations/tenant/schema.sql` diff (Task 1.1's block, re-verify nothing changed since).
- [ ] **Step 2:** Dispatch **tenancy-security-reviewer** on every payments handler and store: `controllers/payment.go`, `controllers/payment_transition.go`, `controllers/payment_audit.go`, `controllers/invoice_payments.go`, `controllers/invoice_transition.go` (the rewired `RecordPayment`), `payment/store.go`, `payment/store_create.go`, `payment/store_update.go`, `payment/apply.go`, `payment/store_transition.go`, `payment/quickpay.go`.
- [ ] **Step 3:** Dispatch **filter-invariant-checker** on `payment/resolver.go` + `payment/search.go` (repeat of Task 3.8's check, now against the final diff).
- [ ] **Step 4:** Dispatch **feature-dev:code-reviewer** (general correctness: money math, transaction boundaries, edge cases) on the full `payment/` + payments-related `controllers/` + `invoice/` diff.
- [ ] **Step 5:** Dispatch **code-simplifier:code-simplifier** on the same diff for a cleanup pass.
- [ ] **Step 6:** Address or explicitly justify every finding from Steps 1–5. Commit fixes — `fix(payment): address security/invariant/correctness review findings` (only if needed; may be more than one commit if findings span unrelated files).
- [ ] **Step 7:** Re-run `go build ./... && go vet ./... && go test ./...` → PASS after any fixes.

### Task 6.3: Write the review doc
- [ ] **Step 1:** Consolidate the findings (and their resolutions) from Task 6.2 into `docs/superpowers/reviews/2026-07-13-payments-module-review.md`, mirroring the shape of `docs/superpowers/reviews/2026-07-12-invoice-integration-review.md` (read it first for the expected structure/tone).
- [ ] **Step 2:** Commit — `git commit -m "docs(payment): add module review summary"`.
- [ ] **Step 3:** Leave the branch in a state ready for review — do not push, do not open a PR, do not merge. Summarize the design and review findings for the user first (per the original task's instructions).

---

## Notes for the implementing agent

- **The lock order is load-bearing.** Every function that locks both a `payment` row and an `invoice` row (`Apply`, `Unapply`, the `VOID` cascade in `Transition`) must lock `payment` first, then `invoice`. Deviating risks a deadlock between two concurrent calls locking in opposite orders. `QuickPay` is exempt from this concern since it calls `Create` then `Apply` as separate transactions, never holding both locks in one tx alongside another path.
- **`deriveInvoiceStatus` bypassing `invoice.CanTransition` is intentional**, not an oversight — see Task 3.3 Step 1's rationale in full before "fixing" it to go through the transition map. Doing so would silently break `Unapply` on any invoice that reached `PAID`.
- **The two-phase (non-atomic) trade-off in `Create`'s inline applications and in `QuickPay`** (header commits in its own transaction, then `Apply` runs as a separate transaction per application) is accepted deliberately (spec AD-5/§8) — do not try to wrap them in one giant transaction; that would require threading a shared `pgx.Tx` through `Apply`, which would also change its concurrency/locking story for the general-purpose `POST /payments/{uuid}/apply` endpoint. If a caller needs atomicity, they can inspect the returned/partial state and retry the specific failed application.
- **Don't touch Credit Memo or Refund.** Their `lkp_record_type`/`lkp_record_status` rows are seeded and their own future modules — out of scope here (spec §1, confirmed in brainstorming).
- If any spec section is ambiguous once you're in the code, resolve it the same direction the Invoice module resolved the analogous question (cite the Invoice spec section you're mirroring) rather than inventing a new convention.
