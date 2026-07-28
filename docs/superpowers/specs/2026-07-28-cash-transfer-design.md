# Cash Transfer Module — Backend Design Spec

**Date:** 2026-07-28
**Status:** Approved by user in planning session, proceeding to implementation. Branch `feat/cash-transfer-module`.
**Scope:** New Cash Transfer module — moves funds between two Cash/Bank GL accounts, with Draft → Approve → Post → Reverse lifecycle. Because no general-ledger posting engine exists anywhere in this codebase yet, this spec also introduces the minimal GL foundation (`journal_entry` / `journal_entry_line`, an account running balance, and a single closed-period date) needed to make Post/Reverse real. Trial balance, P&L/Balance Sheet rollups, and a full fiscal calendar remain out of scope — same posture the Chart of Accounts spec took.

---

## 1. Overview & Goals

Add a **Cash Transfer** module — record-only money movement between two of the tenant's own Cash/Bank GL accounts — as a sibling of the existing relational document modules (`payment`, `refund`, `itemreceipt`), following the same v2 conventions: hybrid PK, employee-based audit, soft delete, `record_version`, RBAC/scope/IDOR, the `query/` filter engine, keyset pagination.

Today there is no way to move money between GL accounts at all, and no posting engine to make any document module's "Post" action produce real ledger effects. The **Chart of Accounts** module (`chartofaccounts/`, `docs/superpowers/specs/2026-07-25-chart-of-accounts-design.md`) ships the account tree, default-account slots, and the `is_postable`/`is_active`/`account_type` flags a transaction needs to resolve accounts — but its own spec (§9) explicitly defers "general ledger, journal entries, posting, balances, trial balance" to a future spec. This module is that future spec, scoped to exactly what Cash Transfer needs.

### What already exists (reuse, do not recreate)

| Concern | Existing asset | Location |
|---|---|---|
| GL accounts, `is_postable`/`is_active`, `account_type IN ('bank','cash')` | `chartofaccounts` / `coa_account` | `chartofaccounts/`, `tenant/schema.sql:4981` |
| Auth skeleton (the only one that logs `permission_denied`) | `controllers/payment.go:24-79` | `controllers/payment.go` |
| Cross-resource IDOR pattern | `recordInScope` | `controllers/scope.go:29` |
| Row-lock-then-post-once pattern, cached-total + ledger + dbtest invariant | `itemreceipt.Post` / `inventory_ledger` / `inventory_stock.quantity_on_hand` | `itemreceipt/store_post.go`, `itemreceipt/inventory_post.go` |
| Dedicated-Post + dedicated-reversal + generic Transition route shape | `item-receipts` (`/post`, `/void`, `/transition`) | `main.go:646-656` |
| Filter/sort/paginate/search | `query/` package | `query/` |
| Generic per-record attachments (already wired, record-agnostic by design) | `workflow_record_attachments` + `controllers/attachments.go` | `tenant/schema.sql:1325`, `controllers/attachments.go` |
| Audit log (one table, all resources) | `audit_logs` via `workflow.LogAudit` | `tenant/schema.sql:1278` |
| Safe (non-hardcoded-integer) status seed pattern | fabrication module's code-based `lkp_record_status` seed | `tenant/schema.sql:4181-4193` |
| Notes | Plain header TEXT columns (`*_notes`/`*_internal_notes`), not a separate table — every sibling module does this | e.g. `invoice_notes` |
| History | Per-module `_history` table + `Audit` handler | e.g. `invoice_history`, `sales_order_history` |

**Two negative findings that shaped this design:**

1. **No notification service exists** for any document-module lifecycle event — checked `payment/`, `estimate/`, `quote/`, `invoice/`, `refund/`; none send notifications on transitions. Only `services/email.go` (onboarding invites) sends anything. Cash Transfer does not add one either: the audit trail (`cash_transfer_history` + `audit_logs`) is the existing "record what happened" mechanism every sibling relies on, and inventing a notification channel with zero other callers would be new, unprecedented surface area, not reuse.
2. **`workflow.ResolveRecordAccess`** (the RBAC/IDOR resolver the generic attachments endpoint depends on) only recognizes three storage models today — v1 `workflow_records`, v2 `customer`, and `sales_order` — not `payment`/`invoice`/`quote`/`estimate`/`refund`/`chartofaccounts`. To make "Attachments" work for Cash Transfer, this function gets a **fourth branch**, added the same way the `sales_order` branch was added: additive, isolated, does not touch or fix the other modules' gap (out of scope here).

### What is genuinely missing (new tables — justified below)

- `cash_transfer`, `cash_transfer_history` — no existing table represents a fund transfer between two GL accounts.
- `journal_entry`, `journal_entry_line` — no journal/posting engine exists at all.
- `coa_account.coa_account_balance` — one new column; `coa_account` has no balance field today.
- `accounting_settings` — one new singleton row; no period/fiscal-calendar concept exists at all today.

---

## 2. Architecture Decisions

**AD-1 — The GL foundation lives in its own package, `journal/`, not inside `cashtransfer/`.** `journal_entry`/`journal_entry_line` conceptually belong to the ledger, not to any one document module, and Cash Transfer is simply the first caller. `journal/` exposes only a Go API (`CreateEntry`, `ReverseEntry`, `IsPeriodClosed`) with no HTTP routes of its own — the same posture as `query/`. `cashtransfer/store_post.go` and `store_reverse.go` call into it from inside their own transaction, exactly as `itemreceipt/store_post.go` calls the local `ledgerAndStock` helper inside its own transaction. This is scoped narrowly: no trial balance, no journal-entry HTTP surface, no generic "post an arbitrary journal entry" endpoint — just the two Go functions Cash Transfer needs, shaped so a future module can call the same two functions instead of re-deriving them.

**AD-2 — `journal_entry.source_type` + `source_id` is a documented, unenforced pointer, not a real FK.** The refund spec (AD-2 there) rejected a polymorphic `source_type`/`source_id` pair on `refund_application` in favor of two real nullable FKs with an XOR `CHECK`, because Postgres cannot enforce a polymorphic FK's referential integrity. That tradeoff doesn't transfer cleanly here: `refund_application` had exactly two known sources forever (`payment`, `credit_memo`), so real FKs cost one extra column. A journal entry's source is, by definition, "whatever module posts next" — an unbounded, growing set — so a real FK per source module would mean altering `journal_entry` every time a new module starts posting. `source_type TEXT NOT NULL` (`'cash_transfer'` for now) + `source_id UUID NOT NULL`, indexed but not FK-constrained, accepts the referential-integrity tradeoff explicitly in exchange for not having to touch this table again. `journal/` is trusted internal code called only by document-module store layers (never directly off user input), the same trust boundary `query/` and `ai/` already operate inside.

**AD-3 — Account balance is a maintained running total, not computed on read, mirroring `inventory_stock.quantity_on_hand`.** `coa_account_balance DECIMAL(15,2) NOT NULL DEFAULT 0` is updated transactionally by `journal.CreateEntry`/`ReverseEntry` in the same transaction as the `journal_entry_line` insert — never derived by summing lines at read time. A dbtest invariant (mirroring `itemreceipt/store_test.go`'s `stockFor`) asserts `coa_account_balance == SUM(journal_entry_line.debit - journal_entry_line.credit)` per account after every posting/reversal test. The column is debit-positive; Cash Transfer only ever posts to debit-normal accounts (Assets, `account_type IN ('bank','cash')`), so this sign convention is correct for every account this module touches. A future module posting to credit-normal accounts (Liabilities/Equity/Revenue) will need to decide how it wants that displayed — explicitly out of scope here, flagged so it reads as intent, not oversight.

**AD-4 — Closed period is one nullable date, not a fiscal calendar.** `accounting_settings` is a singleton row (`accounting_settings_id SMALLINT PRIMARY KEY DEFAULT 1`, `CHECK (accounting_settings_id = 1)`) holding `books_closed_through DATE NULL`. Posting or reversing with an effective date on or before that date is blocked with `409`. No `accounting_period` table, no per-month rows, no open/close-month endpoints — those become a real fiscal-calendar spec if/when a second module needs period-level granularity. This is the minimum that satisfies "prevent posting to closed accounting periods" today.

**AD-5 — Cash Transfer is a single-step approval, not the AD-8 multi-approver chain.** `estimate`/`quote` use `approval.go` with `approverCount`/`signOffCount`. Cash Transfer's lifecycle is `DRFT → APPR → POST → RVSD`, with `CANC` reachable from `DRFT`/`APPR` only. Approve is one `Transition` call (`DRFT→APPR`), gated by `authz.ActionTransition` — not a new `ActionApprove` — following the documented convention (`ActionApprove` is reserved for `{ResourceRecord, ActionApprove}` and document modules do not get it independently). No `_approver`/`_approval` tables.

**AD-6 — Post and Reverse are dedicated handlers, not generic transitions**, exactly like `item-receipts`' `/post` and `/void`: they have real side effects (create a journal entry, move a running balance) that the generic `transitions.go` state-machine map cannot express. `Transition` stays generic and side-effect-free (Approve, Cancel); `Post`/`Reverse` are their own store functions that lock the header row (`SELECT ... FOR UPDATE`), check `IsPosted`, call into `journal/`, and update status — all in one transaction, mirroring `itemreceipt.Post` line for line.

**AD-7 — Source/destination accounts are restricted to `account_type IN ('bank','cash')`**, checked against live `chartofaccounts` data (`is_postable AND is_active AND deleted_at IS NULL`) at Create, Update, and Post. This matches the module's name and the user's own phrasing ("Cash/Bank GL accounts") rather than allowing a transfer into an arbitrary GL account.

**AD-8 — `lkp_record_status` is seeded with the fabrication-style safe pattern, not the old hardcoded-integer style.** The original ~17 record types hardcode `record_status_record_type` as a bare integer relying on `SERIAL` insertion order (a known trap — see `new-module` skill's `wiring.md`). The most recent module (`fabrication`, `tenant/schema.sql:4181-4193`) instead resolves the type id by code: `INSERT INTO lkp_record_status (...) SELECT v.code, v.name, rt.record_type_id, ... FROM (VALUES (...)) AS v CROSS JOIN lkp_record_type rt WHERE rt.record_type_code = 'FJOB' ON CONFLICT (...) DO NOTHING`. Cash Transfer's seed uses this safer pattern, keyed to a new `CTRF` record type inserted as its own append-only `INSERT` statement (not edited into the original 17-row list).

**AD-9 — `DELETE` is included for CRUD parity even though it wasn't in the user's literal op list.** Draft-only soft delete, gated the same way `Update` is. Omitting it would read as an accidental gap to `module-drift-checker` (every sibling module has it), and it costs nothing beyond the standard catalog row.

---

## 3. Schema

All appended to `database/migrations/tenant/schema.sql`, in FK order: `accounting_settings` → `journal_entry` → `journal_entry_line` → `coa_account` (balance column) → `lkp_record_type`/`lkp_record_status` (CTRF) → `cash_transfer` → `cash_transfer_history`.

```sql
-- accounting_settings -- singleton; the entire "closed period" concept (AD-4) --------
CREATE TABLE IF NOT EXISTS accounting_settings (
    accounting_settings_id      SMALLINT     PRIMARY KEY DEFAULT 1,
    books_closed_through        DATE             NULL,
    accounting_settings_updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    accounting_settings_updated_by INTEGER       NULL REFERENCES employee(employee_id),
    CONSTRAINT chk_accounting_settings_singleton CHECK (accounting_settings_id = 1)
);
INSERT INTO accounting_settings (accounting_settings_id) VALUES (1) ON CONFLICT DO NOTHING;

-- journal_entry -- the GL posting header (AD-1, AD-2) ---------------------------------
CREATE TABLE IF NOT EXISTS journal_entry (
    journal_entry_id          SERIAL       PRIMARY KEY,
    journal_entry_uuid        UUID         NOT NULL DEFAULT gen_random_uuid(),
    journal_entry_number      VARCHAR(20)  NOT NULL,
    entry_date                DATE         NOT NULL,
    memo                      TEXT         NOT NULL DEFAULT '',
    source_type               VARCHAR(30)  NOT NULL,
    source_id                 UUID         NOT NULL,
    is_reversal               BOOLEAN      NOT NULL DEFAULT FALSE,
    reverses_journal_entry_id INTEGER          NULL REFERENCES journal_entry(journal_entry_id),
    journal_entry_created_at  TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    journal_entry_created_by  INTEGER          NULL REFERENCES employee(employee_id),

    CONSTRAINT uq_je_uuid   UNIQUE (journal_entry_uuid),
    CONSTRAINT uq_je_number UNIQUE (journal_entry_number)
);
CREATE INDEX IF NOT EXISTS idx_je_source ON journal_entry (source_type, source_id);

-- journal_entry_line -- one debit or credit leg (AD-3) ---------------------------------
CREATE TABLE IF NOT EXISTS journal_entry_line (
    journal_entry_line_id SERIAL       PRIMARY KEY,
    journal_entry_id      INTEGER      NOT NULL REFERENCES journal_entry(journal_entry_id),
    line_number            INTEGER      NOT NULL,
    coa_account_id         INTEGER      NOT NULL REFERENCES coa_account(coa_account_id),
    debit                  DECIMAL(15,2) NOT NULL DEFAULT 0,
    credit                 DECIMAL(15,2) NOT NULL DEFAULT 0,

    CONSTRAINT uq_jel_line          UNIQUE (journal_entry_id, line_number),
    CONSTRAINT chk_jel_nonneg       CHECK (debit >= 0 AND credit >= 0),
    CONSTRAINT chk_jel_one_side     CHECK (NOT (debit > 0 AND credit > 0)),
    CONSTRAINT chk_jel_nonzero      CHECK (debit > 0 OR credit > 0)
);
CREATE INDEX IF NOT EXISTS idx_jel_account ON journal_entry_line (coa_account_id);
CREATE INDEX IF NOT EXISTS idx_jel_entry   ON journal_entry_line (journal_entry_id);

-- coa_account running balance (AD-3) ---------------------------------------------------
ALTER TABLE coa_account ADD COLUMN IF NOT EXISTS coa_account_balance DECIMAL(15,2) NOT NULL DEFAULT 0;

-- New record type for Cash Transfer, appended as its own statement (AD-8) -------------
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

-- cash_transfer -- header ---------------------------------------------------------------
CREATE TABLE IF NOT EXISTS cash_transfer (
    cash_transfer_id           SERIAL       PRIMARY KEY,
    cash_transfer_uuid         UUID         NOT NULL DEFAULT gen_random_uuid(),
    cash_transfer_number       VARCHAR(20)  NOT NULL,
    record_type                INTEGER      NOT NULL REFERENCES lkp_record_type(record_type_id),
    cash_transfer_status       INTEGER      NOT NULL REFERENCES lkp_record_status(record_status_id),
    cash_transfer_date         DATE         NOT NULL DEFAULT CURRENT_DATE,
    from_account_id            INTEGER      NOT NULL REFERENCES coa_account(coa_account_id),
    to_account_id               INTEGER      NOT NULL REFERENCES coa_account(coa_account_id),
    cash_transfer_amount       DECIMAL(15,2) NOT NULL,
    cash_transfer_reference    VARCHAR(100) NOT NULL DEFAULT '',
    cash_transfer_notes        TEXT         NOT NULL DEFAULT '',
    cash_transfer_internal_notes TEXT       NOT NULL DEFAULT '',
    cash_transfer_custom_fields JSONB       NOT NULL DEFAULT '{}',
    cash_transfer_owner_id     INTEGER          NULL REFERENCES employee(employee_id),
    journal_entry_id            INTEGER          NULL REFERENCES journal_entry(journal_entry_id),
    reversal_journal_entry_id   INTEGER          NULL REFERENCES journal_entry(journal_entry_id),
    cash_transfer_posted_at    TIMESTAMP        NULL,
    cash_transfer_posted_by    INTEGER          NULL REFERENCES employee(employee_id),
    cash_transfer_reversed_at  TIMESTAMP        NULL,
    cash_transfer_reversed_by  INTEGER          NULL REFERENCES employee(employee_id),
    cash_transfer_created_at   TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    cash_transfer_created_by   INTEGER          NULL REFERENCES employee(employee_id),
    cash_transfer_updated_at   TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    cash_transfer_updated_by   INTEGER          NULL REFERENCES employee(employee_id),
    cash_transfer_deleted_at   TIMESTAMP        NULL,
    cash_transfer_deleted_by   INTEGER          NULL REFERENCES employee(employee_id),
    cash_transfer_record_version INTEGER    NOT NULL DEFAULT 1,

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
-- keyset pairs
CREATE INDEX IF NOT EXISTS idx_ct_created_keyset ON cash_transfer (cash_transfer_created_at, cash_transfer_id) WHERE cash_transfer_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_ct_updated_keyset ON cash_transfer (cash_transfer_updated_at, cash_transfer_id) WHERE cash_transfer_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_ct_number_keyset  ON cash_transfer (cash_transfer_number, cash_transfer_id)     WHERE cash_transfer_deleted_at IS NULL;

-- cash_transfer_history -- status trail (mirrors invoice_history / sales_order_history) -
CREATE TABLE IF NOT EXISTS cash_transfer_history (
    cash_transfer_history_id SERIAL      PRIMARY KEY,
    cash_transfer_id          INTEGER     NOT NULL REFERENCES cash_transfer(cash_transfer_id),
    from_status_id             INTEGER         NULL REFERENCES lkp_record_status(record_status_id),
    to_status_id                INTEGER         NULL REFERENCES lkp_record_status(record_status_id),
    history_action              VARCHAR(20) NOT NULL,
    history_at                  TIMESTAMP   NOT NULL DEFAULT CURRENT_TIMESTAMP,
    history_by                  INTEGER         NULL REFERENCES employee(employee_id),
    CONSTRAINT chk_ct_history_action CHECK (history_action IN
        ('create','update','transition','post','reverse','delete'))
);
CREATE INDEX IF NOT EXISTS idx_ct_history_record ON cash_transfer_history (cash_transfer_id, history_at DESC);
```

---

## 4. Package Layout

```
journal/                    (new — internal GL primitives, no HTTP surface, AD-1)
  types.go                  JournalEntry, JournalEntryLine, CreateEntryInput, LineInput
  numbering.go + _test.go   JE- number formatting (pure)
  period.go + _test.go      IsPeriodClosed (pure date-compare logic split from the DB read)
  store.go                  CreateEntry — validates balanced lines, inserts header+lines,
                             updates coa_account_balance, all against the caller's pgx.Tx
  reverse.go                ReverseEntry — mirrors CreateEntry with swapped debit/credit
  store_test.go             //go:build dbtest — balance invariant, unbalanced-entry rejection

cashtransfer/                (new — document module)
  types.go                  CreateInput, UpdateInput, CashTransfer response, Page
  numbering.go + _test.go   CTRF- number formatting (pure)
  transitions.go + _test.go DRFT/APPR/POST/CANC/RVSD map, CanTransition (pure)
  resolver.go + _test.go    query.FieldResolver whitelist (pure)
  errors.go                 ErrNotFound, ErrInvalidTransition, ErrAlreadyPosted, ErrNotPosted,
                             ErrPeriodClosed, ClientError
  store.go                  shared helpers + Get: recordTypeIDByCode, statusIDByCode, scanCT
  store_create.go           validates accounts (AD-7), inserts header + history row
  store_update.go           Draft-only guard, nil-guard on custom_fields
  store_search.go           List/Search via query/
  store_transition.go       Approve (DRFT→APPR), Cancel (DRFT/APPR→CANC)
  store_post.go             APPR→POST: FOR UPDATE lock, IsPosted check, builds the two-line
                             journal.CreateEntryInput (debit to_account, credit from_account),
                             calls journal.CreateEntry, sets journal_entry_id/posted_at
  store_reverse.go          POST→RVSD: FOR UPDATE lock, calls journal.ReverseEntry,
                             sets reversal_journal_entry_id/reversed_at
  store_test.go             //go:build dbtest — double-post race, period-closed rejection,
                             different-accounts/positive-amount/active-account rejection

controllers/
  cashtransfer.go           authCashTransfer / authCashTransferByUUID (copied from payment.go,
                             AD-9's DELETE included), List/Search/Create/Get/Update/Delete/
                             Transition/Post/Reverse
  cashtransfer_audit.go     Audit handler
```

Every file stays under the 300-line cap.

---

## 5. API Surface

All routes `/api/tenant/finance/cash-transfers/...`, behind the mandatory JWT + `TenantResolver` + `authz.Check` chain (`tenantChain`, `main.go:370`). This nests under the same `/finance/` prefix Chart of Accounts established for itself ("the first module of the Finance section", CoA spec §6) — Cash Transfer is the second, and the one that makes CoA's balances real, so it belongs in that section rather than sitting flat alongside the CRM/sales document modules.

| Method | Route | Permission | Notes |
|---|---|---|---|
| `GET` | `/finance/cash-transfers` | `read` | keyset list |
| `POST` | `/finance/cash-transfers/search` | `read` | filter+sort+paginate via `query/` |
| `POST` | `/finance/cash-transfers` | `create` | Draft only |
| `GET` | `/finance/cash-transfers/{uuid}` | `read` | |
| `PATCH` | `/finance/cash-transfers/{uuid}` | `update` | 409 unless status = Draft |
| `DELETE` | `/finance/cash-transfers/{uuid}` | `delete` | soft delete, Draft only (AD-9) |
| `POST` | `/finance/cash-transfers/{uuid}/transition` | `transition` | Approve (DRFT→APPR), Cancel (→CANC) |
| `POST` | `/finance/cash-transfers/{uuid}/post` | `transition` | APPR→POST; creates journal entry, updates balances |
| `POST` | `/finance/cash-transfers/{uuid}/reverse` | `transition` | POST→RVSD; creates reversing entry, updates balances |
| `GET` | `/finance/cash-transfers/{uuid}/audit` | `read` | history |

Attachments and Notes have **no dedicated routes** — Attachments reuse `/api/tenant/records/{id}/attachments/*` (via the `ResolveRecordAccess` extension below); Notes are plain fields returned/updated through `Get`/`Update`.

**Wiring edits (additive, isolated):**
- `authz/catalog.go`: `ResourceCashTransfer Resource = "cash_transfer"` + 5 rows (create/read/update/delete/transition).
- `controllers/crm.go` `resourceForKey`: `case "cash_transfer": return authz.ResourceCashTransfer`.
- `workflow/attachments.go` `ResolveRecordAccess`: new 4th branch querying `cash_transfer` by `cash_transfer_uuid`, mirroring the existing `sales_order` branch (owner from `cash_transfer_owner_id` → `employee` → `users.id`).
- `main.go`: one constructor + 10 `mux.Handle` lines in the tenant block, all wrapped in `tenantChain`.

---

## 6. Validation & Error Handling

| Validation | Enforced by |
|---|---|
| Different accounts | `CHECK chk_ct_diff_accounts` + store-level `400` before insert |
| Amount positive | `CHECK chk_ct_amount_positive` + store-level `400` |
| Both accounts active + postable + bank/cash (AD-7) | Store queries `coa_account` at Create/Update/Post; `400` naming the offending account |
| Accounting period not closed (AD-4) | `journal.IsPeriodClosed(ctx, tx, effectiveDate)` at Post and Reverse; `409`. `effectiveDate` is `cash_transfer_date` at Post, and `CURRENT_DATE` at Reverse (a reversal always happens "today" — there is no separate user-supplied reversal date) |
| No duplicate posting | `SELECT ... FOR UPDATE` row lock on `cash_transfer` + status check → `ErrAlreadyPosted` (`409`), mirroring `itemreceipt.Post` |
| No edit after posting | `Update`/`Delete` reject with `409` unless status = Draft |
| Reverse only valid for Posted | `Reverse` rejects with `409` unless status = Posted |

### Status codes

| Code | Cause |
|---|---|
| `400` | different-accounts/positive-amount/account-not-eligible validation; `*query.InvalidFilterError` |
| `403` | RBAC denial (logged via `logSecurityEvent` with `permission_denied`) |
| `404` | unknown uuid, or IDOR scope denial (never `403`, per CLAUDE.md) |
| `409` | already posted; not posted (reverse); period closed; edit/delete after posting; invalid transition; `recordVersion` mismatch |
| `500` | wrapped internal errors only |

All responses carry `success` and `message` per CLAUDE.md.

### Sorting and pagination

Keyset via `query/`, default page 25, `MaxLimit` 100. Sortable fields: `created_at`, `updated_at`, `cash_transfer_number` (this module's `record_number` equivalent, same deviation CoA documented for `coa_account_code` — stable, non-null, unique among live rows).

---

## 7. Testing

**Pure functions — stdlib `testing`, table-driven** (this repo's convention for pure functions per `module-anatomy.md`):

- `cashtransfer/numbering`, `journal/numbering`: prefix formatting, zero-padding.
- `cashtransfer/transitions`: every legal/illegal edge (`DRFT→APPR` ok, `POST→APPR` rejected, `RVSD→*` all rejected, etc.).
- `cashtransfer/resolver`: whitelist membership, unresolved key behavior.
- `journal/period`: date-compare boundary (on the closed date, day after, `NULL` closed-through).

**Database-backed — `-tags dbtest`, skipping cleanly without `TEST_DATABASE_URL`:**

- Create rejects same-account, non-positive amount, inactive/non-bank-or-cash account.
- Post: creates a balanced 2-line journal entry (debit `to_account`, credit `from_account`), updates both accounts' `coa_account_balance`, sets `journal_entry_id`/`posted_at`; re-posting the same transfer returns `ErrAlreadyPosted`.
- Concurrent double-post: two goroutines posting the same transfer — exactly one succeeds (proves the `FOR UPDATE` lock), mirroring the intent of `itemreceipt/store_test.go`.
- Post blocked when `cash_transfer_date <= accounting_settings.books_closed_through`.
- Reverse: only legal from Posted; creates a reversing entry with swapped debit/credit, restores both accounts' balances to their pre-post values, sets `reversal_journal_entry_id`/`reversed_at`.
- Balance invariant: after any sequence of posts/reverses in the test, `coa_account_balance == SUM(journal_entry_line.debit - journal_entry_line.credit)` for every touched account.
- Update/Delete rejected once status ≠ Draft.
- Re-applying `schema.sql` twice is a no-op (idempotency).

---

## 8. Out of Scope

Deliberately excluded, consistent with how Chart of Accounts scoped itself:

- **Trial balance, P&L, Balance Sheet rollups.** `coa_account_balance` exists to serve this module's own invariant, not as a reporting feature.
- **Fiscal calendars, per-month period open/close, opening balances.** `accounting_settings.books_closed_through` is the entire period concept for now (AD-4).
- **Any other module posting through `journal/`.** The package is shaped to allow it (AD-1, AD-2) but nothing beyond Cash Transfer is wired to it in this iteration.
- **Notifications.** No document module has this today; not adding it here either (see §1).
- **Multi-approver approval chains.** Single-step only (AD-5).
- **Multi-currency.** Matches the single-entity/single-currency policy already established by Chart of Accounts.
