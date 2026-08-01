# Accounting Period Management — Locks, Quarters, Range Generation — Plan

Spec: `docs/superpowers/specs/2026-07-31-accounting-period-locks-and-ranges-design.md`
Branch: `feat/accounting-period-management`

All tasks are **sequential** — each depends on schema/types/rules changes
earlier in the list. Do not parallelize.

## Task 1 — Schema

`database/migrations/tenant/schema.sql`, appended at EOF (after the existing
Accounting Period Management block, line ~6694):

- [ ] `fiscal_quarter` table: `fiscal_quarter_id` serial PK,
      `fiscal_quarter_uuid` unique, `fiscal_year_id` FK, `quarter_number`
      (1-4), `quarter_name`, `quarter_start`, `quarter_end`,
      `fiscal_quarter_status` (open/closed, default open), created/updated
      at/by. `UNIQUE(fiscal_year_id, quarter_number)`, `UNIQUE(quarter_start)`,
      `CHECK(quarter_end > quarter_start)`, `CHECK(quarter_number BETWEEN 1
      AND 4)`, `CHECK(fiscal_quarter_status IN ('open','closed'))`.
- [ ] `ALTER TABLE accounting_period ADD COLUMN IF NOT EXISTS
      fiscal_quarter_id INTEGER NULL REFERENCES fiscal_quarter(fiscal_quarter_id)`
      (must come after the `fiscal_quarter` table is created).
- [ ] `ALTER TABLE accounting_period ADD COLUMN IF NOT EXISTS ap_lock_status
      VARCHAR(10) NOT NULL DEFAULT 'open'` — same for `ar_lock_status`,
      `gl_lock_status`.
- [ ] Guarded `DO $$` CHECK constraints (`pg_constraint` existence test) for
      the three lock columns (`IN ('open','closed')`) and for
      `fiscal_quarter`'s `chk_fq_*` constraints not already inline.
- [ ] Widen `chk_ap_history_action`: it is inline in
      `accounting_period_history`'s `CREATE TABLE IF NOT EXISTS` body, which
      is a no-op on every already-provisioned tenant, so do **not** edit that
      inline definition. Instead append: `DROP CONSTRAINT IF EXISTS
      chk_ap_history_action` then an unconditional `ADD CONSTRAINT
      chk_ap_history_action CHECK (history_action IN
      ('generate','close','reopen','base_setup','ap_lock','ap_unlock',
      'ar_lock','ar_unlock','gl_lock','gl_unlock'))` — safe to re-run every
      boot, always converges to the same definition.
- [ ] Backfill (idempotent by construction — see spec §6):
      `UPDATE accounting_period SET ap_lock_status='closed',
      ar_lock_status='closed', gl_lock_status='closed' WHERE
      accounting_period_status='closed' AND ap_lock_status='open' AND
      ar_lock_status='open' AND gl_lock_status='open'`.
- [ ] `CREATE INDEX IF NOT EXISTS idx_ap_fiscal_quarter ON accounting_period
      (fiscal_quarter_id)`.
- [ ] Run the `migration-auditor` agent on the diff before moving on.

## Task 2 — Pure helpers (no DB)

- [ ] `accountingperiod/calendar.go`: add `FiscalYearEndMonth(startMonth
      int) int` (startMonth-1, wrapping Jan→Dec). Add `QuarterSpan` struct
      (`Number`, `Name`, `Start`, `End`) and `QuartersFor(months []MonthSpan,
      fyLabel string) []QuarterSpan` — groups every 3 consecutive `MonthSpan`
      into one quarter, naming e.g. `"Q1 FY2026"`.
- [ ] `accountingperiod/calendar_test.go`: table-driven cases for
      `FiscalYearEndMonth` (1→12, 7→6, 12→11) and `QuartersFor` (calendar
      year and a July-start year that straddles the calendar boundary;
      assert the 4 spans are contiguous and each covers exactly 3 months).

## Task 3 — Rules generalization (no DB)

`accountingperiod/rules.go`:

- [ ] Add `LockField` type (`LockAP`, `LockAR`, `LockGL` — put the type
      itself and its two helper methods, `get(PeriodState) string` and
      `label() string`, here since rules.go is the only pure-logic consumer;
      the SQL column name lives in the store layer, not here).
- [ ] Add `APStatus`, `ARStatus`, `GLStatus string` fields to `PeriodState`.
- [ ] Generalize the internal `plan()` engine to take a `get func(PeriodState)
      string` accessor and a `label string` (empty for the existing
      whole-period case) instead of hardcoding `p.Status`. Thread `label`
      into `checkClose`/`checkReopen` only for message text — prefix
      `"<label> for "` before the period name when `label != ""`, nothing
      when `label == ""`. Sentinel errors (`ErrAlreadyClosed`,
      `ErrPriorPeriodOpen`, etc.) are unchanged; only the message text grows
      a prefix.
- [ ] `PlanClose`/`PlanReopen` keep their existing signatures and now call
      `plan(ids, all, func(p PeriodState) string { return p.Status }, "",
      closing)` — behaviour and every existing test must be byte-for-byte
      unchanged.
- [ ] Add `PlanCloseLock(ids []string, all []PeriodState, lock LockField)
      ([]PeriodState, error)` and `PlanReopenLock` — same shape, call `plan`
      with `lock.get` and `lock.label()`.
- [ ] `accountingperiod/rules_test.go`: do not modify existing cases. Add a
      new table (mirroring `TestPlanClose`/`TestPlanReopen`'s shape) driving
      `PlanCloseLock`/`PlanReopenLock` over `PeriodState.APStatus` (or AR/GL),
      covering: chronological enforcement holds per-lock independently of the
      other two locks/overall `Status`, and the error message names the lock
      (e.g. contains `"AP for"`).

## Task 4 — Store layer

- [ ] `accountingperiod/types.go`: add `Quarter` struct (`ID`,
      `FiscalYearID`, `Number`, `Name`, `Start`, `End`, `Status`,
      `CreatedAt`, `UpdatedAt`); add `Quarters []Quarter
      `json:"quarters,omitempty"`` to `FiscalYear`; add `APLockStatus`,
      `ARLockStatus`, `GLLockStatus string` and `QuarterID`, `QuarterName
      string `json:"...,omitempty"`` to `Period`; add `EndYear int` to
      `GenerateInput`; add `GenerateResult` struct (spec §5).
- [ ] `accountingperiod/store_get.go`: extend `periodSelect` with a `LEFT
      JOIN fiscal_quarter fq ON fq.fiscal_quarter_id = ap.fiscal_quarter_id`
      and select `ap.ap_lock_status, ap.ar_lock_status, ap.gl_lock_status,
      fq.fiscal_quarter_uuid, fq.quarter_name`; `scanPeriod` scans the three
      lock columns plus nullable quarter uuid/name into `Period`. Extend
      `loadStates`' SQL and scan to also read `ap_lock_status,
      ar_lock_status, gl_lock_status` into the new `PeriodState` fields.
- [ ] `accountingperiod/store_generate.go`:
  - `generateYear`: before the period-insert loop, compute `QuartersFor` for
    the year's 12 `MonthSpan`s, insert 4 `fiscal_quarter` rows (status: same
    open/closed rule the periods already use — a quarter whose 3 periods are
    all pre-base-period is closed, else open — reuse `DeriveYearStatus`-style
    logic or inline the equivalent AND), collect their ids; the period insert
    gains `fiscal_quarter_id, ap_lock_status, ar_lock_status,
    gl_lock_status` columns, each lock set to the period's own `status`.
    `fy.Quarters` is populated alongside `fy.Periods`.
  - `GenerateFiscalYear`: signature becomes `(ctx, pool, in GenerateInput,
    employeeID int) (*GenerateResult, error)`. Validate `EndYear` (spec §4
    table) before opening the transaction. Resolve `endYear` per the
    resolution table, cap `count := endYear - start.Year() + 1` at
    `maxGenerateYears = 20` (new const), loop `generateYear` once per year
    advancing `cursor` by the previous year's end, append each to a
    `[]FiscalYear`, then `syncBooksClosedThrough` once and commit. Return
    `&GenerateResult{FiscalYears: years, FiscalYearStartMonth:
    cal.FiscalYearStartMonth, FiscalYearEndMonth:
    FiscalYearEndMonth(cal.FiscalYearStartMonth)}`.
- [ ] `accountingperiod/store_status.go`: `applyStatus`'s UPDATE also sets
      `ap_lock_status = $2, ar_lock_status = $2, gl_lock_status = $2` (same
      target as `accounting_period_status`) in the same statement. No other
      change — `Close`/`Reopen`'s public signatures, `StatusChangeResult`,
      and the single close/reopen history row per period are unchanged.
- [ ] New `accountingperiod/store_locks.go`:
  - `LockField.column() string` (the SQL column name — a fixed 3-value
    switch, never derived from request data) and `.lockAction()` /
    `.unlockAction()` (history verbs).
  - `changeLock(ctx, pool, lock LockField, ids []string, note string,
    employeeID int, closing bool) (*StatusChangeResult, error)` — mirrors
    `changeStatus`: same `ORDER BY period_start FOR UPDATE` lock, same
    `loadStates`, calls `PlanCloseLock`/`PlanReopenLock`, applies via
    `applyLock` (writes exactly the one lock column + one history row per
    period), then `recomputePeriodStatus` for the touched period ids, then
    the existing `syncFiscalYearStatus` + `syncBooksClosedThrough`, then
    `readBack`, then commit.
  - `applyLock` — like `applyStatus` but `SET <lock.column()> = $2` only
    (column name from the fixed enum, not string-built from input) plus the
    updated_at/by pair; returns the period's int id for history + status
    recompute.
  - `recomputePeriodStatus(ctx, q querier, periodIDs []int, employeeID int)
    error` — one UPDATE deriving `accounting_period_status` (and the paired
    `closed_at`/`closed_by`, preserving the existing timestamp with
    `COALESCE` when already closed) from `ap_lock_status = 'closed' AND
    ar_lock_status = 'closed' AND gl_lock_status = 'closed'`, scoped to
    `accounting_period_id = ANY($1)`.
  - `LockAP`, `UnlockAP`, `LockAR`, `UnlockAR`, `LockGL`, `UnlockGL` —
    thin public wrappers over `changeLock`, mirroring how `Close`/`Reopen`
    wrap `changeStatus`.
- [ ] `accountingperiod/sync.go`: extend `syncFiscalYearStatus` (or add a
      sibling `syncQuarterStatus` called alongside it from every call site —
      `store_status.go` and the new `store_locks.go`) to also recompute
      `fiscal_quarter_status` for every quarter owning one of the changed
      periods, same derivation shape (closed iff all 3 of its periods are
      closed, open if it owns none).
- [ ] `accountingperiod/store_dbtest_test.go` / `store_dbtest_status_test.go`
      (split by existing convention — generation/setup vs. status/reads):
  - Range generation: `GenerateInput{StartYear: N, EndYear: N+2}` produces 3
    contiguous fiscal years, each with 4 quarters and 12 periods, in one call;
    assert `GenerateResult.FiscalYearStartMonth`/`FiscalYearEndMonth` match
    the configured calendar.
  - Single-year via `StartYear == EndYear` behaves identically to
    `StartYear` alone (existing `TestGenerateFiscalYear_IsForwardOnlyAndContiguous`
    must keep passing unmodified).
  - `EndYear < StartYear` → `ClientError`; `EndYear` without `StartYear` →
    `ClientError`; a range whose `StartYear` doesn't match the next
    contiguous year → `ClientError` (duplicate/gap prevention); a mid-range
    collision rolls back the whole batch (assert none of the range's years
    exist after the error).
  - Quarters: assert `fiscal_quarter_status` starts open, flips closed only
    once all 3 of its periods close, and flips back on the first reopen —
    mirroring `TestFiscalYearStatusIsDerived`.
  - Locks: `LockAP`/`LockAR`/`LockGL` each independently move their own
    column and leave the other two + the overall `accounting_period_status`
    untouched until all three are closed, at which point
    `accounting_period_status` flips to `closed` with `closed_at` set;
    unlocking any one flips it back to `open` with `closed_at` cleared.
    Assert the chronological rule holds per-lock (locking AP for period 2
    while AP is still open on period 1 is refused, independent of GL/AR
    state on either period). Assert history rows carry the right
    `ap_lock`/`ar_lock`/`gl_lock`/`*_unlock` action.
  - Whole-period `Close`/`Reopen` still writes exactly one history row per
    period (not four) — extend `TestListGetAndHistory`'s assertion or add a
    sibling test confirming this explicitly, since it is the backward-
    compatibility guarantee for the audit trail.

## Task 5 — GL choke point

- [ ] `journal/store.go`: `lookupPeriod`'s query selects `ap.gl_lock_status`
      instead of `ap.accounting_period_status`. Update the doc comment above
      it and above `CheckPeriodOpen` to say so, referencing the new spec.
- [ ] `journal/store_dbtest_test.go`: `seedPeriod` currently only sets
      `accounting_period_status`/`accounting_period_closed_at`; it must also
      set `gl_lock_status` (and, for realism, `ap_lock_status`/
      `ar_lock_status`) to the same `status` value, or `TestCreateEntry_
      RejectsClosedPeriod` will silently pass on the wrong column (new
      columns default `'open'`). Add a new case proving the GL lock alone
      gates `CreateEntry`: seed a period with `accounting_period_status =
      'open'` (derived state, i.e. AP/AR open) but `gl_lock_status =
      'closed'`, and assert `CreateEntry` still returns `ErrPeriodClosed`.
- [ ] `journal/period_test.go`: no change expected (the pure `verdictFor`
      logic is unchanged — only the SQL column feeding `periodLookup.Status`
      moved). Re-run it to confirm.

## Task 6 — Controllers + routes

- [ ] `controllers/accountingperiod_actions.go`: `GenerateYear` — decode
      `EndYear` (already part of `GenerateInput`, no handler-side change
      needed there beyond the decode target type), call the new
      `GenerateFiscalYear` returning `*GenerateResult`, audit each
      `result.FiscalYears` entry individually (loop, same `auditPeriod` call
      as today), respond with `success`, `fiscalYears`, `fiscalYearStartMonth`,
      `fiscalYearEndMonth`, and `fiscalYear` (singular) only when
      `len(result.FiscalYears) == 1`.
- [ ] New `controllers/accountingperiod_locks.go`: six handlers
      (`LockAP`/`UnlockAP`/`LockAR`/`UnlockAR`/`LockGL`/`UnlockGL`) as thin
      wrappers over one shared `changeLock(w, r, lock accountingperiod.LockField,
      closing bool)` body — same auth chain (`authPeriod(..., authz.ActionUpdate)`),
      same request/response shape as `Close`/`Reopen` in
      `accountingperiod_actions.go`, same `auditPeriod` per-period audit
      loop with the lock-specific action verb.
- [ ] `controllers/accountingperiod_audit.go`: extend `periodSnapshot` to
      include `apLockStatus`/`arLockStatus`/`glLockStatus` (additive, low
      risk, keeps the audit snapshot showing the field that actually changed
      for a lock-only action).
- [ ] `main.go`: register the six new routes in the existing Finance block
      next to `close`/`reopen`; update the route-list doc comment on
      `AccountingPeriodOps` in `controllers/accountingperiod.go` to mention
      them.

## Task 7 — Verify

- [ ] `go build ./...`, `go vet ./...`, `go vet -tags dbtest ./...`, `go test
      ./...`.
- [ ] `gofmt -l` clean.
- [ ] Schema applied twice in one transaction against a scratch Postgres —
      idempotent, including the new backfill and the widened history-action
      constraint.
- [ ] `-tags dbtest` run against that database: `accountingperiod`, `journal`,
      `cashtransfer` (transitively exercises the GL lock through
      `CheckPeriodOpen`).
- [ ] `migration-auditor`, `tenancy-security-reviewer`, `module-drift-checker`
      on the diff.
