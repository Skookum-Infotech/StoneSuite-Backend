# Accounting Period Management — Sub-Ledger Locks, Quarters, Range Generation

**Date:** 2026-07-31
**Branch:** `feat/accounting-period-management`
**Status:** proposed
**Builds on:** `docs/superpowers/specs/2026-07-31-accounting-period-management-design.md` (commit `b8243ff`)

## 1. Why

The base module (`b8243ff`) gave every accounting period one status: open or
closed, closed by one authority. Real close cycles are not that flat — AP,
AR and the general ledger close on different days, by different people, and a
tenant setting up a new install wants to generate several years of calendar at
once, not click "generate" twelve times. This is the follow-up that adds:

1. Fiscal start/end month surfaced as read-only context on the generate API.
2. Multi-year range generation (`startYear`..`endYear`, single year when equal).
3. Duplicate-proof, single-transaction generation across the whole range.
4. Three independent sub-ledger locks per period — AP, AR, GL.
5. Overall period status derived from the three locks (`closed` iff all three
   are `closed`), recomputed on every lock change, never set directly.

## 2. Sub-ledger locks

`accounting_period` gains three columns, each `open`/`closed`, defaulting
`open`: `ap_lock_status`, `ar_lock_status`, `gl_lock_status`.

`accounting_period_status` stops being written directly by a lock change and
becomes derived — the same posture `fiscal_year_status` already has over its
periods:

```
accounting_period_status = closed  iff  ap_lock_status = closed
                                    AND  ar_lock_status = closed
                                    AND  gl_lock_status = closed
                            open    otherwise
```

`accounting_period_closed_at`/`_closed_by` stay paired with the DERIVED
status (`chk_ap_closed_pair` is unchanged), so they still answer "when did
this period fully close" — not per-lock. Per-lock timestamps are not added;
`accounting_period_history` already timestamps every lock change individually,
which is enough of a trail without three more nullable columns.

### 2.1 Two ways to change a lock

**Whole-period `Close`/`Reopen` (existing, unchanged behaviour).** Still takes
a batch of period ids, still enforced by the existing chronological
sequencing rule (`PlanClose`/`PlanReopen`, unchanged), still writes exactly
one `close`/`reopen` history row per period. The only change is mechanical:
the write now sets `ap_lock_status`, `ar_lock_status` and `gl_lock_status` to
the same target as `accounting_period_status`, in the same statement — so for
every caller who never touches the new granular endpoints, the three locks
are always in lock-step with the status they already see today. This is what
makes the change backward compatible: existing tests, existing API
responses, existing UI all keep working unmodified.

**New granular `LockAP`/`UnlockAP`/`LockAR`/`UnlockAR`/`LockGL`/`UnlockGL`.**
Each takes the same `{periodIds, note}` batch shape as Close/Reopen, and is
governed by the *same* chronological sequencing rule — applied to that one
lock's own history, not the derived status. AP can lag GL by a period the way
real close cycles do, but AP itself still can't skip a month, for the same
reason the whole-period rule exists: a hole in one sub-ledger's closed prefix
is not a state anyone can act on sanely. After the write, the overall
`accounting_period_status` is *recomputed* from the three current lock values
(never assumed), then fiscal year status and `books_closed_through` are
resynced exactly as they are today after any status change.

Both paths run inside one transaction, row-locked the same way
`changeStatus` already locks the table today (`ORDER BY period_start FOR
UPDATE`) — no new locking primitive.

### 2.2 The GL lock is the GL choke point

`journal.CheckPeriodOpen` — the single choke point every `CreateEntry` call
goes through — currently reads `accounting_period_status`. It moves to
reading `gl_lock_status` instead. This is the one behavioural change outside
`accountingperiod/`, and it is what makes the GL lock real rather than
decorative: "All GL Transactions" locked has to mean GL postings actually
stop.

Backward compatibility argument: `gl_lock_status` is written identically to
`accounting_period_status` by every existing code path (Setup's pre-go-live
closed periods, and whole-period Close/Reopen). A tenant that never calls the
new granular lock endpoints will always have `gl_lock_status ==
accounting_period_status`, so `journal`'s behaviour is unchanged for them. The
migration backfill (§5) makes this true for periods that were closed before
this change ships, too. Only a tenant that actively uses `LockGL`/`UnlockGL`
independently of AP/AR sees new behaviour — which is the capability being
asked for.

**Out of scope:** wiring AP/AR locks into a choke point of their own. There is
no existing AP or AR posting choke point to wire into — `journal` is GL-only,
and retrofitting per-subledger gating into `invoice`, `payment`, `vendors`,
`creditmemo`, `refund` (the clone-twin document modules) is a separate,
much larger initiative the task does not ask for. AP/AR locks are exposed as
first-class period state — set, read, audited — ready for a future module to
consume the same way `journal` consumes the GL lock, but nothing consumes
them yet.

## 3. Quarters

`fiscal_quarter`: four rows per fiscal year, each spanning three consecutive
periods (Q1 = periods 1-3, etc). Status is derived the same way fiscal year
status is — closed iff its three periods are all closed — recomputed
alongside fiscal year status on every period status change.

Generated only going forward, by the same `generateYear` call that creates a
fiscal year's periods. **Not backfilled onto fiscal years that already exist**
— `accounting_period.fiscal_quarter_id` is nullable, and pre-existing periods
keep it `NULL`. Synthesizing non-overlapping quarter boundaries retroactively
for already-generated years is not needed by anything the task asks for, and
every read path already treats the field as optional.

No new CRUD/history/lock surface for quarters — they ride along embedded in
`FiscalYear.Quarters` and are visible per-period as `Period.QuarterID` /
`Period.QuarterName`, exactly like periods already ride along embedded in
`FiscalYear.Periods`. The original design's "quarters are a reporting rollup,
not a closable unit" stance (§10 of the base spec) still holds — quarters
have no lock of their own, only a derived status.

## 4. Range generation

`GenerateInput` gains `EndYear`. Resolution rules, in order:

| `StartYear` | `EndYear` | Behaviour |
|---|---|---|
| 0 | 0 | Generate exactly the next contiguous year (today's default, unchanged) |
| N | 0 | Generate exactly year N, as confirmation that N is the next contiguous year (today's behaviour, unchanged) |
| N | 0 (implicit = N) | — same row as above; `EndYear` defaults to `StartYear` |
| N | M, M ≥ N | Generate N, N+1, … M — one fiscal year per calendar year in the range, contiguous, all inside one transaction |
| N | M, M < N | 400: `endYear cannot be before startYear` |
| 0 | M ≠ 0 | 400: `startYear is required when endYear is given` |

`StartYear`, when given, is still a confirmation, not a free choice — it must
equal the year the next contiguous fiscal year actually starts in, exactly as
today. This is what makes "prevent duplicate generation" hold for ranges the
same way it already holds for single years: a caller cannot silently
regenerate or skip a year, they can only confirm the one that is next.

A range is capped at `maxGenerateYears = 20` — not asked for explicitly, but
a bare validation against an unbounded request size, consistent with
"handlers must validate input." Trivial to change if 20 is wrong.

The whole range is one transaction: the calendar row is locked once
(`lockCalendar`, unchanged), the next contiguous start is read once, then
`generateYear` runs once per requested year against the same `tx`, threading
each year's end into the next year's start. Any failure — a duplicate, an
overlap, a mid-range unique-constraint hit — rolls back every year in the
batch, not just the one that failed. This is the same all-or-nothing posture
`changeStatus` already has for bulk close/reopen.

## 5. Fiscal Start/End Month on the generate API

`GenerateFiscalYear` returns a `GenerateResult`:

```go
type GenerateResult struct {
    FiscalYears          []FiscalYear `json:"fiscalYears"`
    FiscalYearStartMonth int          `json:"fiscalYearStartMonth"`
    FiscalYearEndMonth   int          `json:"fiscalYearEndMonth"`
}
```

Start/end month are read from `accounting_settings` — Accounting
Preferences, the same singleton `GetCalendar`/`Calendar` already reads —
inside the *same* locked read (`lockCalendar`) the generation itself uses, so
there is no separate query and no risk of the two ever disagreeing. End month
is derived, never stored: `FiscalYearEndMonth(startMonth)` is `startMonth -
1`, wrapping January to December — a fiscal year is always exactly twelve
months, so the end month is implied. Both are request-independent: `Generate`
doesn't accept them as input, and nothing in this change makes them settable
outside `Setup`'s one-time configuration.

**Response shape / backward compatibility.** The endpoint keeps returning
`fiscalYear` (singular) whenever exactly one year was generated — which is
every call an existing caller makes, since they never send `endYear`. A range
call additionally populates `fiscalYears` (plural, the full list); `fiscalYear`
is only present when `len(fiscalYears) == 1`. An old client reading
`resp.fiscalYear` sees byte-for-byte what it saw before.

## 6. Schema (appended to `tenant/schema.sql`)

```sql
-- fiscal_quarter: 4 rows per fiscal year, status derived like fiscal_year_status
CREATE TABLE IF NOT EXISTS fiscal_quarter ( ... )

ALTER TABLE accounting_period
    ADD COLUMN IF NOT EXISTS fiscal_quarter_id INTEGER NULL REFERENCES fiscal_quarter(fiscal_quarter_id);
ALTER TABLE accounting_period ADD COLUMN IF NOT EXISTS ap_lock_status VARCHAR(10) NOT NULL DEFAULT 'open';
ALTER TABLE accounting_period ADD COLUMN IF NOT EXISTS ar_lock_status VARCHAR(10) NOT NULL DEFAULT 'open';
ALTER TABLE accounting_period ADD COLUMN IF NOT EXISTS gl_lock_status VARCHAR(10) NOT NULL DEFAULT 'open';
-- guarded CHECKs on the three lock columns and on fiscal_quarter's status/number

-- widen chk_ap_history_action (DROP CONSTRAINT IF EXISTS + unconditional ADD,
-- since it is inline in accounting_period_history's CREATE TABLE body and a
-- second CREATE TABLE IF NOT EXISTS is a no-op on every already-provisioned
-- tenant — same reasoning CLAUDE.md gives for never editing a CREATE TABLE
-- body to add a column):
--   'ap_lock','ap_unlock','ar_lock','ar_unlock','gl_lock','gl_unlock'

-- Backfill: a period already closed under the old single-status model gets
-- all three locks closed too, exactly once. Guarded so it cannot re-fire and
-- clobber a deliberately-diverged lock state after this ships (schema.sql
-- re-runs in full on every boot):
UPDATE accounting_period
SET ap_lock_status = 'closed', ar_lock_status = 'closed', gl_lock_status = 'closed'
WHERE accounting_period_status = 'closed'
  AND ap_lock_status = 'open' AND ar_lock_status = 'open' AND gl_lock_status = 'open';
```

The backfill's `WHERE` is the guard: it only ever matches a period that is
closed overall but whose locks are still at their just-added default — a
state that, after this ships, only pre-existing data can be in (going
forward `accounting_period_status` is derived FROM the locks, so "closed
overall, all locks open" cannot arise from any code path this module
controls). Re-running it on every boot is therefore a no-op after the first.

## 7. RBAC, routes, audit

No new RBAC resource or action. The six lock endpoints reuse
`accounting_period:update` — locking one sub-ledger is the same authority
tier "closing the books" already requires.

```
POST /api/tenant/finance/accounting-periods/lock-ap
POST /api/tenant/finance/accounting-periods/unlock-ap
POST /api/tenant/finance/accounting-periods/lock-ar
POST /api/tenant/finance/accounting-periods/unlock-ar
POST /api/tenant/finance/accounting-periods/lock-gl
POST /api/tenant/finance/accounting-periods/unlock-gl
```

Same body shape as close/reopen: `{"periodIds":[...],"note":"..."}`. Same
response shape: `{"periods":[...],"booksClosedThrough":...}`.

Audit: one `accounting_period_history` row per period per call (action ∈
`ap_lock`/`ap_unlock`/`ar_lock`/`ar_unlock`/`gl_lock`/`gl_unlock`), plus one
`workflow.LogAuditFull` row via the existing `auditPeriod` helper — the same
two-layer convention every other mutation here already follows.

## 8. Out of scope

- Wiring AP/AR locks into any posting module (§2.2).
- Retroactive quarters for already-generated fiscal years (§3).
- Per-lock `closed_at`/`closed_by` timestamps (§2).
- A `?apLockStatus=`-style filter on `List` — not asked for, easy to add later
  through the same `Filters` struct if needed.
- Changing `fiscal_year_start_month` after Setup — still single-shot,
  untouched by this change.
