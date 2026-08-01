# Accounting Period Management — Implementation Plan

Spec: `docs/superpowers/specs/2026-07-31-accounting-period-management-design.md`
Branch: `feat/accounting-period-management`

## Task 1 — Schema

- [x] `database/migrations/tenant/schema.sql`, appended at EOF:
  - `ALTER TABLE accounting_settings ADD COLUMN IF NOT EXISTS fiscal_year_start_month SMALLINT`
  - `... base_period_start DATE`
  - `... accounting_calendar_configured_at TIMESTAMP`
  - guarded CHECK: `fiscal_year_start_month BETWEEN 1 AND 12`
  - `fiscal_year`, `accounting_period`, `accounting_period_history` + indexes
  - `EXCLUDE USING gist (daterange(period_start, period_end, '[]') WITH &&)`,
    added through a `DO $$` guard

## Task 2 — RBAC

- [x] `authz/catalog.go`: `ResourceAccountingPeriod` + 4 catalog entries
      (`read`, `create`, `update`, `configure`). No backfill.

## Task 3 — journal guard

- [x] `journal/period.go`: `periodVerdict` + `verdictFor` (pure), keep `isClosed`,
      add `isBefore`
- [x] `journal/store.go`: `CheckPeriodOpen`; call it from `CreateEntry`;
      `IsPeriodClosed` becomes a compat wrapper. New sentinel errors
      `ErrPeriodClosed`, `ErrNoAccountingPeriod`.
- [x] `journal/period_test.go`: table-driven over the resolution table
- [x] `journal/store_dbtest_test.go`: closed-period rejection at the choke point
      + the `books_closed_through` fallback

## Task 4 — accountingperiod package

- [x] `types.go`, `errors.go`
- [x] `calendar.go` + `calendar_test.go` — pure date math
- [x] `rules.go` + `rules_test.go` — pure sequencing rules
- [x] `store_setup.go`, `store_generate.go`, `store_get.go`,
      `store_status.go`, `store_history.go`, `sync.go`
- [x] `store_dbtest_test.go` (harness, setup, generation) +
      `store_dbtest_status_test.go` (close/reopen, reads) — split for the
      300-line cap, as chartofaccounts splits its own

## Task 5 — Controllers + routes

- [x] `controllers/accountingperiod.go` — auth chain, Calendar/List/Get/Current
- [x] `controllers/accountingperiod_actions.go` — Setup/GenerateYear/Close/Reopen
- [x] `controllers/accountingperiod_audit.go` — auditPeriod + History
- [x] `main.go` — 10 routes in the Finance block

## Task 6 — cashtransfer passthrough

- [x] `cashtransfer/errors.go` — `ErrNoAccountingPeriod` + `translatePeriodError`
- [x] `store_post.go` / `store_reverse.go` — call `CheckPeriodOpen`, translate
- [x] `controllers/cashtransfer.go` — `ctFail` maps both sentinels to 409

## Task 7 — Verify

- [x] `go build ./...`, `go vet ./...`, `go vet -tags dbtest ./...`, `go test ./...`
- [x] `gofmt` clean (checked against LF-normalized copies, as CI sees them)
- [x] Schema applied twice against pgvector/pgvector:pg16 in one transaction —
      idempotent, EXCLUDE constraint included
- [x] `-tags dbtest` run against that database, one per package as CI does
- [x] `migration-auditor`, `tenancy-security-reviewer`, `module-drift-checker`
      — 0 findings each
