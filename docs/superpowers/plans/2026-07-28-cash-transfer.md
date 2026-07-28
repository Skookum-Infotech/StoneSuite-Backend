# Cash Transfer Module Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking. This plan assumes Chart of Accounts (`chartofaccounts/`, `coa_account` + friends) is **already implemented and merged on this branch** — Cash Transfer FKs into `coa_account`. Verify with `ls chartofaccounts/` and `grep -n "CREATE TABLE IF NOT EXISTS coa_account " database/migrations/tenant/schema.sql` before starting; if missing, stop.

**Goal:** Add a production-grade Cash Transfer module (move funds between two Cash/Bank GL accounts, Draft→Approve→Post→Reverse) plus the minimal GL foundation (`journal/` package: `journal_entry`/`journal_entry_line`, an account running balance, one closed-period date) needed to make Post/Reverse produce real, balanced ledger effects — because no posting engine exists anywhere in this codebase yet.

**Spec (authoritative):** `docs/superpowers/specs/2026-07-28-cash-transfer-design.md` — cite section/AD numbers (e.g. "spec AD-3").

**Architecture:** Two new packages. `journal/` is dependency-free of app packages (same discipline as `query/`) and exposes only `CreateEntry`/`ReverseEntry`/`IsPeriodClosed` — no HTTP surface. `cashtransfer/` is a normal sibling document-module package (mirrors `itemreceipt/`'s shape: header + dedicated Post + dedicated Reverse + generic Transition, no lines/no multi-approver chain) that calls into `journal/` from inside its own transaction, the same way `itemreceipt/store_post.go` calls its local `ledgerAndStock` helper.

**Tech Stack:** Go (`net/http`, `pgx/v5`, `pgxpool`), PostgreSQL (per-tenant DB), stdlib `testing` for pure functions.

## Global Constraints (from CLAUDE.md and the spec)

- No `tenant_id` column on any tenant-DB table; the DB connection itself is the tenant scope.
- Migrations idempotent + append-only (`CREATE TABLE IF NOT EXISTS`, `ALTER TABLE ... ADD COLUMN IF NOT EXISTS`, `ON CONFLICT DO NOTHING`). Never `DROP`/rename/destructive `ALTER COLUMN`.
- Every `/api/tenant/` route: `tenantChain` (RequireAuth → rate limit → TenantResolver) + RBAC `authz.Check` before any write + scope filtering on lists + single-record IDOR guard returning **404** (not 403), logged via `logSecurityEvent(r, "idor_denied", ...)`. Permission denials logged via `logSecurityEvent(r, "permission_denied", ...)`.
- Filter × scope ANDed, never OR; field keys resolved via whitelist `FieldResolver` only; all values parameterized; keyset pagination only, `MaxLimit` 100, default 25.
- Money `DECIMAL(15,2)` in SQL, `float64` in Go (matches `payment`/`refund`).
- Response envelope `{ success, message?, ... }` via `controllers.writeJSON`/`controllers.fail`.
- Conventional Commits; `go build ./... && go vet ./... && go test ./...` green before each commit.
- Integration tests: `//go:build dbtest` + `TEST_DATABASE_URL`, `t.Skip` when unset (mirror `itemreceipt/store_test.go`). Pure-function tests carry no build tag, use stdlib `testing`, table-driven.
- Files over 300 lines: split them.
- `journal/` package: **zero imports of any `stonesuite-backend/*` app package** — only stdlib + `github.com/jackc/pgx/v5` + `github.com/jackc/pgx/v5/pgconn`. This is what keeps it dependency-free (spec AD-1).
- New RBAC resource `ResourceCashTransfer` needs 5 catalog rows (create/read/update/delete/transition) — **not** a 6th `ActionApprove` row (spec AD-5; Approve rides the existing `ActionTransition`).
- Routes live under `/api/tenant/finance/cash-transfers/...` (spec §5 — nests under the Finance section Chart of Accounts established).

## File Structure

**Created:**
- `journal/types.go` — `LineInput`, `CreateEntryInput`, `JournalEntryLine`, `JournalEntry`
- `journal/numbering.go` + `_test.go` — `JE-000001` formatter
- `journal/period.go` + `_test.go` — `isClosed` (pure) + `IsPeriodClosed` (DB read)
- `journal/store.go` + `_test.go` (pure `TestValidateLines`) — local `querier` interface, `nullableInt`, `round2`, `ErrUnbalancedEntry`, `ErrInvalidLine`, `validateLines`, `CreateEntry`
- `journal/store_dbtest_test.go` (dbtest) — the spec §7-equivalent integration tests for `journal/`
- `journal/reverse.go` — `ReverseEntry`
- `cashtransfer/types.go` — `AccountRef`, `CashTransfer`, `CreateInput`, `UpdateInput`, `Page`
- `cashtransfer/errors.go` — `ErrNotFound`, `ErrInvalidTransition`, `ErrAlreadyPosted`, `ErrNotPosted`, `ErrPeriodClosed`, `ClientError`
- `cashtransfer/numbering.go` + `_test.go` — `CTRF-000001` formatter
- `cashtransfer/transitions.go` + `_test.go` — status map, `CanTransition`, `ValidateTransition`, `IsPosted`, status/type code constants
- `cashtransfer/resolver.go` + `_test.go` — `query.FieldResolver`/`SortResolver`/`SearchResolver`
- `cashtransfer/store.go` — shared helpers + `Get`: `headerSelect`, `scanCT`, `ctMeta`, `recordTypeIDByCode`, `statusIDByCode`, `nullableInt`, `writeHistory`, `validateCustom`, `resolveAccount`
- `cashtransfer/store_create.go` — `Create`
- `cashtransfer/store_update.go` — `Update`, `SoftDelete`
- `cashtransfer/store_search.go` — `Search`, `sortValue`
- `cashtransfer/store_transition.go` — `Transition` (Approve/Cancel)
- `cashtransfer/store_post.go` — `Post`
- `cashtransfer/store_reverse.go` — `Reverse`
- `cashtransfer/store_test.go` (dbtest) — the spec §7 integration tests
- `controllers/cashtransfer.go` — `CashTransferOps`, `authCashTransfer`/`authCashTransferByUUID`, `ctFail`, `Create`/`Get`/`Update`/`Delete`/`List`/`Search`
- `controllers/cashtransfer_actions.go` — `Transition`, `Post`, `Reverse` handlers
- `controllers/cashtransfer_audit.go` — `auditCT`, `ctAuditEntry`, `Audit`

**Modified:**
- `database/migrations/tenant/schema.sql` — append `accounting_settings`, `journal_entry`, `journal_entry_line`, the `coa_account_balance` column, the `CTRF` record type + status seed, `cash_transfer`, `cash_transfer_history` (spec §3)
- `authz/catalog.go` — `ResourceCashTransfer` const + 5 catalog rows
- `controllers/crm.go` — add `"cash_transfer"` case to `resourceForKey`
- `workflow/attachments.go` — add a 4th branch to `ResolveRecordAccess` for `cash_transfer`
- `main.go` — one constructor + 10 `mux.Handle` lines in the tenant block

---

## Phase 1 — Database schema

### Task 1.1: Journal foundation + Cash Transfer tables
- [ ] **Step 1:** Read spec §3 in full. Use the **add-migration** skill's idempotency checklist. Append the following to `database/migrations/tenant/schema.sql`, placed after the `chartofaccounts` block (after `coa_default_mapping` and its index):

```sql
-- ── Cash Transfer module + GL foundation (journal/) ────────────────────

-- accounting_settings -- singleton; the entire "closed period" concept --------
CREATE TABLE IF NOT EXISTS accounting_settings (
    accounting_settings_id      SMALLINT     PRIMARY KEY DEFAULT 1,
    books_closed_through        DATE             NULL,
    accounting_settings_updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    accounting_settings_updated_by INTEGER       NULL REFERENCES employee(employee_id),
    CONSTRAINT chk_accounting_settings_singleton CHECK (accounting_settings_id = 1)
);
INSERT INTO accounting_settings (accounting_settings_id) VALUES (1) ON CONFLICT DO NOTHING;

-- journal_entry -- the GL posting header --------------------------------------
CREATE TABLE IF NOT EXISTS journal_entry (
    journal_entry_id          SERIAL       PRIMARY KEY,
    journal_entry_uuid        UUID         NOT NULL DEFAULT gen_random_uuid(),
    journal_entry_number      VARCHAR(20)      NULL,
    entry_date                 DATE         NOT NULL,
    memo                       TEXT         NOT NULL DEFAULT '',
    source_type                 VARCHAR(30)  NOT NULL,
    source_id                    UUID         NOT NULL,
    is_reversal                   BOOLEAN      NOT NULL DEFAULT FALSE,
    reverses_journal_entry_id      INTEGER          NULL REFERENCES journal_entry(journal_entry_id),
    journal_entry_created_at        TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    journal_entry_created_by         INTEGER          NULL REFERENCES employee(employee_id),

    CONSTRAINT uq_je_uuid   UNIQUE (journal_entry_uuid),
    CONSTRAINT uq_je_number UNIQUE (journal_entry_number)
);
CREATE INDEX IF NOT EXISTS idx_je_source ON journal_entry (source_type, source_id);

-- journal_entry_line -- one debit or credit leg -------------------------------
CREATE TABLE IF NOT EXISTS journal_entry_line (
    journal_entry_line_id SERIAL        PRIMARY KEY,
    journal_entry_id       INTEGER       NOT NULL REFERENCES journal_entry(journal_entry_id),
    line_number              INTEGER       NOT NULL,
    coa_account_id             INTEGER       NOT NULL REFERENCES coa_account(coa_account_id),
    debit                        DECIMAL(15,2) NOT NULL DEFAULT 0,
    credit                        DECIMAL(15,2) NOT NULL DEFAULT 0,

    CONSTRAINT uq_jel_line      UNIQUE (journal_entry_id, line_number),
    CONSTRAINT chk_jel_nonneg   CHECK (debit >= 0 AND credit >= 0),
    CONSTRAINT chk_jel_one_side CHECK (NOT (debit > 0 AND credit > 0)),
    CONSTRAINT chk_jel_nonzero  CHECK (debit > 0 OR credit > 0)
);
CREATE INDEX IF NOT EXISTS idx_jel_account ON journal_entry_line (coa_account_id);
CREATE INDEX IF NOT EXISTS idx_jel_entry   ON journal_entry_line (journal_entry_id);

-- coa_account running balance -------------------------------------------------
ALTER TABLE coa_account ADD COLUMN IF NOT EXISTS coa_account_balance DECIMAL(15,2) NOT NULL DEFAULT 0;

-- New record type for Cash Transfer, appended as its own statement -----------
INSERT INTO lkp_record_type (record_type_code, record_type_code_full, record_type_name, record_type_is_active, record_type_is_system, record_type_created_by) VALUES
    ('CTRF', 'cashtransfer', 'Cash Transfer', TRUE, TRUE, 1)
ON CONFLICT (record_type_code) DO NOTHING;

INSERT INTO lkp_record_status (record_status_code, record_status_name,
    record_status_record_type, record_status_is_active, record_status_is_system, record_status_created_by)
SELECT v.code, v.name, rt.record_type_id, TRUE, TRUE, 1
FROM (VALUES
    ('DRFT','Draft'), ('APPR','Approved'), ('POST','Posted'),
    ('CANC','Cancelled'), ('RVSD','Reversed')
) AS v(code, name)
CROSS JOIN lkp_record_type rt
WHERE rt.record_type_code = 'CTRF'
ON CONFLICT (record_status_code, record_status_record_type) DO NOTHING;

-- cash_transfer -- header ------------------------------------------------------
CREATE TABLE IF NOT EXISTS cash_transfer (
    cash_transfer_id            SERIAL       PRIMARY KEY,
    cash_transfer_uuid           UUID         NOT NULL DEFAULT gen_random_uuid(),
    cash_transfer_number          VARCHAR(20)      NULL,
    record_type                    INTEGER      NOT NULL REFERENCES lkp_record_type(record_type_id),
    cash_transfer_status             INTEGER      NOT NULL REFERENCES lkp_record_status(record_status_id),
    cash_transfer_date                 DATE         NOT NULL DEFAULT CURRENT_DATE,
    from_account_id                      INTEGER      NOT NULL REFERENCES coa_account(coa_account_id),
    to_account_id                          INTEGER      NOT NULL REFERENCES coa_account(coa_account_id),
    cash_transfer_amount                    DECIMAL(15,2) NOT NULL,
    cash_transfer_reference                   VARCHAR(100) NOT NULL DEFAULT '',
    cash_transfer_notes                         TEXT         NOT NULL DEFAULT '',
    cash_transfer_internal_notes                  TEXT         NOT NULL DEFAULT '',
    cash_transfer_custom_fields                     JSONB        NOT NULL DEFAULT '{}',
    cash_transfer_owner_id                            INTEGER          NULL REFERENCES employee(employee_id),
    journal_entry_id                                    INTEGER          NULL REFERENCES journal_entry(journal_entry_id),
    reversal_journal_entry_id                             INTEGER          NULL REFERENCES journal_entry(journal_entry_id),
    cash_transfer_posted_at                                 TIMESTAMP        NULL,
    cash_transfer_posted_by                                   INTEGER          NULL REFERENCES employee(employee_id),
    cash_transfer_reversed_at                                   TIMESTAMP        NULL,
    cash_transfer_reversed_by                                     INTEGER          NULL REFERENCES employee(employee_id),
    cash_transfer_created_at                                       TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    cash_transfer_created_by                                         INTEGER          NULL REFERENCES employee(employee_id),
    cash_transfer_updated_at                                           TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    cash_transfer_updated_by                                             INTEGER          NULL REFERENCES employee(employee_id),
    cash_transfer_deleted_at                                               TIMESTAMP        NULL,
    cash_transfer_deleted_by                                                 INTEGER          NULL REFERENCES employee(employee_id),
    cash_transfer_record_version                                               INTEGER      NOT NULL DEFAULT 1,

    CONSTRAINT uq_cash_transfer_uuid   UNIQUE (cash_transfer_uuid),
    CONSTRAINT uq_cash_transfer_number UNIQUE (cash_transfer_number),
    CONSTRAINT chk_ct_diff_accounts CHECK (from_account_id <> to_account_id),
    CONSTRAINT chk_ct_amount_positive CHECK (cash_transfer_amount > 0),
    CONSTRAINT chk_ct_posted_pair   CHECK ((cash_transfer_posted_at IS NULL) = (journal_entry_id IS NULL)),
    CONSTRAINT chk_ct_reversed_pair CHECK ((cash_transfer_reversed_at IS NULL) = (reversal_journal_entry_id IS NULL)),
    CONSTRAINT chk_ct_soft_delete CHECK (
        (cash_transfer_deleted_at IS NULL AND cash_transfer_deleted_by IS NULL) OR
        (cash_transfer_deleted_at IS NOT NULL AND cash_transfer_deleted_by IS NOT NULL)
    )
);

CREATE INDEX IF NOT EXISTS idx_ct_status  ON cash_transfer (cash_transfer_status) WHERE cash_transfer_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_ct_from    ON cash_transfer (from_account_id)      WHERE cash_transfer_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_ct_to      ON cash_transfer (to_account_id)        WHERE cash_transfer_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_ct_owner   ON cash_transfer (cash_transfer_owner_id) WHERE cash_transfer_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_ct_custom_gin ON cash_transfer USING GIN (cash_transfer_custom_fields);
CREATE INDEX IF NOT EXISTS idx_ct_created_keyset ON cash_transfer (cash_transfer_created_at, cash_transfer_id) WHERE cash_transfer_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_ct_updated_keyset ON cash_transfer (cash_transfer_updated_at, cash_transfer_id) WHERE cash_transfer_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_ct_number_keyset  ON cash_transfer (cash_transfer_number, cash_transfer_id)     WHERE cash_transfer_deleted_at IS NULL;

-- cash_transfer_history -- status trail ---------------------------------------
CREATE TABLE IF NOT EXISTS cash_transfer_history (
    cash_transfer_history_id SERIAL      PRIMARY KEY,
    cash_transfer_id           INTEGER     NOT NULL REFERENCES cash_transfer(cash_transfer_id),
    from_status_id               INTEGER         NULL REFERENCES lkp_record_status(record_status_id),
    to_status_id                   INTEGER         NULL REFERENCES lkp_record_status(record_status_id),
    history_action                   VARCHAR(20) NOT NULL,
    history_at                         TIMESTAMP   NOT NULL DEFAULT CURRENT_TIMESTAMP,
    history_by                           INTEGER         NULL REFERENCES employee(employee_id),
    CONSTRAINT chk_ct_history_action CHECK (history_action IN
        ('create','update','transition','post','reverse','delete'))
);
CREATE INDEX IF NOT EXISTS idx_ct_history_record ON cash_transfer_history (cash_transfer_id, history_at DESC);
```

- [ ] **Step 2:** `go build ./...` — confirm this is a pure append with no compile impact yet (no Go code references these tables).
- [ ] **Step 3:** If `TEST_DATABASE_URL` is reachable, apply the schema twice against a fresh database to verify idempotency. Otherwise visually confirm every statement is `IF NOT EXISTS` / `ON CONFLICT DO NOTHING` / `ADD COLUMN IF NOT EXISTS`.
- [ ] **Step 4:** Dispatch the **migration-auditor** agent on the diff. Fix findings.
- [ ] **Step 5:** Commit — `git commit -m "feat(cashtransfer): add journal_entry, cash_transfer, and accounting_settings tables"`.

---

## Phase 2 — `journal/` package (pure logic + transactional store, TDD)

### Task 2.1: Journal entry number formatting
- [ ] **Step 1:** Write a failing test `journal/numbering_test.go`:

```go
package journal

import "testing"

func TestFormatNumber(t *testing.T) {
	for in, want := range map[int64]string{1: "JE-000001", 42: "JE-000042", 1234567: "JE-1234567"} {
		if got := FormatNumber(in); got != want {
			t.Errorf("FormatNumber(%d) = %s, want %s", in, got, want)
		}
	}
}
```

- [ ] **Step 2:** Run `go test ./journal/... -run TestFormatNumber -v` → FAIL (package doesn't exist).
- [ ] **Step 3:** Implement `journal/numbering.go`:

```go
package journal

import "fmt"

const numberPrefix = "JE"

// FormatNumber renders the human-readable journal entry number from the row's
// serial PK, zero-padded to 6 digits: JE-000001.
func FormatNumber(serialID int64) string {
	return fmt.Sprintf("%s-%06d", numberPrefix, serialID)
}
```

- [ ] **Step 4:** Run → PASS.
- [ ] **Step 5:** Commit — `git commit -m "feat(journal): add journal entry number formatting"`.

### Task 2.2: Closed-period check (pure comparison)
- [ ] **Step 1:** Write a failing test `journal/period_test.go`:

```go
package journal

import (
	"testing"
	"time"
)

func date(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

func TestIsClosed(t *testing.T) {
	tests := []struct {
		name          string
		effectiveDate time.Time
		closedThrough time.Time
		want          bool
	}{
		{"before closed date", date(2026, 6, 30), date(2026, 6, 30), true},
		{"on closed date", date(2026, 6, 30), date(2026, 6, 30), true},
		{"day after closed date", date(2026, 7, 1), date(2026, 6, 30), false},
		{"far after closed date", date(2026, 12, 31), date(2026, 6, 30), false},
		{"time-of-day is ignored", time.Date(2026, 6, 30, 23, 59, 0, 0, time.UTC), date(2026, 6, 30), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isClosed(tt.effectiveDate, tt.closedThrough); got != tt.want {
				t.Errorf("isClosed(%v, %v) = %v, want %v", tt.effectiveDate, tt.closedThrough, got, tt.want)
			}
		})
	}
}
```

- [ ] **Step 2:** Run → FAIL.
- [ ] **Step 3:** Implement `journal/period.go` — **only** the pure comparison. The DB-reading wrapper `IsPeriodClosed` is deliberately *not* here: it needs the `querier` interface, which Task 2.4 declares in `journal/store.go`, so it lives there instead, right next to it:

```go
package journal

import "time"

// isClosed reports whether effectiveDate falls on or before closedThrough,
// comparing calendar dates only (the time-of-day component is ignored, since
// closedThrough is always midnight and effectiveDate may not be — e.g. a
// reversal's effective date is time.Now()).
func isClosed(effectiveDate, closedThrough time.Time) bool {
	ey, em, ed := effectiveDate.Date()
	cy, cm, cd := closedThrough.Date()
	e := time.Date(ey, em, ed, 0, 0, 0, 0, time.UTC)
	c := time.Date(cy, cm, cd, 0, 0, 0, 0, time.UTC)
	return !e.After(c)
}
```

- [ ] **Step 4:** Run `go test ./journal/... -run TestIsClosed -v` → PASS (this file has no dependency on anything Task 2.4 introduces, so it compiles and passes standalone).
- [ ] **Step 5:** Commit — `git commit -m "feat(journal): add closed-period date comparison"`.

### Task 2.3: Journal types
- [ ] **Step 1:** No test — this is a pure data-shape file (mirrors `payment/types.go`, which also has no `_test.go`). Implement `journal/types.go`:

```go
package journal

import "time"

// LineInput is one caller-supplied debit or credit leg of an entry to create.
// Exactly one of Debit/Credit must be positive; the other must be zero.
type LineInput struct {
	AccountID int
	Debit     float64
	Credit    float64
}

// CreateEntryInput is what CreateEntry needs to post a new, balanced entry.
type CreateEntryInput struct {
	EntryDate              time.Time
	Memo                   string
	SourceType             string // e.g. "cash_transfer" — spec AD-2, no FK
	SourceID               string // the source document's UUID
	IsReversal             bool
	ReversesJournalEntryID int // internal journal_entry_id being reversed; 0 when not a reversal
	CreatedBy              int // employee id; 0 = unknown
	Lines                  []LineInput
}

// JournalEntryLine is one persisted leg of a posted entry.
type JournalEntryLine struct {
	LineNumber int
	AccountID  int
	Debit      float64
	Credit     float64
}

// JournalEntry is a persisted, balanced posting. InternalID is the row's
// serial PK — callers (document-module store layers) store this in their own
// header's journal_entry_id FK column; UUID is for display.
type JournalEntry struct {
	InternalID int
	UUID       string
	Number     string
	EntryDate  time.Time
	Memo       string
	SourceType string
	SourceID   string
	IsReversal bool
	Lines      []JournalEntryLine
}
```

- [ ] **Step 2:** `go build ./journal/...` → should still fail (querier/CreateEntry not defined yet) — expected, continue to Task 2.4.
- [ ] **Step 3:** Commit — `git commit -m "feat(journal): add journal entry types"`.

### Task 2.4: `CreateEntry` — validated, balanced posting
- [ ] **Step 1:** Write a failing test `journal/store_test.go` (the pure-validation half; no build tag, no database needed for these cases):

```go
package journal

import "testing"

func TestValidateLines(t *testing.T) {
	tests := []struct {
		name    string
		lines   []LineInput
		wantErr error
	}{
		{
			name: "balanced two lines ok",
			lines: []LineInput{
				{AccountID: 1, Debit: 100},
				{AccountID: 2, Credit: 100},
			},
			wantErr: nil,
		},
		{
			name:    "single line rejected",
			lines:   []LineInput{{AccountID: 1, Debit: 100}},
			wantErr: ErrInvalidLine,
		},
		{
			name: "both debit and credit on one line rejected",
			lines: []LineInput{
				{AccountID: 1, Debit: 100, Credit: 50},
				{AccountID: 2, Credit: 50},
			},
			wantErr: ErrInvalidLine,
		},
		{
			name: "zero line rejected",
			lines: []LineInput{
				{AccountID: 1, Debit: 0, Credit: 0},
				{AccountID: 2, Credit: 100},
			},
			wantErr: ErrInvalidLine,
		},
		{
			name: "unbalanced rejected",
			lines: []LineInput{
				{AccountID: 1, Debit: 100},
				{AccountID: 2, Credit: 99},
			},
			wantErr: ErrUnbalancedEntry,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateLines(tt.lines)
			if tt.wantErr == nil && err != nil {
				t.Errorf("validateLines() = %v, want nil", err)
			}
			if tt.wantErr != nil && err != tt.wantErr {
				t.Errorf("validateLines() = %v, want %v", err, tt.wantErr)
			}
		})
	}
}
```

- [ ] **Step 2:** Run `go test ./journal/... -run TestValidateLines -v` → FAIL (nothing defined).
- [ ] **Step 3:** Implement `journal/store.go`:

```go
package journal

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// querier is the subset of pgx behavior journal needs (consumer-side
// interface, structurally identical to workflow.Querier but declared locally
// so this package imports zero stonesuite-backend/* app packages — the same
// discipline query/ and ai/ follow). A *pgxpool.Pool or a pgx.Tx both satisfy
// it. Every exported function in this package takes one of these and expects
// it to be the caller's own in-flight pgx.Tx, so the journal write commits
// atomically with whatever header row the caller is posting on behalf of.
type querier interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// ErrUnbalancedEntry is returned when a proposed entry's debits and credits
// do not sum to the same amount.
var ErrUnbalancedEntry = errors.New("journal entry is not balanced")

// ErrInvalidLine is returned when fewer than two lines are given, or a line
// does not have exactly one of debit/credit populated and positive.
var ErrInvalidLine = errors.New("invalid journal entry line")

func round2(x float64) float64 { return math.Round(x*100) / 100 }

func nullableInt(v int) any {
	if v == 0 {
		return nil
	}
	return v
}

// validateLines enforces double-entry shape: at least two lines, each with
// exactly one side populated and positive, summing to the same total debit
// and total credit.
func validateLines(lines []LineInput) error {
	if len(lines) < 2 {
		return ErrInvalidLine
	}
	var totalDebit, totalCredit float64
	for _, l := range lines {
		if l.Debit < 0 || l.Credit < 0 {
			return ErrInvalidLine
		}
		if (l.Debit > 0) == (l.Credit > 0) {
			return ErrInvalidLine // both zero, or both positive
		}
		totalDebit += l.Debit
		totalCredit += l.Credit
	}
	if round2(totalDebit) != round2(totalCredit) {
		return ErrUnbalancedEntry
	}
	return nil
}

// IsPeriodClosed reports whether effectiveDate falls within the tenant's
// closed accounting period (spec AD-4: a single "books closed through" date).
// A nil books_closed_through means nothing is closed.
func IsPeriodClosed(ctx context.Context, q querier, effectiveDate time.Time) (bool, error) {
	var closedThrough *time.Time
	if err := q.QueryRow(ctx,
		`SELECT books_closed_through FROM accounting_settings WHERE accounting_settings_id = 1`,
	).Scan(&closedThrough); err != nil {
		return false, fmt.Errorf("load accounting settings: %w", err)
	}
	if closedThrough == nil {
		return false, nil
	}
	return isClosed(effectiveDate, *closedThrough), nil
}

// CreateEntry inserts a balanced journal entry and applies each line's signed
// effect (debit positive, credit negative) to coa_account.coa_account_balance
// — all against the caller-supplied querier, which MUST be an in-flight
// pgx.Tx so this write commits atomically with the caller's own header update
// (spec AD-1, AD-3).
func CreateEntry(ctx context.Context, q querier, in CreateEntryInput) (*JournalEntry, error) {
	if err := validateLines(in.Lines); err != nil {
		return nil, err
	}

	var newID int
	var newUUID string
	err := q.QueryRow(ctx, `
		INSERT INTO journal_entry (
			entry_date, memo, source_type, source_id, is_reversal,
			reverses_journal_entry_id, journal_entry_created_by
		) VALUES ($1,$2,$3,$4,$5,$6,$7)
		RETURNING journal_entry_id, journal_entry_uuid`,
		in.EntryDate, in.Memo, in.SourceType, in.SourceID, in.IsReversal,
		nullableInt(in.ReversesJournalEntryID), nullableInt(in.CreatedBy),
	).Scan(&newID, &newUUID)
	if err != nil {
		return nil, fmt.Errorf("insert journal entry: %w", err)
	}

	number := FormatNumber(int64(newID))
	if _, err := q.Exec(ctx,
		`UPDATE journal_entry SET journal_entry_number = $1 WHERE journal_entry_id = $2`,
		number, newID); err != nil {
		return nil, fmt.Errorf("set journal entry number: %w", err)
	}

	out := &JournalEntry{
		InternalID: newID, UUID: newUUID, Number: number,
		EntryDate: in.EntryDate, Memo: in.Memo,
		SourceType: in.SourceType, SourceID: in.SourceID, IsReversal: in.IsReversal,
	}
	for i, l := range in.Lines {
		lineNumber := i + 1
		if _, err := q.Exec(ctx, `
			INSERT INTO journal_entry_line (journal_entry_id, line_number, coa_account_id, debit, credit)
			VALUES ($1,$2,$3,$4,$5)`,
			newID, lineNumber, l.AccountID, l.Debit, l.Credit); err != nil {
			return nil, fmt.Errorf("insert journal entry line %d: %w", lineNumber, err)
		}
		delta := l.Debit - l.Credit
		if _, err := q.Exec(ctx, `
			UPDATE coa_account SET coa_account_balance = coa_account_balance + $2
			WHERE coa_account_id = $1`, l.AccountID, delta); err != nil {
			return nil, fmt.Errorf("update account balance for line %d: %w", lineNumber, err)
		}
		out.Lines = append(out.Lines, JournalEntryLine{
			LineNumber: lineNumber, AccountID: l.AccountID, Debit: l.Debit, Credit: l.Credit,
		})
	}
	return out, nil
}
```

`period.go` (Task 2.2) contains only `isClosed`; `IsPeriodClosed` — the DB-reading wrapper — lives in this file instead, since it needs the `querier` interface declared just above it.

- [ ] **Step 4:** Run `go test ./journal/... -run TestValidateLines -v` → PASS. Then `go build ./journal/...` → succeeds.
- [ ] **Step 5:** Commit — `git commit -m "feat(journal): add CreateEntry with balance validation and account balance updates"`.

### Task 2.5: `ReverseEntry`
- [ ] **Step 1:** No new pure-unit test here (this function's correctness is a database-backed property — "reversing an entry nets its balance impact to zero" — covered by the dbtest in Task 7.1). Implement `journal/reverse.go`:

```go
package journal

import (
	"context"
	"fmt"
	"time"
)

// ReverseEntry loads originalJournalEntryID's lines and posts a new entry
// with every line's debit and credit swapped, crediting back exactly what the
// original debited and vice versa, so the balance impact of the pair nets to
// zero. The new entry carries the same source_type/source_id as the
// original, plus is_reversal=true and reverses_journal_entry_id pointing back
// at it.
func ReverseEntry(ctx context.Context, q querier, originalJournalEntryID int, reversalDate time.Time, memo string, createdBy int) (*JournalEntry, error) {
	rows, err := q.Query(ctx, `
		SELECT jel.coa_account_id, jel.debit, jel.credit
		FROM journal_entry_line jel
		WHERE jel.journal_entry_id = $1
		ORDER BY jel.line_number`, originalJournalEntryID)
	if err != nil {
		return nil, fmt.Errorf("load original journal entry lines: %w", err)
	}
	var swapped []LineInput
	for rows.Next() {
		var accountID int
		var debit, credit float64
		if err := rows.Scan(&accountID, &debit, &credit); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan original journal entry line: %w", err)
		}
		swapped = append(swapped, LineInput{AccountID: accountID, Debit: credit, Credit: debit})
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate original journal entry lines: %w", err)
	}
	if len(swapped) == 0 {
		return nil, ErrInvalidLine
	}

	var sourceType, sourceID string
	if err := q.QueryRow(ctx,
		`SELECT source_type, source_id FROM journal_entry WHERE journal_entry_id = $1`,
		originalJournalEntryID,
	).Scan(&sourceType, &sourceID); err != nil {
		return nil, fmt.Errorf("load original journal entry source: %w", err)
	}

	return CreateEntry(ctx, q, CreateEntryInput{
		EntryDate:              reversalDate,
		Memo:                   memo,
		SourceType:             sourceType,
		SourceID:               sourceID,
		IsReversal:             true,
		ReversesJournalEntryID: originalJournalEntryID,
		CreatedBy:              createdBy,
		Lines:                  swapped,
	})
}
```

- [ ] **Step 2:** `go build ./journal/... && go vet ./journal/... && go test ./journal/...` → all succeed.
- [ ] **Step 3:** Commit — `git commit -m "feat(journal): add ReverseEntry"`.

---

## Phase 3 — `cashtransfer/` pure domain logic (TDD)

### Task 3.1: Cash transfer number formatting
- [ ] **Step 1:** Write a failing test `cashtransfer/numbering_test.go`:

```go
package cashtransfer

import "testing"

func TestFormatNumber(t *testing.T) {
	for in, want := range map[int64]string{1: "CTRF-000001", 42: "CTRF-000042", 1234567: "CTRF-1234567"} {
		if got := FormatNumber(in); got != want {
			t.Errorf("FormatNumber(%d) = %s, want %s", in, got, want)
		}
	}
}
```

- [ ] **Step 2:** Run → FAIL.
- [ ] **Step 3:** Implement `cashtransfer/numbering.go`:

```go
package cashtransfer

import "fmt"

const numberPrefix = "CTRF"

// FormatNumber renders the human-readable document number from the row's
// serial PK, zero-padded to 6 digits: CTRF-000001.
func FormatNumber(serialID int64) string {
	return fmt.Sprintf("%s-%06d", numberPrefix, serialID)
}
```

- [ ] **Step 4:** Run → PASS.
- [ ] **Step 5:** Commit — `git commit -m "feat(cashtransfer): add document number formatting"`.

### Task 3.2: Status transition map
- [ ] **Step 1:** Write a failing test `cashtransfer/transitions_test.go` covering every edge of spec AD-5's lifecycle (`DRFT → APPR → POST → RVSD`, `CANC` reachable from `DRFT`/`APPR` only):

```go
package cashtransfer

import "testing"

func TestTransitions(t *testing.T) {
	tests := []struct {
		from, to string
		want     bool
	}{
		{"DRFT", "APPR", true},
		{"DRFT", "CANC", true},
		{"DRFT", "POST", false},
		{"DRFT", "RVSD", false},
		{"APPR", "POST", true},
		{"APPR", "CANC", true},
		{"APPR", "DRFT", false},
		{"APPR", "RVSD", false},
		{"POST", "RVSD", true},
		{"POST", "CANC", false},
		{"POST", "APPR", false},
		{"CANC", "DRFT", false},
		{"CANC", "APPR", false},
		{"RVSD", "POST", false},
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

func TestIsPosted(t *testing.T) {
	tests := []struct {
		code string
		want bool
	}{
		{"DRFT", false}, {"APPR", false}, {"POST", true}, {"CANC", false}, {"RVSD", true},
	}
	for _, tt := range tests {
		if got := IsPosted(tt.code); got != tt.want {
			t.Errorf("IsPosted(%q) = %v, want %v", tt.code, got, tt.want)
		}
	}
}
```

- [ ] **Step 2:** Run → FAIL.
- [ ] **Step 3:** Implement `cashtransfer/transitions.go`:

```go
package cashtransfer

import "errors"

// Record type + status code constants (spec §3's CTRF seed).
const (
	recordTypeCode      = "CTRF"
	draftStatusCode     = "DRFT"
	approvedStatusCode  = "APPR"
	postedStatusCode    = "POST"
	cancelledStatusCode = "CANC"
	reversedStatusCode  = "RVSD"

	// sourceType is the journal_entry.source_type value this module writes
	// (spec AD-2).
	sourceType = "cash_transfer"
)

// ErrInvalidTransition is returned when a status change is not permitted.
var ErrInvalidTransition = errors.New("invalid cash transfer status transition")

// allowedTransitions maps a status code to the set of codes reachable from it
// (spec AD-5). Terminal states (CANC, RVSD) map to an empty set.
var allowedTransitions = map[string]map[string]bool{
	draftStatusCode:     {approvedStatusCode: true, cancelledStatusCode: true},
	approvedStatusCode:  {postedStatusCode: true, cancelledStatusCode: true},
	postedStatusCode:    {reversedStatusCode: true},
	cancelledStatusCode: {},
	reversedStatusCode:  {},
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

// IsPosted reports whether a status represents a transfer that has already
// moved money through the ledger — true for POST and RVSD (a reversed
// transfer is still "posted" in the sense that it can never be posted
// again). Mirrors itemreceipt.IsPosted's role guarding against double-post.
func IsPosted(code string) bool {
	return code == postedStatusCode || code == reversedStatusCode
}
```

- [ ] **Step 4:** Run → PASS.
- [ ] **Step 5:** Commit — `git commit -m "feat(cashtransfer): add status transition validation"`.

### Task 3.3: Query field resolver
- [ ] **Step 1:** Write a failing test `cashtransfer/resolver_test.go` (mirrors `payment/resolver_test.go`'s shape if one exists; otherwise mirrors the assertions implied by `payment/resolver.go`):

```go
package cashtransfer

import "testing"

func TestResolverResolve(t *testing.T) {
	r := resolver{}
	tests := []struct {
		key     string
		wantOK  bool
	}{
		{"id", true},
		{"document_number", true},
		{"record_number", true},
		{"status", true},
		{"from_account_id", true},
		{"to_account_id", true},
		{"amount", true},
		{"transfer_date", true},
		{"reference", true},
		{"owner_id", true},
		{"created_at", true},
		{"updated_at", true},
		{"cf:department", true},
		{"cf:INVALID KEY", false},
		{"not_a_real_field", false},
	}
	for _, tt := range tests {
		if _, _, ok := r.Resolve(tt.key); ok != tt.wantOK {
			t.Errorf("Resolve(%q) ok = %v, want %v", tt.key, ok, tt.wantOK)
		}
	}
}

func TestResolverSortExpr(t *testing.T) {
	r := resolver{}
	tests := []struct {
		key    string
		wantOK bool
	}{
		{"document_number", true},
		{"record_number", true},
		{"transfer_date", true},
		{"amount", true},
		{"status", true},
		{"not_sortable", false},
	}
	for _, tt := range tests {
		if _, _, ok := r.SortExpr(tt.key); ok != tt.wantOK {
			t.Errorf("SortExpr(%q) ok = %v, want %v", tt.key, ok, tt.wantOK)
		}
	}
}
```

- [ ] **Step 2:** Run → FAIL.
- [ ] **Step 3:** Implement `cashtransfer/resolver.go` (mirrors `payment/resolver.go` exactly, field names swapped):

```go
package cashtransfer

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
	"id":               {"ct.cash_transfer_uuid::text", query.TypeString},
	"document_number":  {"COALESCE(ct.cash_transfer_number,'')", query.TypeString},
	"record_number":    {"COALESCE(ct.cash_transfer_number,'')", query.TypeString},
	"status":           {"ct.cash_transfer_status::text", query.TypeString},
	"from_account_id":  {"ct.from_account_id::text", query.TypeString},
	"to_account_id":    {"ct.to_account_id::text", query.TypeString},
	"amount":           {"ct.cash_transfer_amount", query.TypeNumber},
	"transfer_date":    {"ct.cash_transfer_date", query.TypeDate},
	"reference":        {"ct.cash_transfer_reference", query.TypeString},
	"owner_id":         {"ct.cash_transfer_owner_id::text", query.TypeString},
	"created_by":       {"ct.cash_transfer_created_by::text", query.TypeString},
	"updated_by":       {"ct.cash_transfer_updated_by::text", query.TypeString},
	"created_at":       {"ct.cash_transfer_created_at", query.TypeDate},
	"updated_at":       {"ct.cash_transfer_updated_at", query.TypeDate},
}

func (resolver) Resolve(key string) (string, query.DataType, bool) {
	if s, ok := systemFields[key]; ok {
		return s.expr, s.dt, true
	}
	if k, ok := strings.CutPrefix(key, "cf:"); ok && validCustomKey.MatchString(k) {
		return "ct.cash_transfer_custom_fields->>'" + k + "'", query.TypeString, true
	}
	return "", "", false
}

var _ query.FieldResolver = resolver{}

var sortFields = map[string]resolved{
	"document_number": {"COALESCE(ct.cash_transfer_number,'')", query.TypeString},
	"record_number":   {"COALESCE(ct.cash_transfer_number,'')", query.TypeString},
	"transfer_date":   {"ct.cash_transfer_date", query.TypeDate},
	"amount":          {"ct.cash_transfer_amount", query.TypeNumber},
	"status":          {"ct.cash_transfer_status", query.TypeNumber},
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
		"ct.cash_transfer_number ILIKE '%'||" + ph + "||'%'" +
		" OR ct.cash_transfer_reference ILIKE '%'||" + ph + "||'%'" +
		" OR ct.cash_transfer_notes ILIKE '%'||" + ph + "||'%'" +
		")"
}

var _ query.SearchResolver = resolver{}
```

- [ ] **Step 4:** Run → PASS.
- [ ] **Step 5:** Commit — `git commit -m "feat(cashtransfer): add query field resolver"`.

### Task 3.4: Types and errors
- [ ] **Step 1:** No test (pure data shapes, mirrors `payment/types.go`). Implement `cashtransfer/types.go`:

```go
package cashtransfer

import "time"

// AccountRef is the flattened {id, code, name} for an account reference.
type AccountRef struct {
	ID   string `json:"id"`
	Code string `json:"code"`
	Name string `json:"name"`
}

// CashTransfer is the full record returned by Get/Search.
type CashTransfer struct {
	ID     string `json:"id"`
	Number string `json:"transferNumber"`

	StatusCode string `json:"statusCode"`
	StatusName string `json:"status"`

	TransferDate time.Time `json:"transferDate"`

	FromAccount AccountRef `json:"fromAccount"`
	ToAccount   AccountRef `json:"toAccount"`

	Amount    float64 `json:"amount"`
	Reference string  `json:"reference"`

	Notes         string `json:"notes"`
	InternalNotes string `json:"internalNotes"`

	OwnerUserID     string `json:"-"`
	OwnerEmployeeID *int   `json:"ownerEmployeeId,omitempty"`

	CustomFields map[string]any `json:"customFields"`

	JournalEntryID         *string    `json:"journalEntryId,omitempty"`
	ReversalJournalEntryID *string    `json:"reversalJournalEntryId,omitempty"`
	PostedAt               *time.Time `json:"postedAt,omitempty"`
	ReversedAt             *time.Time `json:"reversedAt,omitempty"`

	CreatedAt     time.Time `json:"createdAt"`
	UpdatedAt     time.Time `json:"updatedAt"`
	RecordVersion int       `json:"recordVersion"`
}

// CreateInput is the request payload for POST /api/tenant/finance/cash-transfers.
type CreateInput struct {
	FromAccountUUID string         `json:"fromAccountUuid"`
	ToAccountUUID   string         `json:"toAccountUuid"`
	Amount          float64        `json:"amount"`
	TransferDate    *time.Time     `json:"transferDate,omitempty"`
	Reference       string         `json:"reference"`
	Notes           string         `json:"notes"`
	InternalNotes   string         `json:"internalNotes"`
	OwnerEmployeeID *int           `json:"ownerEmployeeId,omitempty"`
	CustomFields    map[string]any `json:"customFields"`
}

// UpdateInput is the request payload for PATCH .../{uuid} (Draft only —
// unlike payment, amount IS editable here because nothing has posted yet).
type UpdateInput struct {
	FromAccountUUID string         `json:"fromAccountUuid"`
	ToAccountUUID   string         `json:"toAccountUuid"`
	Amount          float64        `json:"amount"`
	TransferDate    *time.Time     `json:"transferDate,omitempty"`
	Reference       string         `json:"reference"`
	Notes           string         `json:"notes"`
	InternalNotes   string         `json:"internalNotes"`
	OwnerEmployeeID *int           `json:"ownerEmployeeId,omitempty"`
	CustomFields    map[string]any `json:"customFields"`
}

// Page is one keyset-paginated slice of cash transfers.
type Page struct {
	Records    []CashTransfer `json:"records"`
	NextCursor string         `json:"nextCursor"`
	HasMore    bool           `json:"hasMore"`
}
```

- [ ] **Step 2:** Implement `cashtransfer/errors.go`:

```go
package cashtransfer

import "errors"

// ErrNotFound is returned when a cash transfer id matches no live row.
var ErrNotFound = errors.New("cash transfer not found")

// ErrAlreadyPosted is returned when Post is called on a transfer that has
// already moved through POST or RVSD (spec: prevent duplicate posting).
var ErrAlreadyPosted = errors.New("cash transfer is already posted")

// ErrNotPosted is returned when Reverse is called on a transfer that was
// never posted (spec: reverse only valid for posted transfers).
var ErrNotPosted = errors.New("cash transfer has not been posted")

// ErrPeriodClosed is returned when Post or Reverse's effective date falls
// within the closed accounting period (spec AD-4).
var ErrPeriodClosed = errors.New("accounting period is closed for this date")

// ClientError marks a caller-fault error (maps to HTTP 400).
type ClientError struct{ Msg string }

func (e ClientError) Error() string { return e.Msg }
```

- [ ] **Step 3:** `go build ./cashtransfer/...` — expected to still fail (no `store.go` yet defining things `types.go`/`errors.go` don't reference, so this actually succeeds in isolation). Run it to confirm.
- [ ] **Step 4:** Commit — `git commit -m "feat(cashtransfer): add types and error sentinels"`.

---

## Phase 4 — `cashtransfer/` store layer

### Task 4.1: Shared helpers + `Get`
- [ ] **Step 1:** No isolated unit test for this task (it is exercised end-to-end by Task 4.2 onward and by the Phase 7 dbtests — `Get` cannot be tested without `Create`, which doesn't exist yet). Implement `cashtransfer/store.go`:

```go
package cashtransfer

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/workflow"
)

const headerSelect = `
	SELECT ct.cash_transfer_uuid, COALESCE(ct.cash_transfer_number,''),
	       COALESCE(rs.record_status_code,''), COALESCE(rs.record_status_name,''),
	       ct.cash_transfer_date,
	       fa.coa_account_uuid, fa.coa_account_code, fa.coa_account_name,
	       ta.coa_account_uuid, ta.coa_account_code, ta.coa_account_name,
	       ct.cash_transfer_amount, ct.cash_transfer_reference,
	       ct.cash_transfer_notes, ct.cash_transfer_internal_notes,
	       COALESCE(ou.id::text,''), ct.cash_transfer_owner_id,
	       ct.cash_transfer_custom_fields,
	       je.journal_entry_uuid, rje.journal_entry_uuid,
	       ct.cash_transfer_posted_at, ct.cash_transfer_reversed_at,
	       ct.cash_transfer_created_at, ct.cash_transfer_updated_at, ct.cash_transfer_record_version,
	       ct.cash_transfer_id, ct.cash_transfer_status, ct.from_account_id, ct.to_account_id
	FROM cash_transfer ct
	JOIN lkp_record_status rs ON rs.record_status_id = ct.cash_transfer_status
	JOIN coa_account fa ON fa.coa_account_id = ct.from_account_id
	JOIN coa_account ta ON ta.coa_account_id = ct.to_account_id
	LEFT JOIN employee oe ON oe.employee_id = ct.cash_transfer_owner_id
	LEFT JOIN users ou ON ou.id = oe.employee_user_id
	LEFT JOIN journal_entry je ON je.journal_entry_id = ct.journal_entry_id
	LEFT JOIN journal_entry rje ON rje.journal_entry_id = ct.reversal_journal_entry_id`

type ctMeta struct {
	internalID     int
	statusID       int
	fromAccountID  int
	toAccountID    int
}

func scanCT(row pgx.Row) (*CashTransfer, ctMeta, error) {
	var (
		ct         CashTransfer
		ownerEmpID *int
		custom     map[string]any
		jeUUID     *string
		rjeUUID    *string
		meta       ctMeta
	)
	err := row.Scan(
		&ct.ID, &ct.Number,
		&ct.StatusCode, &ct.StatusName,
		&ct.TransferDate,
		&ct.FromAccount.ID, &ct.FromAccount.Code, &ct.FromAccount.Name,
		&ct.ToAccount.ID, &ct.ToAccount.Code, &ct.ToAccount.Name,
		&ct.Amount, &ct.Reference,
		&ct.Notes, &ct.InternalNotes,
		&ct.OwnerUserID, &ownerEmpID,
		&custom,
		&jeUUID, &rjeUUID,
		&ct.PostedAt, &ct.ReversedAt,
		&ct.CreatedAt, &ct.UpdatedAt, &ct.RecordVersion,
		&meta.internalID, &meta.statusID, &meta.fromAccountID, &meta.toAccountID,
	)
	if err != nil {
		return nil, ctMeta{}, err
	}
	ct.OwnerEmployeeID = ownerEmpID
	if custom == nil {
		custom = map[string]any{}
	}
	ct.CustomFields = custom
	ct.JournalEntryID = jeUUID
	ct.ReversalJournalEntryID = rjeUUID
	return &ct, meta, nil
}

// Get loads a single live cash transfer by its external uuid.
func Get(ctx context.Context, pool *pgxpool.Pool, id string) (*CashTransfer, error) {
	ct, _, err := scanCT(pool.QueryRow(ctx, headerSelect+`
		WHERE ct.cash_transfer_uuid = $1 AND ct.cash_transfer_deleted_at IS NULL`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("get cash transfer: %w", err)
	}
	return ct, nil
}

func recordTypeIDByCode(ctx context.Context, q workflow.Querier, code string) (int, error) {
	var id int
	if err := q.QueryRow(ctx,
		`SELECT record_type_id FROM lkp_record_type WHERE record_type_code = $1`, code).Scan(&id); err != nil {
		return 0, fmt.Errorf("resolve record type %s: %w", code, err)
	}
	return id, nil
}

func statusIDByCode(ctx context.Context, q workflow.Querier, typeID int, code string) (int, error) {
	var id int
	if err := q.QueryRow(ctx,
		`SELECT record_status_id FROM lkp_record_status WHERE record_status_record_type = $1 AND record_status_code = $2`,
		typeID, code).Scan(&id); err != nil {
		return 0, fmt.Errorf("resolve status %s: %w", code, err)
	}
	return id, nil
}

func nullableInt(v int) any {
	if v == 0 {
		return nil
	}
	return v
}

// writeHistory records one row to cash_transfer_history. Best-effort, like
// itemreceipt.writeHistory — a history-write failure must never roll back the
// primary operation it is documenting.
func writeHistory(ctx context.Context, tx pgx.Tx, ctInternalID int, action string, fromStatusID, toStatusID *int, actorEmployeeID int) {
	_, _ = tx.Exec(ctx, `
		INSERT INTO cash_transfer_history (cash_transfer_id, from_status_id, to_status_id, history_action, history_by)
		VALUES ($1,$2,$3,$4,$5)`,
		ctInternalID, fromStatusID, toStatusID, action, nullableInt(actorEmployeeID))
}

// validateCustom validates in.CustomFields against the "cash_transfer"
// workflow's field definitions, if one has been seeded. No-ops when it
// hasn't (mirrors payment.validateCustom).
func validateCustom(ctx context.Context, pool *pgxpool.Pool, custom map[string]any) error {
	if custom == nil {
		return nil
	}
	wf, err := workflow.GetWorkflowByKey(ctx, pool, "cash_transfer")
	if errors.Is(err, workflow.ErrWorkflowNotFound) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("load cash_transfer workflow: %w", err)
	}
	def, err := workflow.LoadDefinition(ctx, pool, wf.ID)
	if err != nil {
		return fmt.Errorf("load cash_transfer field definitions: %w", err)
	}
	if err := workflow.ValidateCustomFieldsPartial(def.Fields, custom); err != nil {
		return ClientError{Msg: err.Error()}
	}
	return nil
}

// resolveAccount validates that accountUUID names a live, active, postable
// Bank or Cash account (spec AD-7) and returns its internal id. label is
// "Source" or "Destination", used in the error message.
func resolveAccount(ctx context.Context, pool *pgxpool.Pool, accountUUID, label string) (int, error) {
	var id int
	var acctType string
	var active, postable bool
	err := pool.QueryRow(ctx, `
		SELECT coa_account_id, coa_account_type, coa_account_is_active, coa_account_is_postable
		FROM coa_account
		WHERE coa_account_uuid = $1 AND coa_account_deleted_at IS NULL`, accountUUID,
	).Scan(&id, &acctType, &active, &postable)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, ClientError{Msg: fmt.Sprintf("Unknown %s account.", label)}
	}
	if err != nil {
		return 0, fmt.Errorf("resolve %s account: %w", label, err)
	}
	if acctType != "bank" && acctType != "cash" {
		return 0, ClientError{Msg: fmt.Sprintf("%s account must be a Bank or Cash account.", label)}
	}
	if !active || !postable {
		return 0, ClientError{Msg: fmt.Sprintf("%s account is not active and postable.", label)}
	}
	return id, nil
}
```

- [ ] **Step 2:** `go build ./cashtransfer/...` → succeeds.
- [ ] **Step 3:** Commit — `git commit -m "feat(cashtransfer): add shared store helpers and Get"`.

### Task 4.2: `Create`
- [ ] **Step 1:** No isolated unit test (requires a live database — covered by Phase 7's dbtest `TestCreate_Validations`). Implement `cashtransfer/store_create.go`:

```go
package cashtransfer

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Create inserts a new Draft cash transfer inside one transaction: validates
// the two accounts (different, active, postable, Bank/Cash — spec AD-7),
// validates custom fields, inserts the header, assigns the document number,
// and writes the 'create' history row.
func Create(ctx context.Context, pool *pgxpool.Pool, in CreateInput, actorEmployeeID int) (*CashTransfer, error) {
	if in.Amount <= 0 {
		return nil, ClientError{Msg: "amount must be positive."}
	}
	if in.FromAccountUUID == "" || in.ToAccountUUID == "" {
		return nil, ClientError{Msg: "fromAccountUuid and toAccountUuid are required."}
	}
	if in.FromAccountUUID == in.ToAccountUUID {
		return nil, ClientError{Msg: "source and destination accounts must be different."}
	}

	fromID, err := resolveAccount(ctx, pool, in.FromAccountUUID, "Source")
	if err != nil {
		return nil, err
	}
	toID, err := resolveAccount(ctx, pool, in.ToAccountUUID, "Destination")
	if err != nil {
		return nil, err
	}
	if fromID == toID {
		return nil, ClientError{Msg: "source and destination accounts must be different."}
	}

	custom := in.CustomFields
	if custom == nil {
		custom = map[string]any{}
	}
	if err := validateCustom(ctx, pool, custom); err != nil {
		return nil, err
	}

	typeID, err := recordTypeIDByCode(ctx, pool, recordTypeCode)
	if err != nil {
		return nil, err
	}
	draftStatusID, err := statusIDByCode(ctx, pool, typeID, draftStatusCode)
	if err != nil {
		return nil, err
	}

	ownerEmp := in.OwnerEmployeeID
	if ownerEmp == nil && actorEmployeeID != 0 {
		ownerEmp = &actorEmployeeID
	}

	transferDate := time.Now()
	if in.TransferDate != nil {
		transferDate = *in.TransferDate
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin create cash transfer: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var newID int
	var newUUID string
	err = tx.QueryRow(ctx, `
		INSERT INTO cash_transfer (
			record_type, cash_transfer_status, cash_transfer_date,
			from_account_id, to_account_id, cash_transfer_amount, cash_transfer_reference,
			cash_transfer_notes, cash_transfer_internal_notes,
			cash_transfer_owner_id, cash_transfer_custom_fields,
			cash_transfer_created_by, cash_transfer_updated_by
		) VALUES (
			$1,$2,$3, $4,$5,$6,$7, $8,$9, $10,$11, $12,$12
		) RETURNING cash_transfer_id, cash_transfer_uuid`,
		typeID, draftStatusID, transferDate,
		fromID, toID, in.Amount, in.Reference,
		in.Notes, in.InternalNotes,
		ownerEmp, custom, nullableInt(actorEmployeeID),
	).Scan(&newID, &newUUID)
	if err != nil {
		return nil, fmt.Errorf("insert cash transfer: %w", err)
	}

	number := FormatNumber(int64(newID))
	if _, err := tx.Exec(ctx, `UPDATE cash_transfer SET cash_transfer_number = $1 WHERE cash_transfer_id = $2`,
		number, newID); err != nil {
		return nil, fmt.Errorf("set cash transfer number: %w", err)
	}

	writeHistory(ctx, tx, newID, "create", nil, &draftStatusID, actorEmployeeID)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit create cash transfer: %w", err)
	}
	return Get(ctx, pool, newUUID)
}
```

- [ ] **Step 2:** `go build ./cashtransfer/...` → succeeds.
- [ ] **Step 3:** Commit — `git commit -m "feat(cashtransfer): add Create with account validation"`.

### Task 4.3: `Update` and `SoftDelete`
- [ ] **Step 1:** No isolated unit test (dbtest-covered in Phase 7). Implement `cashtransfer/store_update.go`:

```go
package cashtransfer

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Update edits a cash transfer's fields. Only a Draft transfer may be edited
// (spec: "prevent edits after posting") — every other status returns a
// ClientError (400).
func Update(ctx context.Context, pool *pgxpool.Pool, uuid string, in UpdateInput, actorEmployeeID int) (*CashTransfer, error) {
	if in.Amount <= 0 {
		return nil, ClientError{Msg: "amount must be positive."}
	}
	if in.FromAccountUUID == "" || in.ToAccountUUID == "" {
		return nil, ClientError{Msg: "fromAccountUuid and toAccountUuid are required."}
	}
	if in.FromAccountUUID == in.ToAccountUUID {
		return nil, ClientError{Msg: "source and destination accounts must be different."}
	}
	fromID, err := resolveAccount(ctx, pool, in.FromAccountUUID, "Source")
	if err != nil {
		return nil, err
	}
	toID, err := resolveAccount(ctx, pool, in.ToAccountUUID, "Destination")
	if err != nil {
		return nil, err
	}
	if fromID == toID {
		return nil, ClientError{Msg: "source and destination accounts must be different."}
	}
	custom := in.CustomFields
	if custom == nil {
		custom = map[string]any{}
	}
	if err := validateCustom(ctx, pool, custom); err != nil {
		return nil, err
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin update cash transfer: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var internalID, curStatusID int
	var curStatusCode string
	err = tx.QueryRow(ctx, `
		SELECT ct.cash_transfer_id, ct.cash_transfer_status, rs.record_status_code
		FROM cash_transfer ct
		JOIN lkp_record_status rs ON rs.record_status_id = ct.cash_transfer_status
		WHERE ct.cash_transfer_uuid = $1 AND ct.cash_transfer_deleted_at IS NULL
		FOR UPDATE OF ct`, uuid,
	).Scan(&internalID, &curStatusID, &curStatusCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load cash transfer for update: %w", err)
	}
	if curStatusCode != draftStatusCode {
		return nil, ClientError{Msg: "Only a draft cash transfer can be edited."}
	}

	if _, err := tx.Exec(ctx, `
		UPDATE cash_transfer SET
			from_account_id = $2, to_account_id = $3, cash_transfer_amount = $4,
			cash_transfer_date = COALESCE($5, cash_transfer_date),
			cash_transfer_reference = $6, cash_transfer_notes = $7, cash_transfer_internal_notes = $8,
			cash_transfer_owner_id = COALESCE($9, cash_transfer_owner_id),
			cash_transfer_custom_fields = $10,
			cash_transfer_updated_at = NOW(), cash_transfer_updated_by = $11,
			cash_transfer_record_version = cash_transfer_record_version + 1
		WHERE cash_transfer_id = $1`,
		internalID, fromID, toID, in.Amount, in.TransferDate,
		in.Reference, in.Notes, in.InternalNotes, in.OwnerEmployeeID, custom, nullableInt(actorEmployeeID),
	); err != nil {
		return nil, fmt.Errorf("update cash transfer: %w", err)
	}
	writeHistory(ctx, tx, internalID, "update", &curStatusID, &curStatusID, actorEmployeeID)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit update cash transfer: %w", err)
	}
	return Get(ctx, pool, uuid)
}

// SoftDelete removes a Draft cash transfer (spec AD-9: CRUD parity; Draft
// only, same guard as Update).
func SoftDelete(ctx context.Context, pool *pgxpool.Pool, uuid string, actorEmployeeID int) error {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin delete cash transfer: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var internalID, curStatusID int
	var curStatusCode string
	err = tx.QueryRow(ctx, `
		SELECT ct.cash_transfer_id, ct.cash_transfer_status, rs.record_status_code
		FROM cash_transfer ct
		JOIN lkp_record_status rs ON rs.record_status_id = ct.cash_transfer_status
		WHERE ct.cash_transfer_uuid = $1 AND ct.cash_transfer_deleted_at IS NULL
		FOR UPDATE OF ct`, uuid,
	).Scan(&internalID, &curStatusID, &curStatusCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("load cash transfer for delete: %w", err)
	}
	if curStatusCode != draftStatusCode {
		return ClientError{Msg: "Only a draft cash transfer can be deleted."}
	}

	if _, err := tx.Exec(ctx, `
		UPDATE cash_transfer SET
			cash_transfer_deleted_at = NOW(), cash_transfer_deleted_by = $2,
			cash_transfer_updated_at = NOW(), cash_transfer_updated_by = $2,
			cash_transfer_record_version = cash_transfer_record_version + 1
		WHERE cash_transfer_id = $1`, internalID, nullableInt(actorEmployeeID)); err != nil {
		return fmt.Errorf("delete cash transfer: %w", err)
	}
	writeHistory(ctx, tx, internalID, "delete", &curStatusID, nil, actorEmployeeID)

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit delete cash transfer: %w", err)
	}
	return nil
}
```

- [ ] **Step 2:** `go build ./cashtransfer/...` → succeeds.
- [ ] **Step 3:** Commit — `git commit -m "feat(cashtransfer): add Update and SoftDelete, both Draft-only"`.

### Task 4.4: `Search`
- [ ] **Step 1:** No isolated unit test (dbtest-covered). Implement `cashtransfer/store_search.go` (mirrors `payment/search.go`):

```go
package cashtransfer

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

// Search lists live cash transfers with server-side filter/sort/global-search
// + keyset pagination.
func Search(ctx context.Context, pool *pgxpool.Pool, scope, actorIdentityID string, req query.Request) (Page, error) {
	where := []string{"ct.cash_transfer_deleted_at IS NULL"}
	args := []any{}
	nextIdx := 1
	if scope != string(authz.ScopeAll) {
		empID, found := workflow.EmployeeIDByIdentity(ctx, pool, actorIdentityID)
		if !found {
			return Page{}, nil
		}
		where = append(where, fmt.Sprintf("ct.cash_transfer_owner_id = $%d", nextIdx))
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
		return Page{}, fmt.Errorf("search cash transfers: %w", err)
	}
	defer rows.Close()
	out := []CashTransfer{}
	metas := []ctMeta{}
	for rows.Next() {
		ct, meta, err := scanCT(rows)
		if err != nil {
			return Page{}, fmt.Errorf("scan cash transfer: %w", err)
		}
		out = append(out, *ct)
		metas = append(metas, meta)
	}
	if err := rows.Err(); err != nil {
		return Page{}, fmt.Errorf("search cash transfers: %w", err)
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

func sortValue(ct CashTransfer, meta ctMeta, field string) any {
	switch field {
	case "updated_at":
		return ct.UpdatedAt
	case "document_number", "record_number":
		return ct.Number
	case "transfer_date":
		return ct.TransferDate
	case "amount":
		return ct.Amount
	case "status":
		return meta.statusID
	default: // created_at (default)
		return ct.CreatedAt
	}
}
```

- [ ] **Step 2:** `go build ./cashtransfer/...` → succeeds.
- [ ] **Step 3:** Commit — `git commit -m "feat(cashtransfer): add keyset Search"`.

### Task 4.5: `Transition` (Approve, Cancel)
- [ ] **Step 1:** No isolated unit test (dbtest-covered). Implement `cashtransfer/store_transition.go` (mirrors `itemreceipt/store_transition.go`):

```go
// cashtransfer/store_transition.go
package cashtransfer

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Transition moves a live cash transfer to toStatusCode. It is deliberately
// the *narrow* status endpoint (spec AD-6): the two moves with side effects —
// Post (creates a journal entry, moves balances) and Reverse (creates a
// reversing entry) — are refused here and routed to their own functions. What
// remains is Approve (DRFT→APPR) and Cancel (DRFT/APPR→CANC), neither of
// which touches the ledger.
func Transition(ctx context.Context, pool *pgxpool.Pool, uuid, toStatusCode string, actorEmployeeID int) (*CashTransfer, error) {
	switch toStatusCode {
	case postedStatusCode:
		return nil, ClientError{Msg: "Use the post endpoint to post a cash transfer."}
	case reversedStatusCode:
		return nil, ClientError{Msg: "Use the reverse endpoint to reverse a posted cash transfer."}
	}

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin transition: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var internalID, curStatusID int
	var curStatusCode string
	err = tx.QueryRow(ctx, `
		SELECT ct.cash_transfer_id, ct.cash_transfer_status, rs.record_status_code
		FROM cash_transfer ct
		JOIN lkp_record_status rs ON rs.record_status_id = ct.cash_transfer_status
		WHERE ct.cash_transfer_uuid = $1 AND ct.cash_transfer_deleted_at IS NULL
		FOR UPDATE OF ct`, uuid,
	).Scan(&internalID, &curStatusID, &curStatusCode)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load cash transfer for transition: %w", err)
	}
	if err := ValidateTransition(curStatusCode, toStatusCode); err != nil {
		return nil, err
	}

	recordTypeID, err := recordTypeIDByCode(ctx, tx, recordTypeCode)
	if err != nil {
		return nil, fmt.Errorf("resolve CTRF record type: %w", err)
	}
	toStatusID, err := statusIDByCode(ctx, tx, recordTypeID, toStatusCode)
	if err != nil {
		return nil, ClientError{Msg: "Unknown target status."}
	}

	if _, err := tx.Exec(ctx, `
		UPDATE cash_transfer SET
			cash_transfer_status = $2,
			cash_transfer_updated_at = NOW(), cash_transfer_updated_by = $3,
			cash_transfer_record_version = cash_transfer_record_version + 1
		WHERE cash_transfer_id = $1`, internalID, toStatusID, nullableInt(actorEmployeeID)); err != nil {
		return nil, fmt.Errorf("transition cash transfer: %w", err)
	}
	writeHistory(ctx, tx, internalID, "transition", &curStatusID, &toStatusID, actorEmployeeID)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit transition: %w", err)
	}
	return Get(ctx, pool, uuid)
}
```

- [ ] **Step 2:** `go build ./cashtransfer/...` → succeeds.
- [ ] **Step 3:** Commit — `git commit -m "feat(cashtransfer): add generic Transition for Approve/Cancel"`.

### Task 4.6: `Post`
- [ ] **Step 1:** No isolated unit test (the correctness property here — exactly-once posting under concurrency — is a database-backed property, covered by Phase 7's `TestPost_ConcurrentDoublePost`). Implement `cashtransfer/store_post.go`:

```go
// cashtransfer/store_post.go — the act that makes a transfer real.
package cashtransfer

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/journal"
)

// Post advances an Approved transfer to Posted: it locks the header row,
// verifies it hasn't already been posted, checks the accounting period isn't
// closed, builds a balanced two-line journal entry (debit the destination
// account, credit the source account), and updates both accounts' running
// balances — all inside one transaction, so a concurrent second Post call
// blocks on the row lock and then observes the already-posted status instead
// of double-posting (mirrors itemreceipt.Post).
func Post(ctx context.Context, pool *pgxpool.Pool, uuid string, actorEmployeeID int) (*CashTransfer, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin post cash transfer: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var ctInternalID, curStatusID, fromAccountID, toAccountID int
	var curStatusCode string
	var amount float64
	var transferDate time.Time
	err = tx.QueryRow(ctx, `
		SELECT ct.cash_transfer_id, ct.cash_transfer_status, rs.record_status_code,
		       ct.from_account_id, ct.to_account_id, ct.cash_transfer_amount, ct.cash_transfer_date
		FROM cash_transfer ct
		JOIN lkp_record_status rs ON rs.record_status_id = ct.cash_transfer_status
		WHERE ct.cash_transfer_uuid = $1 AND ct.cash_transfer_deleted_at IS NULL
		FOR UPDATE OF ct`, uuid,
	).Scan(&ctInternalID, &curStatusID, &curStatusCode, &fromAccountID, &toAccountID, &amount, &transferDate)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load cash transfer for posting: %w", err)
	}
	if IsPosted(curStatusCode) {
		return nil, ErrAlreadyPosted
	}
	if err := ValidateTransition(curStatusCode, postedStatusCode); err != nil {
		return nil, err
	}

	closed, err := journal.IsPeriodClosed(ctx, tx, transferDate)
	if err != nil {
		return nil, fmt.Errorf("check accounting period: %w", err)
	}
	if closed {
		return nil, ErrPeriodClosed
	}

	recordTypeID, err := recordTypeIDByCode(ctx, tx, recordTypeCode)
	if err != nil {
		return nil, fmt.Errorf("resolve CTRF record type: %w", err)
	}
	postedStatusID, err := statusIDByCode(ctx, tx, recordTypeID, postedStatusCode)
	if err != nil {
		return nil, fmt.Errorf("resolve POST status: %w", err)
	}

	entry, err := journal.CreateEntry(ctx, tx, journal.CreateEntryInput{
		EntryDate:  transferDate,
		Memo:       fmt.Sprintf("Cash transfer %s", uuid),
		SourceType: sourceType,
		SourceID:   uuid,
		CreatedBy:  actorEmployeeID,
		Lines: []journal.LineInput{
			{AccountID: toAccountID, Debit: amount},
			{AccountID: fromAccountID, Credit: amount},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create journal entry: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE cash_transfer SET
			cash_transfer_status = $2,
			journal_entry_id = $3,
			cash_transfer_posted_at = NOW(),
			cash_transfer_posted_by = $4,
			cash_transfer_updated_at = NOW(),
			cash_transfer_updated_by = $4,
			cash_transfer_record_version = cash_transfer_record_version + 1
		WHERE cash_transfer_id = $1`,
		ctInternalID, postedStatusID, entry.InternalID, nullableInt(actorEmployeeID)); err != nil {
		return nil, fmt.Errorf("post cash transfer: %w", err)
	}
	writeHistory(ctx, tx, ctInternalID, "post", &curStatusID, &postedStatusID, actorEmployeeID)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit post cash transfer: %w", err)
	}
	return Get(ctx, pool, uuid)
}
```

- [ ] **Step 2:** `go build ./cashtransfer/...` → succeeds.
- [ ] **Step 3:** Commit — `git commit -m "feat(cashtransfer): add Post — creates a balanced journal entry exactly once"`.

### Task 4.7: `Reverse`
- [ ] **Step 1:** No isolated unit test (dbtest-covered). Implement `cashtransfer/store_reverse.go`:

```go
// cashtransfer/store_reverse.go — the reversal of Post.
package cashtransfer

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/journal"
)

// Reverse creates a reversing journal entry for a Posted transfer, restoring
// both accounts' balances to their pre-post values, and moves the transfer to
// Reversed. Only valid from Posted (spec: "Reverse only valid for
// already-posted transfers").
func Reverse(ctx context.Context, pool *pgxpool.Pool, uuid string, actorEmployeeID int) (*CashTransfer, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin reverse cash transfer: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var ctInternalID, curStatusID int
	var curStatusCode string
	var journalEntryID *int
	err = tx.QueryRow(ctx, `
		SELECT ct.cash_transfer_id, ct.cash_transfer_status, rs.record_status_code, ct.journal_entry_id
		FROM cash_transfer ct
		JOIN lkp_record_status rs ON rs.record_status_id = ct.cash_transfer_status
		WHERE ct.cash_transfer_uuid = $1 AND ct.cash_transfer_deleted_at IS NULL
		FOR UPDATE OF ct`, uuid,
	).Scan(&ctInternalID, &curStatusID, &curStatusCode, &journalEntryID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("load cash transfer for reversal: %w", err)
	}
	if curStatusCode != postedStatusCode {
		return nil, ErrNotPosted
	}
	if journalEntryID == nil {
		return nil, fmt.Errorf("posted cash transfer %s has no journal_entry_id (data invariant violated)", uuid)
	}

	reversalDate := time.Now()
	closed, err := journal.IsPeriodClosed(ctx, tx, reversalDate)
	if err != nil {
		return nil, fmt.Errorf("check accounting period: %w", err)
	}
	if closed {
		return nil, ErrPeriodClosed
	}

	reversingEntry, err := journal.ReverseEntry(ctx, tx, *journalEntryID, reversalDate,
		fmt.Sprintf("Reversal of cash transfer %s", uuid), actorEmployeeID)
	if err != nil {
		return nil, fmt.Errorf("create reversing journal entry: %w", err)
	}

	recordTypeID, err := recordTypeIDByCode(ctx, tx, recordTypeCode)
	if err != nil {
		return nil, fmt.Errorf("resolve CTRF record type: %w", err)
	}
	reversedStatusID, err := statusIDByCode(ctx, tx, recordTypeID, reversedStatusCode)
	if err != nil {
		return nil, fmt.Errorf("resolve RVSD status: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE cash_transfer SET
			cash_transfer_status = $2,
			reversal_journal_entry_id = $3,
			cash_transfer_reversed_at = NOW(),
			cash_transfer_reversed_by = $4,
			cash_transfer_updated_at = NOW(),
			cash_transfer_updated_by = $4,
			cash_transfer_record_version = cash_transfer_record_version + 1
		WHERE cash_transfer_id = $1`,
		ctInternalID, reversedStatusID, reversingEntry.InternalID, nullableInt(actorEmployeeID)); err != nil {
		return nil, fmt.Errorf("reverse cash transfer: %w", err)
	}
	writeHistory(ctx, tx, ctInternalID, "reverse", &curStatusID, &reversedStatusID, actorEmployeeID)

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit reverse cash transfer: %w", err)
	}
	return Get(ctx, pool, uuid)
}
```

- [ ] **Step 2:** `go build ./cashtransfer/... && go vet ./cashtransfer/... && go test ./cashtransfer/...` → all succeed.
- [ ] **Step 3:** Commit — `git commit -m "feat(cashtransfer): add Reverse — creates a reversing journal entry"`.

---

## Phase 5 — Controllers

### Task 5.1: CRUD handlers + auth skeleton
- [ ] **Step 1:** Read `controllers/payment.go` in full (the reference auth skeleton — the only one that logs `permission_denied`). Implement `controllers/cashtransfer.go`:

```go
package controllers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/cashtransfer"
	"stonesuite-backend/middleware"
	"stonesuite-backend/models"
	"stonesuite-backend/query"
	"stonesuite-backend/tenancy"
)

type CashTransferOps struct{}

func NewCashTransferOps() *CashTransferOps { return &CashTransferOps{} }

func (h *CashTransferOps) authCashTransfer(w http.ResponseWriter, r *http.Request, action authz.Action) (*pgxpool.Pool, string, authz.Scope, bool) {
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
	decision, err := authz.Check(r.Context(), pool, payload.ID, authz.ResourceCashTransfer, action)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, "", "", false
	}
	if !decision.Allowed {
		logSecurityEvent(r, "permission_denied",
			"identity", payload.ID, "resource", string(authz.ResourceCashTransfer), "action", string(action))
		fail(w, http.StatusForbidden, "You do not have permission to "+string(action)+" cash transfers.")
		return nil, "", "", false
	}
	return pool, payload.ID, decision.Scope, true
}

func (h *CashTransferOps) authCashTransferByUUID(w http.ResponseWriter, r *http.Request, uuid string, action authz.Action) (*pgxpool.Pool, string, authz.Scope, bool) {
	pool, identityID, scope, ok := h.authCashTransfer(w, r, action)
	if !ok {
		return nil, "", "", false
	}
	if scope == authz.ScopeAll {
		return pool, identityID, scope, true
	}
	ct, err := cashtransfer.Get(r.Context(), pool, uuid)
	if errors.Is(err, cashtransfer.ErrNotFound) {
		fail(w, http.StatusNotFound, "Cash transfer not found.")
		return nil, "", "", false
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load cash transfer.")
		return nil, "", "", false
	}
	allowed, aerr := recordInScope(r.Context(), pool, scope, identityID, ct.OwnerUserID)
	if aerr != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, "", "", false
	}
	if !allowed {
		logSecurityEvent(r, "idor_denied",
			"identity", identityID, "record", uuid, "resource", string(authz.ResourceCashTransfer),
			"action", string(action), "scope", string(scope))
		fail(w, http.StatusNotFound, "Cash transfer not found.")
		return nil, "", "", false
	}
	return pool, identityID, scope, true
}

func ctFail(w http.ResponseWriter, err error, serverMsg string) {
	switch {
	case errors.Is(err, cashtransfer.ErrNotFound):
		fail(w, http.StatusNotFound, "Cash transfer not found.")
	case errors.Is(err, cashtransfer.ErrInvalidTransition):
		fail(w, http.StatusConflict, err.Error())
	case errors.Is(err, cashtransfer.ErrAlreadyPosted):
		fail(w, http.StatusConflict, err.Error())
	case errors.Is(err, cashtransfer.ErrNotPosted):
		fail(w, http.StatusConflict, err.Error())
	case errors.Is(err, cashtransfer.ErrPeriodClosed):
		fail(w, http.StatusConflict, err.Error())
	default:
		var ce cashtransfer.ClientError
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

func (h *CashTransferOps) Create(w http.ResponseWriter, r *http.Request) {
	pool, identityID, _, ok := h.authCashTransfer(w, r, authz.ActionCreate)
	if !ok {
		return
	}
	var in cashtransfer.CreateInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	empID := resolveEmployeeID(r, identityID)
	ct, err := cashtransfer.Create(r.Context(), pool, in, empID)
	if err != nil {
		ctFail(w, err, "Failed to create cash transfer.")
		return
	}
	auditCT(r, pool, empID, "create", ct.ID, nil, ct)
	writeJSON(w, http.StatusCreated, map[string]any{"success": true, "cashTransfer": ct})
}

func (h *CashTransferOps) Get(w http.ResponseWriter, r *http.Request) {
	pool, _, _, ok := h.authCashTransferByUUID(w, r, r.PathValue("uuid"), authz.ActionRead)
	if !ok {
		return
	}
	ct, err := cashtransfer.Get(r.Context(), pool, r.PathValue("uuid"))
	if err != nil {
		ctFail(w, err, "Failed to load cash transfer.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "cashTransfer": ct})
}

func (h *CashTransferOps) Update(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, identityID, _, ok := h.authCashTransferByUUID(w, r, id, authz.ActionUpdate)
	if !ok {
		return
	}
	var in cashtransfer.UpdateInput
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		fail(w, http.StatusBadRequest, "Invalid request body.")
		return
	}
	before, _ := cashtransfer.Get(r.Context(), pool, id)
	empID := resolveEmployeeID(r, identityID)
	after, err := cashtransfer.Update(r.Context(), pool, id, in, empID)
	if err != nil {
		ctFail(w, err, "Failed to update cash transfer.")
		return
	}
	auditCT(r, pool, empID, "update", id, before, after)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "cashTransfer": after})
}

func (h *CashTransferOps) Delete(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, identityID, _, ok := h.authCashTransferByUUID(w, r, id, authz.ActionDelete)
	if !ok {
		return
	}
	before, _ := cashtransfer.Get(r.Context(), pool, id)
	empID := resolveEmployeeID(r, identityID)
	if err := cashtransfer.SoftDelete(r.Context(), pool, id, empID); err != nil {
		ctFail(w, err, "Failed to delete cash transfer.")
		return
	}
	auditCT(r, pool, empID, "delete", id, before, nil)
	writeJSON(w, http.StatusOK, models.APIResponse{Success: true, Message: "Cash transfer deleted."})
}

func (h *CashTransferOps) List(w http.ResponseWriter, r *http.Request) {
	pool, identityID, scope, ok := h.authCashTransfer(w, r, authz.ActionRead)
	if !ok {
		return
	}
	req := query.Request{Cursor: r.URL.Query().Get("cursor")}
	if l := r.URL.Query().Get("limit"); l != "" {
		if n, err := strconv.Atoi(l); err == nil {
			req.Limit = n
		}
	}
	page, err := cashtransfer.Search(r.Context(), pool, string(scope), identityID, req)
	if err != nil {
		ctFail(w, err, "Failed to list cash transfers.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "scope": scope, "records": page.Records,
		"nextCursor": page.NextCursor, "hasMore": page.HasMore,
	})
}

func (h *CashTransferOps) Search(w http.ResponseWriter, r *http.Request) {
	pool, identityID, scope, ok := h.authCashTransfer(w, r, authz.ActionRead)
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
	page, err := cashtransfer.Search(r.Context(), pool, string(scope), identityID, req)
	if err != nil {
		ctFail(w, err, "Failed to search cash transfers.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"success": true, "scope": scope, "records": page.Records,
		"nextCursor": page.NextCursor, "hasMore": page.HasMore,
	})
}
```

- [ ] **Step 2:** `go build ./controllers/...` → will fail until Task 6.1 adds `authz.ResourceCashTransfer` — expected; continue.
- [ ] **Step 3:** Commit — `git commit -m "feat(cashtransfer): add controller CRUD handlers"`.

### Task 5.2: Transition/Post/Reverse handlers
- [ ] **Step 1:** Read `controllers/itemreceipt_actions.go` (the reference for dedicated Post + generic Transition living side by side). Implement `controllers/cashtransfer_actions.go`:

```go
// controllers/cashtransfer_actions.go — Approve/Cancel (generic transition),
// Post, and Reverse. Split from cashtransfer.go for the 300-line file cap.
package controllers

import (
	"encoding/json"
	"net/http"

	"stonesuite-backend/authz"
	"stonesuite-backend/cashtransfer"
)

// Transition POST /api/tenant/finance/cash-transfers/{uuid}/transition  body {"toStatusCode":"..."}
// Used for Approve (DRFT->APPR) and Cancel (->CANC). Post and Reverse are
// their own dedicated endpoints below (spec AD-6).
func (h *CashTransferOps) Transition(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, _, ok := h.authCashTransferByUUID(w, r, uuid, authz.ActionTransition)
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
	ct, err := cashtransfer.Transition(r.Context(), pool, uuid, req.ToStatusCode, resolveEmployeeID(r, identityID))
	if err != nil {
		ctFail(w, err, "Failed to apply transition.")
		return
	}
	auditCT(r, pool, resolveEmployeeID(r, identityID), "transition", uuid, nil, ct)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "cashTransfer": ct})
}

// Post POST /api/tenant/finance/cash-transfers/{uuid}/post
func (h *CashTransferOps) Post(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, _, ok := h.authCashTransferByUUID(w, r, uuid, authz.ActionTransition)
	if !ok {
		return
	}
	empID := resolveEmployeeID(r, identityID)
	ct, err := cashtransfer.Post(r.Context(), pool, uuid, empID)
	if err != nil {
		ctFail(w, err, "Failed to post cash transfer.")
		return
	}
	auditCT(r, pool, empID, "post", uuid, nil, ct)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "cashTransfer": ct})
}

// Reverse POST /api/tenant/finance/cash-transfers/{uuid}/reverse
func (h *CashTransferOps) Reverse(w http.ResponseWriter, r *http.Request) {
	uuid := r.PathValue("uuid")
	pool, identityID, _, ok := h.authCashTransferByUUID(w, r, uuid, authz.ActionTransition)
	if !ok {
		return
	}
	empID := resolveEmployeeID(r, identityID)
	ct, err := cashtransfer.Reverse(r.Context(), pool, uuid, empID)
	if err != nil {
		ctFail(w, err, "Failed to reverse cash transfer.")
		return
	}
	auditCT(r, pool, empID, "reverse", uuid, nil, ct)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "cashTransfer": ct})
}
```

- [ ] **Step 2:** `go build ./controllers/...` → still pending Task 6.1/6.3; expected.
- [ ] **Step 3:** Commit — `git commit -m "feat(cashtransfer): add Transition, Post, and Reverse handlers"`.

### Task 5.3: Audit handler
- [ ] **Step 1:** Read `controllers/payment_audit.go` (the reference shape). Implement `controllers/cashtransfer_audit.go`:

```go
package controllers

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"stonesuite-backend/authz"
	"stonesuite-backend/cashtransfer"
	"stonesuite-backend/workflow"
)

// ctSnapshot flattens a CashTransfer into the map recorded in audit_logs.
func ctSnapshot(ct *cashtransfer.CashTransfer) map[string]any {
	if ct == nil {
		return nil
	}
	return map[string]any{
		"id":             ct.ID,
		"number":         ct.Number,
		"statusCode":     ct.StatusCode,
		"fromAccountId":  ct.FromAccount.ID,
		"toAccountId":    ct.ToAccount.ID,
		"amount":         ct.Amount,
		"ownerUserId":    ct.OwnerUserID,
		"customFields":   ct.CustomFields,
	}
}

// auditCT records a create/update/delete/transition/post/reverse event for a
// cash transfer.
func auditCT(r *http.Request, pool *pgxpool.Pool, actorEmployeeID int, action, ctID string, oldCT, newCT *cashtransfer.CashTransfer) {
	ctx := r.Context()
	if err := workflow.LogAuditFull(ctx, pool, "", action, string(authz.ResourceCashTransfer), ctID, "cash_transfer",
		ctSnapshot(oldCT), ctSnapshot(newCT), map[string]any{"employee_id": actorEmployeeID},
		clientIP(r), r.Header.Get("X-Session-Id"), appVersion); err != nil {
		log.Printf("cashtransfer: audit %s %s: %v", action, ctID, err)
	}
}

// ctAuditEntry is a single row of a cash transfer's audit trail.
type ctAuditEntry struct {
	Action     string         `json:"action"`
	ActorName  string         `json:"actorName"`
	IPAddress  string         `json:"ipAddress"`
	AppVersion string         `json:"appVersion"`
	OldValue   map[string]any `json:"oldValue,omitempty"`
	NewValue   map[string]any `json:"newValue,omitempty"`
	At         time.Time      `json:"at"`
}

// Audit GET /api/tenant/finance/cash-transfers/{uuid}/audit
func (h *CashTransferOps) Audit(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("uuid")
	pool, _, _, ok := h.authCashTransferByUUID(w, r, id, authz.ActionRead)
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
		LIMIT 200`, id, string(authz.ResourceCashTransfer))
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load audit trail.")
		return
	}
	defer rows.Close()
	entries := []ctAuditEntry{}
	for rows.Next() {
		var (
			e              ctAuditEntry
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

- [ ] **Step 2:** `go build ./controllers/...` → still pending Phase 6; expected.
- [ ] **Step 3:** Commit — `git commit -m "feat(cashtransfer): add Audit handler"`.

---

## Phase 6 — Wiring (additive, isolated edits)

### Task 6.1: RBAC catalog
- [ ] **Step 1:** Open `authz/catalog.go`. In the grouped `Resource` const block (near `ResourceChartOfAccount`), add:

```go
	ResourceCashTransfer  Resource = "cash_transfer"
```

- [ ] **Step 2:** In `var catalog = []Permission{...}`, near the other document-module rows, add exactly 5 rows (no `ActionApprove` — spec AD-5):

```go
	{ResourceCashTransfer, ActionCreate},
	{ResourceCashTransfer, ActionRead},
	{ResourceCashTransfer, ActionUpdate},
	{ResourceCashTransfer, ActionDelete},
	{ResourceCashTransfer, ActionTransition},
```

- [ ] **Step 3:** `go build ./...` → `authz` and everything depending on it should now compile; `controllers` (Phase 5) should now build too since `authz.ResourceCashTransfer` exists. Run `go build ./... && go vet ./...` and fix anything unexpected before proceeding.
- [ ] **Step 4:** Commit — `git commit -m "feat(cashtransfer): register RBAC resource and permissions"`.

### Task 6.2: Generic router resource mapping
- [ ] **Step 1:** Open `controllers/crm.go`, find `resourceForKey` (the `switch key { case "lead": ... }` block). Add:

```go
	case "cash_transfer":
		return authz.ResourceCashTransfer
```

- [ ] **Step 2:** `go build ./controllers/...` → succeeds.
- [ ] **Step 3:** Commit — `git commit -m "feat(cashtransfer): map cash_transfer in the generic resourceForKey router"`.

### Task 6.3: Attachments IDOR resolver
- [ ] **Step 1:** Open `workflow/attachments.go`. In `ResolveRecordAccess`, immediately after the existing `sales_order` branch (the final `return RecordAccessInfo{}, ErrRecordNotFound` at the end of the function), insert a fourth branch that mirrors the `sales_order` one exactly, querying `cash_transfer` by `cash_transfer_uuid`:

```go
	// cash_transfer: dedicated relational module (Cash Transfer spec), owner
	// resolved the same way (employee -> users.id); no team column.
	var ctOwnerUserID string
	err = q.QueryRow(ctx, `
		SELECT COALESCE(u.id::text,'')
		FROM cash_transfer ct
		LEFT JOIN employee e ON e.employee_id = ct.cash_transfer_owner_id
		LEFT JOIN users u ON u.id = e.employee_user_id
		WHERE ct.cash_transfer_uuid = $1::uuid AND ct.cash_transfer_deleted_at IS NULL`,
		recordID).Scan(&ctOwnerUserID)
	if err == nil {
		return RecordAccessInfo{WorkflowKey: "cash_transfer", OwnerUserID: ctOwnerUserID}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return RecordAccessInfo{}, fmt.Errorf("lookup cash transfer: %w", err)
	}

	return RecordAccessInfo{}, ErrRecordNotFound
```

The final `return RecordAccessInfo{}, ErrRecordNotFound` in this snippet **replaces** the existing final line of the function — do not leave two copies. Everything above it is new, inserted right after the existing `sales_order` block and before that final return.

- [ ] **Step 2:** `go build ./workflow/...` → succeeds (all imports — `errors`, `pgx`, `fmt` — are already present in this file from the existing branches).
- [ ] **Step 3:** Commit — `git commit -m "feat(cashtransfer): extend attachments IDOR resolver for cash_transfer"`.

### Task 6.4: Route wiring
- [ ] **Step 1:** Open `main.go`. Immediately after the Chart of Accounts route block (the block ending at `mux.Handle("GET /api/tenant/finance/accounts/{uuid}/history", ...)`), insert:

```go
			// Cash Transfer: second module of the Finance section — moves funds
			// between two Cash/Bank GL accounts. /post creates a balanced journal
			// entry (debit destination, credit source) and updates both accounts'
			// running balances; /reverse creates the reversing entry. Neither can
			// run twice (spec docs/superpowers/specs/2026-07-28-cash-transfer-design.md).
			ctOps := controllers.NewCashTransferOps()
			mux.Handle("GET /api/tenant/finance/cash-transfers", tenantChain(ctOps.List))
			mux.Handle("POST /api/tenant/finance/cash-transfers/search", tenantChain(ctOps.Search))
			mux.Handle("POST /api/tenant/finance/cash-transfers", tenantChain(ctOps.Create))
			mux.Handle("GET /api/tenant/finance/cash-transfers/{uuid}", tenantChain(ctOps.Get))
			mux.Handle("PATCH /api/tenant/finance/cash-transfers/{uuid}", tenantChain(ctOps.Update))
			mux.Handle("DELETE /api/tenant/finance/cash-transfers/{uuid}", tenantChain(ctOps.Delete))
			mux.Handle("POST /api/tenant/finance/cash-transfers/{uuid}/transition", tenantChain(ctOps.Transition))
			mux.Handle("POST /api/tenant/finance/cash-transfers/{uuid}/post", tenantChain(ctOps.Post))
			mux.Handle("POST /api/tenant/finance/cash-transfers/{uuid}/reverse", tenantChain(ctOps.Reverse))
			mux.Handle("GET /api/tenant/finance/cash-transfers/{uuid}/audit", tenantChain(ctOps.Audit))
```

Match the exact indentation of the surrounding block (tabs, nested inside the same `if`/block Chart of Accounts' routes are registered in — Chart of Accounts' own registration is guarded by a `cipher` availability check; place the Cash Transfer block **after** that guarded section closes, at the same indentation level as `irOps`/`invOps`/etc., NOT inside the CoA-specific guard, since Cash Transfer has no cipher dependency).

- [ ] **Step 2:** `go build ./... && go vet ./...` → the whole program compiles.
- [ ] **Step 3:** `go test ./...` → all existing tests plus every pure test from Phases 2–3 pass.
- [ ] **Step 4:** Commit — `git commit -m "feat(cashtransfer): wire routes under /api/tenant/finance/cash-transfers"`.

---

## Phase 7 — Database-backed integration tests

### Task 7.1: `journal/store_dbtest_test.go`
- [ ] **Step 1:** `journal/store_test.go` already exists (Task 2.4 — pure, untagged `TestValidateLines`). Do **not** reuse that filename: put the dbtest suite in `journal/store_dbtest_test.go` instead, following the `_dbtest_test.go` naming this repo already uses to keep pure and dbtest-tagged tests apart when they'd otherwise share a base name (see `chartofaccounts/store_dbtest_test.go`, `store_dbtest_guard_test.go`, `store_dbtest_misc_test.go`). Implement `journal/store_dbtest_test.go` (mirrors `itemreceipt/store_test.go`'s `//go:build dbtest` + skip-guard shape):

```go
//go:build dbtest

package journal

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping dbtest")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// seedTwoAccounts creates two live, postable, active bank/cash coa_account
// rows for the test to post between, returning their internal ids.
func seedTwoAccounts(t *testing.T, pool *pgxpool.Pool) (fromID, toID int) {
	t.Helper()
	ctx := context.Background()
	// Reuses the seeded reference tree (chartofaccounts spec §3.1/3.2): every
	// tenant already has subcategory 1100 (Current Assets) seeded.
	var subcatID int
	if err := pool.QueryRow(ctx,
		`SELECT subcategory_id FROM lkp_coa_subcategory WHERE subcategory_code = 1100`,
	).Scan(&subcatID); err != nil {
		t.Fatalf("load Current Assets subcategory: %v", err)
	}
	insert := func(code, name string) int {
		var id int
		if err := pool.QueryRow(ctx, `
			INSERT INTO coa_account (coa_account_code, coa_account_name, subcategory_id,
				coa_account_bs_pnl, coa_account_type, coa_account_is_postable, coa_account_is_active)
			VALUES ($1,$2,$3,'BS','bank',TRUE,TRUE)
			RETURNING coa_account_id`, code, name, subcatID).Scan(&id); err != nil {
			t.Fatalf("seed account %s: %v", code, err)
		}
		return id
	}
	return insert("9990", "Test Bank From"), insert("9991", "Test Bank To")
}

func TestCreateEntry_BalancedAndUpdatesBalances(t *testing.T) {
	pool := testPool(t)
	fromID, toID := seedTwoAccounts(t, pool)
	ctx := context.Background()

	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)

	entry, err := CreateEntry(ctx, tx, CreateEntryInput{
		EntryDate:  time.Now(),
		Memo:       "test transfer",
		SourceType: "cash_transfer",
		SourceID:   "00000000-0000-0000-0000-000000000001",
		Lines: []LineInput{
			{AccountID: toID, Debit: 500},
			{AccountID: fromID, Credit: 500},
		},
	})
	if err != nil {
		t.Fatalf("CreateEntry: %v", err)
	}
	if entry.Number == "" {
		t.Error("expected a journal entry number to be assigned")
	}

	var fromBalance, toBalance float64
	if err := tx.QueryRow(ctx, `SELECT coa_account_balance FROM coa_account WHERE coa_account_id = $1`, fromID).Scan(&fromBalance); err != nil {
		t.Fatalf("read from balance: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT coa_account_balance FROM coa_account WHERE coa_account_id = $1`, toID).Scan(&toBalance); err != nil {
		t.Fatalf("read to balance: %v", err)
	}
	if fromBalance != -500 {
		t.Errorf("from account balance = %v, want -500", fromBalance)
	}
	if toBalance != 500 {
		t.Errorf("to account balance = %v, want 500", toBalance)
	}

	// Balance invariant: coa_account_balance == SUM(debit - credit) over
	// every journal_entry_line for that account.
	var summed float64
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(debit - credit), 0) FROM journal_entry_line WHERE coa_account_id = $1`,
		toID).Scan(&summed); err != nil {
		t.Fatalf("sum lines: %v", err)
	}
	if summed != toBalance {
		t.Errorf("balance invariant broken: coa_account_balance=%v, SUM(lines)=%v", toBalance, summed)
	}
}

func TestCreateEntry_RejectsUnbalanced(t *testing.T) {
	pool := testPool(t)
	fromID, toID := seedTwoAccounts(t, pool)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)

	_, err = CreateEntry(ctx, tx, CreateEntryInput{
		EntryDate:  time.Now(),
		SourceType: "cash_transfer",
		SourceID:   "00000000-0000-0000-0000-000000000002",
		Lines: []LineInput{
			{AccountID: toID, Debit: 500},
			{AccountID: fromID, Credit: 499},
		},
	})
	if err != ErrUnbalancedEntry {
		t.Errorf("CreateEntry() error = %v, want ErrUnbalancedEntry", err)
	}
}

func TestReverseEntry_NetsBalanceToZero(t *testing.T) {
	pool := testPool(t)
	fromID, toID := seedTwoAccounts(t, pool)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer tx.Rollback(ctx)

	entry, err := CreateEntry(ctx, tx, CreateEntryInput{
		EntryDate:  time.Now(),
		SourceType: "cash_transfer",
		SourceID:   "00000000-0000-0000-0000-000000000003",
		Lines: []LineInput{
			{AccountID: toID, Debit: 250},
			{AccountID: fromID, Credit: 250},
		},
	})
	if err != nil {
		t.Fatalf("CreateEntry: %v", err)
	}

	if _, err := ReverseEntry(ctx, tx, entry.InternalID, time.Now(), "reversal", 0); err != nil {
		t.Fatalf("ReverseEntry: %v", err)
	}

	var fromBalance, toBalance float64
	tx.QueryRow(ctx, `SELECT coa_account_balance FROM coa_account WHERE coa_account_id = $1`, fromID).Scan(&fromBalance)
	tx.QueryRow(ctx, `SELECT coa_account_balance FROM coa_account WHERE coa_account_id = $1`, toID).Scan(&toBalance)
	if fromBalance != 0 {
		t.Errorf("from account balance after reversal = %v, want 0", fromBalance)
	}
	if toBalance != 0 {
		t.Errorf("to account balance after reversal = %v, want 0", toBalance)
	}
}
```

- [ ] **Step 2:** If `TEST_DATABASE_URL` is set, run `go test -tags dbtest ./journal/... -v` → all PASS. Otherwise confirm `go test ./journal/...` (no tag) still passes (these tests are excluded, per the build tag) and note in the commit message that dbtest was not run locally.
- [ ] **Step 3:** Commit — `git commit -m "test(journal): add dbtest coverage for CreateEntry/ReverseEntry"`.

### Task 7.2: `cashtransfer/store_test.go`
- [ ] **Step 1:** Implement `cashtransfer/store_test.go`:

```go
//go:build dbtest

package cashtransfer

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func testPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set; skipping dbtest")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatalf("connect test database: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

// seedAccount creates one live bank-type coa_account row and returns its uuid.
func seedAccount(t *testing.T, pool *pgxpool.Pool, code, name string, postable, active bool) string {
	t.Helper()
	ctx := context.Background()
	var subcatID int
	if err := pool.QueryRow(ctx,
		`SELECT subcategory_id FROM lkp_coa_subcategory WHERE subcategory_code = 1100`,
	).Scan(&subcatID); err != nil {
		t.Fatalf("load Current Assets subcategory: %v", err)
	}
	var uuid string
	if err := pool.QueryRow(ctx, `
		INSERT INTO coa_account (coa_account_code, coa_account_name, subcategory_id,
			coa_account_bs_pnl, coa_account_type, coa_account_is_postable, coa_account_is_active)
		VALUES ($1,$2,$3,'BS','bank',$4,$5)
		RETURNING coa_account_uuid`, code, name, subcatID, postable, active).Scan(&uuid); err != nil {
		t.Fatalf("seed account %s: %v", code, err)
	}
	return uuid
}

func TestCreate_Validations(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	from := seedAccount(t, pool, "9980", "From", true, true)
	to := seedAccount(t, pool, "9981", "To", true, true)
	inactive := seedAccount(t, pool, "9982", "Inactive", true, false)

	tests := []struct {
		name string
		in   CreateInput
	}{
		{"same account", CreateInput{FromAccountUUID: from, ToAccountUUID: from, Amount: 100}},
		{"zero amount", CreateInput{FromAccountUUID: from, ToAccountUUID: to, Amount: 0}},
		{"negative amount", CreateInput{FromAccountUUID: from, ToAccountUUID: to, Amount: -5}},
		{"inactive destination", CreateInput{FromAccountUUID: from, ToAccountUUID: inactive, Amount: 100}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := Create(ctx, pool, tt.in, 0); err == nil {
				t.Error("expected an error, got nil")
			}
		})
	}
}

func TestPost_CreatesBalancedEntryAndUpdatesBalances(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	from := seedAccount(t, pool, "9970", "PostFrom", true, true)
	to := seedAccount(t, pool, "9971", "PostTo", true, true)

	ct, err := Create(ctx, pool, CreateInput{FromAccountUUID: from, ToAccountUUID: to, Amount: 1000}, 0)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := Transition(ctx, pool, ct.ID, approvedStatusCode, 0); err != nil {
		t.Fatalf("Transition to APPR: %v", err)
	}
	posted, err := Post(ctx, pool, ct.ID, 0)
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	if posted.StatusCode != postedStatusCode {
		t.Errorf("status = %s, want %s", posted.StatusCode, postedStatusCode)
	}
	if posted.JournalEntryID == nil {
		t.Fatal("expected journalEntryId to be set after posting")
	}

	var fromBalance, toBalance float64
	pool.QueryRow(ctx, `SELECT coa_account_balance FROM coa_account WHERE coa_account_uuid = $1`, from).Scan(&fromBalance)
	pool.QueryRow(ctx, `SELECT coa_account_balance FROM coa_account WHERE coa_account_uuid = $1`, to).Scan(&toBalance)
	if fromBalance != -1000 || toBalance != 1000 {
		t.Errorf("balances after post: from=%v to=%v, want from=-1000 to=1000", fromBalance, toBalance)
	}

	// Re-posting must fail.
	if _, err := Post(ctx, pool, ct.ID, 0); !errors.Is(err, ErrAlreadyPosted) {
		t.Errorf("second Post() error = %v, want ErrAlreadyPosted", err)
	}

	// Editing a posted transfer must fail.
	if _, err := Update(ctx, pool, ct.ID, UpdateInput{FromAccountUUID: from, ToAccountUUID: to, Amount: 1}, 0); err == nil {
		t.Error("expected Update on a posted transfer to fail")
	}
}

func TestPost_ConcurrentDoublePost(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	from := seedAccount(t, pool, "9960", "RaceFrom", true, true)
	to := seedAccount(t, pool, "9961", "RaceTo", true, true)

	ct, err := Create(ctx, pool, CreateInput{FromAccountUUID: from, ToAccountUUID: to, Amount: 250}, 0)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := Transition(ctx, pool, ct.ID, approvedStatusCode, 0); err != nil {
		t.Fatalf("Transition to APPR: %v", err)
	}

	var wg sync.WaitGroup
	results := make([]error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, results[i] = Post(ctx, pool, ct.ID, 0)
		}(i)
	}
	wg.Wait()

	successes := 0
	for _, err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 {
		t.Errorf("expected exactly 1 successful Post out of 2 concurrent attempts, got %d", successes)
	}
}

func TestReverse_OnlyValidWhenPosted(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	from := seedAccount(t, pool, "9950", "RevFrom", true, true)
	to := seedAccount(t, pool, "9951", "RevTo", true, true)

	ct, err := Create(ctx, pool, CreateInput{FromAccountUUID: from, ToAccountUUID: to, Amount: 300}, 0)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	if _, err := Reverse(ctx, pool, ct.ID, 0); !errors.Is(err, ErrNotPosted) {
		t.Errorf("Reverse on a draft = %v, want ErrNotPosted", err)
	}

	if _, err := Transition(ctx, pool, ct.ID, approvedStatusCode, 0); err != nil {
		t.Fatalf("Transition to APPR: %v", err)
	}
	if _, err := Post(ctx, pool, ct.ID, 0); err != nil {
		t.Fatalf("Post: %v", err)
	}
	reversed, err := Reverse(ctx, pool, ct.ID, 0)
	if err != nil {
		t.Fatalf("Reverse: %v", err)
	}
	if reversed.StatusCode != reversedStatusCode {
		t.Errorf("status = %s, want %s", reversed.StatusCode, reversedStatusCode)
	}
	if reversed.ReversalJournalEntryID == nil {
		t.Fatal("expected reversalJournalEntryId to be set")
	}

	var fromBalance, toBalance float64
	pool.QueryRow(ctx, `SELECT coa_account_balance FROM coa_account WHERE coa_account_uuid = $1`, from).Scan(&fromBalance)
	pool.QueryRow(ctx, `SELECT coa_account_balance FROM coa_account WHERE coa_account_uuid = $1`, to).Scan(&toBalance)
	if fromBalance != 0 || toBalance != 0 {
		t.Errorf("balances after reverse: from=%v to=%v, want 0 and 0", fromBalance, toBalance)
	}

	// Reversing twice must fail (status is now RVSD, not POST).
	if _, err := Reverse(ctx, pool, ct.ID, 0); !errors.Is(err, ErrNotPosted) {
		t.Errorf("second Reverse() error = %v, want ErrNotPosted", err)
	}
}

func TestPost_RejectsWhenPeriodClosed(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	from := seedAccount(t, pool, "9940", "ClosedFrom", true, true)
	to := seedAccount(t, pool, "9941", "ClosedTo", true, true)

	// Close the books through tomorrow, so today's transfer date is closed.
	closedThrough := time.Now().Add(24 * time.Hour)
	if _, err := pool.Exec(ctx,
		`UPDATE accounting_settings SET books_closed_through = $1 WHERE accounting_settings_id = 1`,
		closedThrough); err != nil {
		t.Fatalf("set books_closed_through: %v", err)
	}
	t.Cleanup(func() {
		pool.Exec(ctx, `UPDATE accounting_settings SET books_closed_through = NULL WHERE accounting_settings_id = 1`)
	})

	ct, err := Create(ctx, pool, CreateInput{FromAccountUUID: from, ToAccountUUID: to, Amount: 100}, 0)
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := Transition(ctx, pool, ct.ID, approvedStatusCode, 0); err != nil {
		t.Fatalf("Transition to APPR: %v", err)
	}
	if _, err := Post(ctx, pool, ct.ID, 0); !errors.Is(err, ErrPeriodClosed) {
		t.Errorf("Post() error = %v, want ErrPeriodClosed", err)
	}
}
```

- [ ] **Step 2:** If `TEST_DATABASE_URL` is set, run `go test -tags dbtest ./cashtransfer/... -v` → all PASS. Otherwise confirm `go test ./cashtransfer/...` (no tag) passes and note dbtest wasn't run locally.
- [ ] **Step 3:** Commit — `git commit -m "test(cashtransfer): add dbtest coverage for validations, Post, Reverse, and closed-period rejection"`.

---

## Phase 8 — Final verification

### Task 8.1: Full build/vet/test + idempotency + review agents
- [ ] **Step 1:** `go build ./... && go vet ./... && go test ./...` → all green.
- [ ] **Step 2:** If a local database is available, apply `database/migrations/tenant/schema.sql` twice (fresh database) to re-confirm idempotency now that it includes every table from Phase 1, then run `TEST_DATABASE_URL=... go test -tags dbtest ./journal/... ./cashtransfer/... -v` end to end.
- [ ] **Step 3:** Dispatch, in one parallel batch: `migration-auditor` (schema diff), `module-drift-checker` (the new `cashtransfer` module + its controllers + `authz/catalog.go` + `main.go` changes), `tenancy-security-reviewer` (all touched controllers/store files), `filter-invariant-checker` (only if `cashtransfer/resolver.go` or `query/` itself changed — it did not, so this one can be skipped per the feature pipeline's conditional fan-out rule).
- [ ] **Step 4:** Triage findings per the feature pipeline's Phase 5 rules (CRITICAL/HIGH auto-fix within a capped 2 rounds, MEDIUM report-only, discard anything contradicting this approved plan).
- [ ] **Step 5:** Final commit if any fixes were applied — otherwise this phase produces no new commit, only a report.

---

## Self-Review Notes (from writing this plan)

- **Spec coverage:** Create/View/Update(Draft)/Approve/Post/Cancel/Reverse/History all have a task (4.2–4.7, 5.1–5.3). Attachments is Task 6.3 (no new module code — reuses the generic endpoint). Notes are plain fields on `CreateInput`/`UpdateInput`/`CashTransfer` (Task 3.4) — no separate task needed. All 6 validations from spec §6 map to code in Tasks 4.2/4.3/4.6/4.7. Balanced-JE-on-Post and reversing-JE-on-Reverse are Tasks 4.6/4.7. Every schema object in spec §3 is in Task 1.1.
- **Type consistency check performed:** `JournalEntry.InternalID`/`UUID` (Task 2.3) is what `cashtransfer/store_post.go` (Task 4.6) reads as `entry.InternalID` and what `store_reverse.go` (Task 4.7) passes to `journal.ReverseEntry` as `*journalEntryID` (an `int`, matching `JournalEntry.InternalID`'s type) — consistent. `CashTransfer.JournalEntryID`/`ReversalJournalEntryID` are `*string` (the UUID, for display, scanned from `je.journal_entry_uuid`/`rje.journal_entry_uuid` in Task 4.1's `headerSelect`) — deliberately a different type than the internal `int` FK column, since the API should never leak internal serial ids; this is consistent throughout every task that touches it.
- **Placeholder scan:** none found — every step has real code or an explicit "no isolated unit test, dbtest-covered" note explaining why, never a bare "add tests" instruction.
