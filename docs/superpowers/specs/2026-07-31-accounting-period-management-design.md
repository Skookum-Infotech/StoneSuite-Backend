# Accounting Period Management — Design

**Date:** 2026-07-31
**Branch:** `feat/accounting-period-management`
**Status:** approved

## 1. Why

`accounting_settings.books_closed_through` — a single nullable DATE — is the
entire period concept today. The cash-transfer design
(`2026-07-28-cash-transfer-design.md` §"Out of scope") named its replacement
explicitly: *"Fiscal calendars, per-month period open/close, opening balances."*
This is that follow-up, minus opening balances.

The gap that matters operationally: one date cannot express "September is closed
but October is open and August was reopened for an audit adjustment." Finance
teams close month by month, and a close must be reversible by someone with the
authority — and auditable afterwards.

## 2. Model

```
fiscal_year (FY2026, starts 2026-01-01, ends 2026-12-31)
  └── accounting_period × 12   (Jan 2026 … Dec 2026, each open|closed)
```

- The fiscal-year **start month** is tenant configuration (1–12), set once.
- Periods are always calendar months. A fiscal year is exactly 12 of them.
- Period status is `open` or `closed`. There is no third state.
- **Fiscal-year status is derived, never set directly**: `closed` when all 12 of
  its periods are closed, `open` otherwise. Recomputed in the same transaction
  as every period status change. This removes a whole class of drift — there is
  no way for a year to claim "closed" while a period under it is open.

### 2.1 Base period

`Setup` is a one-time, idempotent-by-refusal call that fixes:

- `fiscal_year_start_month` — 1..12
- `base_period_start` — the first day of the first period the tenant will ever
  post into; the **go-live boundary**

and generates the first fiscal year's 12 periods. Periods whose end falls before
`base_period_start` are created `closed` and can never be reopened — they
represent books that were closed in whatever system the tenant used before
StoneSuite.

Setup refuses with `ErrAlreadyConfigured` (409) if the calendar already exists.
Reconfiguring a live tenant's fiscal calendar is not a supported operation; it
would silently re-bucket every posted journal entry.

## 3. Lifecycle rules

These are pure functions in `accountingperiod/rules.go`, table-driven tested,
with no database access — the same split as `journal/period.go`'s `isClosed`.

**Close period N**

1. N exists and is `open` (else `ErrNotFound` / already-closed conflict).
2. Every period starting before N is already `closed`.
   Closing out of order would leave a hole that `books_closed_through` cannot
   represent and that no auditor would accept.

**Reopen period N**

1. N exists and is `closed`.
2. N is not before `base_period_start` (`ErrBeforeBasePeriod`).
3. Every period starting after N is `open` (`ErrLaterPeriodClosed`) — reopening
   runs strictly in reverse chronological order.

**Bulk** — `Close`/`Reopen` take a list of period ids. The list is sorted
(ascending for close, descending for reopen) and applied sequentially inside
**one transaction**. Any single failure rolls back the whole batch: a partial
close is exactly the inconsistent state the sequencing rules exist to prevent.
Single-period close is the same code path with a one-element list.

## 4. Enforcement

The closed-period guard moves **into `journal.CreateEntry`**. Every GL write is
validated at the choke point, so `cashtransfer` and every future posting module
inherit it and cannot forget it. `journal.ReverseEntry` calls `CreateEntry`, so
reversals are covered by the same guard.

`journal.CheckPeriodOpen(ctx, q, date)` resolves in this order:

| `accounting_period` covering the date | Result |
|---|---|
| exists, `open` | allowed |
| exists, `closed` | `ErrPeriodClosed` |
| absent, date < `base_period_start` | `ErrPeriodClosed` (pre-go-live) |
| absent, calendar configured | `ErrNoAccountingPeriod` |
| **no periods at all** | fall back to `books_closed_through` |

The last row is the backward-compatibility contract. A tenant that never runs
Setup behaves **exactly** as it does today — same column, same comparison, same
result. The new behaviour is opt-in per tenant, at the moment they configure a
calendar.

`journal` continues to import zero `stonesuite-backend/*` packages: the period
lookup is raw SQL against `accounting_period`, the same way it already reads
`accounting_settings`. `accountingperiod` and `journal` do not import each
other.

### 4.1 `books_closed_through` stays truthful

The column is kept (never drop a column — `CLAUDE.md` §Database) and recomputed
inside every close/reopen transaction as the end date of the **contiguous closed
prefix**. Anything still reading it — including `journal`'s own fallback path —
keeps getting a correct answer.

## 5. Package placement

`accountingperiod/` follows the `chartofaccounts` shape, not the document-module
clone-twin shape: tenant-global master data, no owner column, no status document,
no `lkp_record_status` rows. Consequently there is **no per-record IDOR scope
check** — there is nothing to scope against — only the resource-level
`accounting_period:<action>` grant. This is documented in the handler, as
`ChartOfAccountsOps` documents the same decision.

## 6. RBAC

New resource `accounting_period` under Finance, with four catalog entries:

| Action | Grants |
|---|---|
| `read` | list periods, fiscal years, calendar, current period, history |
| `create` | generate a fiscal year |
| `update` | close / reopen periods |
| `configure` | one-time base period setup |

`create` is separated from `update` deliberately: generating next year's calendar
is routine admin, whereas closing the books is a controller's signature.

No `role_permissions` backfill. A brand-new resource must fail closed for custom
roles; `super_admin` already holds the `*` wildcard. (The `inventory_unit`
backfill exists because that change *split* an existing permission — not the
case here.)

## 7. API

All under `/api/tenant/finance`, all on `tenantChain` (JWT + TenantResolver).

```
GET  /accounting-calendar                 read       fiscal calendar + base period
POST /accounting-calendar/setup           configure  one-time setup
GET  /fiscal-years                        read
POST /fiscal-years                        create     generate a year
GET  /accounting-periods                  read       ?fiscalYear=FY2026&status=open
GET  /accounting-periods/current          read       the period covering today
GET  /accounting-periods/{uuid}           read
GET  /accounting-periods/{uuid}/history   read
POST /accounting-periods/close            update     {"periodIds":[...]}
POST /accounting-periods/reopen           update     {"periodIds":[...]}
```

Status codes: 400 invalid input, 403 missing grant, 404 unknown period, 409
sequencing conflict / already configured / closed period.

## 8. Audit

Both layers, matching the existing convention:

- `accounting_period_history` rows written **inside** the mutating transaction
  (like `cash_transfer_history`) — a rolled-back close leaves no trail claiming
  it happened.
- `workflow.LogAuditFull` from the controller (like `auditCT`), so periods show
  up in the tenant audit browser under resource `accounting_period`.

## 9. Schema

Appended to `database/migrations/tenant/schema.sql`. New CHECK constraints are
wrapped in `DO $$` / `pg_constraint` existence guards (`ADD CONSTRAINT` is not
idempotent). Overlap is made structurally impossible by a `daterange` EXCLUDE
constraint rather than trusted to Go validation.

## 10. Out of scope

- **Opening balances.** The base period is a date boundary, not a balance
  carry-forward. `coa_account_balance` is untouched by this module.
- **Quarters.** Periods are months; a quarter is a reporting rollup, not a
  closable unit here.
- **Non-calendar periods** (13-period retail, 52/53-week).
- **Override posting into a closed period.** To post, reopen the period — which
  is itself permissioned and audited.
