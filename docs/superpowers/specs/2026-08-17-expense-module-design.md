# Expense Module — Backend Design Spec

**Date:** 2026-08-17
**Status:** Approved — proceeding to implementation.
**Scope:** New Expense module (employee expense claims: header + line items +
configuration-driven approval + reject + receipts) for the StoneSuite multi-tenant,
database-per-tenant CRM/ERP backend.

---

## 1. Overview & Goals

An Expense Claim is a self-service submission by an employee covering one or more
dated, categorized spend entries (receipts) that a manager approves or rejects before
Finance marks it reimbursed. Supports Create, Update (Draft only), Submit, Approve,
Reject, Reimburse ("process"), Get, Search, soft delete, receipt attachments, and full
audit history, per the request. Inactive users/employees are blocked from every
mutating action.

**Non-negotiable constraints (from CLAUDE.md, identical to every sibling module):**

- Database-per-tenant; no `tenant_id` column anywhere.
- v2 relational conventions: hybrid PK (`SERIAL` + `UUID`), `employee(employee_id)`-based
  audit columns, paired soft delete, `record_version` optimistic concurrency.
- Idempotent, append-only migrations (`CREATE TABLE IF NOT EXISTS`, `ON CONFLICT DO
  NOTHING`).
- Mandatory security chain on every `/api/tenant/` route.
- All list/search goes through `query/` (whitelisted `FieldResolver`, parameterized
  values, keyset pagination, filter × scope ANDed).

### Key finding that shaped this design

`authz.catalog.go:59,339-343` already carries `ResourceExpense` with `Create/Read/
Update/Delete/Transition` — no `ActionApprove` row, matching `ResourceRequisition`'s
pattern (approve gates on `ActionTransition`, not a dedicated action), not
`ResourceVendorCredit`'s. `controllers/crm.go:87-88` already maps `"expense"` →
`authz.ResourceExpense`. Both predate this module and are correct as-is — no catalog
or router changes needed.

There is also a legacy v1 JSONB `workflows` seed row `key='expense'`
(`tenant/schema.sql:2127-2165`, states draft→submitted→{approved|rejected}→reimbursed,
field defs `submitted_by`/`amount`/`expense_date`/`category`/`description`). This
predates the relational module and is reused **read-only**, exactly as `requisition`
reuses its own same-named legacy row (`requisition/store.go:209-238`) — purely as the
custom-field-definition host via `workflow.GetWorkflowByKey`/`LoadDefinition`/
`ValidateCustomFieldsPartial`. Nothing about the legacy row changes.

The repo also already has a generic, tenant-wide file-attachment mechanism
(`workflow_record_attachments` + `controllers/attachments.go`), extended per module by
adding one branch to `workflow.ResolveRecordAccess` — `vendor_bill` is the existing
precedent (`workflow/attachments.go:191-210`). Receipts reuse this mechanism rather
than a bespoke table, avoiding ~500 lines of duplicate R2/presign code.

### What already exists (reuse, do not recreate)

| Concern | Existing asset | Location |
|---|---|---|
| RBAC resource | `authz.ResourceExpense` — CRUD+Transition already seeded | `authz/catalog.go:59,339-343` |
| Generic JSONB router mapping | `resourceForKey("expense")` | `controllers/crm.go:87-88` |
| Custom-field definition host | Legacy `workflows` row `key='expense'` | `tenant/schema.sql:2127-2165` |
| File attachments (receipts) | `workflow_record_attachments` + `controllers/attachments.go` | `workflow/attachments.go`, `controllers/attachments.go` |
| Direct architectural precedent | `requisition` (header + lines + employee-owner IDOR scope + AD-7 approval gate) | `requisition/` |
| Employee resolution | `resolveEmployeeID(r, identityID)` | `controllers/crm_admin.go:227` |
| Filter/sort/paginate/search | `query/` package | `query/` |
| Audit log | `audit_logs` via `workflow.LogAuditFull` | `controllers/requisition_audit.go` (pattern) |
| Row-level IDOR guard | `recordInScope` | `controllers/scope.go:29` |

### What is genuinely missing (new — justified in §3)

- `expense`, `expense_item`, `expense_history`, `expense_approver`,
  `expense_approval` — the full relational module.
- `lkp_expense_category` — new tenant-editable lookup (categories weren't modeled
  relationally anywhere; the legacy workflow's `category` field is a bare enum with no
  table behind it).
- New `lkp_record_type` row `EXPN` (id resolved by SUBSELECT, not hardcoded — see correction below) and its 5 statuses.
- `userstore.IsActiveIdentity` — small new helper; no existing mechanism blocks a
  disabled tenant user from acting (JWT claims carry no status, `authz.Check`'s grant
  query doesn't filter on it).

---

## 2. Architecture Decisions

**AD-1 — Header + line items, not a flat single-expense record.** Each line is one
dated, categorized, amount+description entry (one receipt); the header aggregates
them into one claim, submitted/approved/rejected/reimbursed as a unit. Chosen over a
flat model so one submission covers a trip's worth of receipts, matching how expense
systems are actually used.

**AD-2 — Claimant is always the acting employee (self-service), no "file on behalf
of" field.** Mirrors `requisition`'s AD-5 owner-is-requester pattern: `expense_claimant_id`
is resolved from the caller via `resolveEmployeeID`, never taken as request input.
Managers see/approve others' claims through RBAC scope (`scope=all`), not by filing on
their behalf. This also makes the IDOR scope owner trivial: `OwnerUserID` is the
claimant's linked `users.id`.

**AD-3 — No tax/discount/vendor/payment-terms fields.** A reimbursement claim isn't a
priced commitment against a counterparty; `expense_total` is a plain rounded sum of
line amounts (`expense/calc.go`'s `ComputeHeaderTotal`), simpler than `requisition`'s
qty×price+tax shape.

**AD-4 — Approval is the AD-7 configuration-driven gate, copied from `requisition/
approval.go` verbatim, plus one addition (`Reject`) the reference module doesn't
need.** `expense_approver`/`expense_approval` tables; `Approve` records sign-off and
flips `expense_approval_status` once quorum is met, but does not itself move
`expense_status` — a subsequent `POST .../transition {toStatus:"APPV"}` does that,
exactly like `requisition`. If zero approvers are configured for `SUBM`, the gate is a
no-op and a plain transition call moves the claim directly.

**AD-5 — `Reject` is a new, dedicated function, not a generic-transition target.**
`requisition` sidesteps rejection entirely (no `RJCT` state — its own transitions.go
comment says rework happens via recall/revise instead), so there's no existing
"decision that bypasses quorum" precedent to copy verbatim. Rejecting is itself the
decision, not something requiring consensus first, so `Reject(ctx, pool, uuid,
actorEmployeeID, reason)` moves `SUBM→RJCT` directly: if approvers are configured for
`SUBM`, only a configured approver may call it (`ErrNotApprover` otherwise, mirroring
`Approve`'s check); if none are configured, anyone with `expense:transition` may.
`RJCT` is deliberately excluded from `allowedTransitions["SUBM"]` so a plain
`POST .../transition {toStatus:"RJCT"}` 409s — rejection must go through
`POST .../reject` so a reason is always captured on `expense_rejection_reason`.

**AD-6 — Status lifecycle deliberately mirrors the legacy JSONB workflow's states**
(`DRFT`/`SUBM`/`APPV`/`RJCT`/`REIM`) for conceptual consistency between the two
systems, even though they don't share storage. `REIM` (reimburse) covers the request's
"process" verb — a Finance/AP action gated by plain `expense:transition`, no approval
gate (`APPV` has zero configured approvers).

**AD-7 — Receipts reuse the generic attachment mechanism, not a dedicated table.**
One new branch in `workflow.ResolveRecordAccess` (mirrors the `vendor_bill` branch
verbatim) is the only change needed; `controllers/attachments.go`'s existing presign/
upload/list/download/delete endpoints and `resourceForKey("expense")` (already
present) do the rest. Zero new R2/presign code.

**AD-8 — Three-layer inactive-user enforcement, all new ground:**
1. *Caller must be an active tenant user* — `userstore.IsActiveIdentity` (new,
   queries `users.status`), checked in `controllers/expense.go`'s `authExp` for every
   non-read action. 403 + `logSecurityEvent(r, "inactive_user_denied", ...)`.
2. *Claimant employee must be active* — `expense.employeeActive` (new, queries
   `employee_is_active` — a column that exists in schema but, per repo-wide grep, is
   read by zero application code today), checked in `Create`. 400 `ClientError`.
3. *Approver must be active* — `expense_approver.is_active`, reused verbatim from
   `requisition`'s existing `activeApproverCount`/`isConfiguredApprover` pattern; no
   new logic.

None of these layers exist as shared/generic mechanisms elsewhere in the codebase
(confirmed: JWT claims carry no status; `authz.Check`'s grant query doesn't filter on
`users.status`; `resolveEmployeeID` checks `employee_deleted_at` but never
`employee_is_active`). This module adds layer 1 as a small, narrowly-scoped addition
to `userstore` (the package that already owns `User`/`Status`) rather than modifying
the shared `resolveEmployeeID` (used by ~40 other files) or any other shared
authorization path — scoped to this module's usage only.

---

## 3. New Tables — Per-Table Justification

### `lkp_expense_category`
- **(a)** No table models expense categories relationally; the legacy workflow's
  `category` field is a bare JSONB enum with nothing behind it.
- **(b)** Tenant-editable lookup: name, code, optional GL account link, active/system
  flags, audit columns. Seeded with the legacy enum's six values for consistency.
- **(c)** New master; optional FK to `coa_account` for future GL work (unused by this
  module's logic).

### `expense` (header)
- **(a)** No table models an employee's expense claim.
- **(b)** Identity, claimant, approval status, rejection reason, department, memo,
  total, custom fields, audit columns.
- **(c)** New master (hybrid PK). FKs to `employee` (claimant/approved_by/rejected_by/
  audit), `lkp_record_type`(=EXPN), `lkp_record_status`.

### `expense_item` (lines)
- **(a)** No table models an individual dated/categorized spend entry within a claim.
- **(b)** One row per receipt: category, date, amount, description.
- **(c)** New child of `expense` (CASCADE); FK to `lkp_expense_category`.

### `expense_history`
- **(b)** One row per status change / approve / reject, mirrors `requisition_history`.
- **(c)** New child of `expense`; FKs to `lkp_record_status`, `employee`.

### `expense_approver` / `expense_approval`
- **(a)** No table models a configurable per-status approval quorum for expense
  claims.
- **(b)/(c)** Exact structural copies of `requisition_approver`/`requisition_approval`.

---

## 4. SQL — Schema Changes

> Appended to `database/migrations/tenant/schema.sql` after the Vendor Credit block
> (file end). New `lkp_record_type` row `EXPN` and 5 new `lkp_record_status` rows for
> it, resolved via `SELECT ... CROSS JOIN lkp_record_type WHERE record_type_code =
> 'EXPN'` — **not** a hardcoded id. The original draft of this spec assumed `VCRD`/17
> was the last `lkp_record_type` row (append-only, hardcoded-integer status seeding),
> but `FJOB`, `CTRF`, and `IADJ`/`ITRF`/`ICNT` were inserted after it elsewhere in the
> file, so `EXPN` actually lands at id 23 — caught by `migration-auditor` during
> review and fixed to the SUBSELECT pattern those later blocks already established
> (`tenant/schema.sql:5947`'s comment states the same reasoning explicitly). Full DDL
> implemented in the schema file; see the `-- EXPENSE MODULE` block for the
> authoritative text (table/column names match §3 exactly: `expense`, `expense_item`,
> `expense_history`, `expense_approver`, `expense_approval`, `lkp_expense_category`).

Indexes: `idx_exp_claimant`, `idx_exp_status`, `idx_exp_created_id`,
`idx_exp_custom_gin` (all partial `WHERE expense_deleted_at IS NULL` where
applicable), `idx_exp_item_expense`, `idx_exp_history_expense`.

No `authz/catalog.go` changes (`ResourceExpense` already complete) and no
`controllers/crm.go` changes (`resourceForKey("expense")` already present).

---

## 5. Status Transition Rules (record_type=EXPN)

```go
var allowedTransitions = map[string]map[string]bool{
    "DRFT": {"SUBM": true},
    "SUBM": {"APPV": true, "DRFT": true},  // RJCT excluded -- see AD-5
    "APPV": {"REIM": true},
    "RJCT": {"DRFT": true},
    "REIM": {},
}
```

- New claims start at **DRFT**.
- `DRFT → SUBM` ("Submit") — plain transition, `SUBM` typically has zero configured
  approvers so no gate applies leaving `DRFT`.
- `SUBM → APPV` ("Approve") — gated per AD-4: blocked (`ErrApprovalRequired`) until
  `expense_approval_status = 'approved'` if `SUBM` has configured approvers.
- `SUBM → RJCT` ("Reject") — **not** in this map; only reachable via the dedicated
  `Reject` function (AD-5).
- `SUBM → DRFT` ("Recall") — always allowed, no gate (withdrawing before a decision).
- `RJCT → DRFT` ("Revise") — always allowed; the employee fixes and resubmits.
- `APPV → REIM` ("Reimburse"/"process") — plain transition, `APPV` has zero configured
  approvers so no gate applies.

---

## 6. API Contracts

All under `/api/tenant/`, through `tenantChain`, RBAC-checked in-handler, IDOR-guarded
(404 on scope denial), active-user-checked (403 on inactive caller, AD-8 layer 1),
same envelope as every sibling (`{success, message?, ...}`).

| Method & path | Purpose | RBAC |
|---|---|---|
| `GET  /api/tenant/expenses` | Simple in-scope list, cursor-paginated | `expense:read` + scope |
| `POST /api/tenant/expenses/search` | Full filter + sort + search + pagination | `expense:read` + scope |
| `POST /api/tenant/expenses` | Create (claimant = caller) | `expense:create` + active-user |
| `GET  /api/tenant/expenses/{uuid}` | Get one (+ lines) | `expense:read` + IDOR |
| `PATCH /api/tenant/expenses/{uuid}` | Update (DRFT only) | `expense:update` + IDOR + active-user |
| `DELETE /api/tenant/expenses/{uuid}` | Soft delete (DRFT only) | `expense:delete` + IDOR + active-user |
| `POST /api/tenant/expenses/{uuid}/transition` | Status change (§5) | `expense:transition` + IDOR + active-user |
| `POST /api/tenant/expenses/{uuid}/approve` | Approval sign-off (AD-4) | `expense:transition` + IDOR + active-user |
| `POST /api/tenant/expenses/{uuid}/reject` | `{reason}` (AD-5) | `expense:transition` + IDOR + active-user |
| `GET  /api/tenant/expenses/{uuid}/audit` | Audit / history trail | `expense:read` + IDOR |

Receipts: existing `/api/tenant/records/{id}/attachments/*` endpoints, `{id}` = an
expense claim's UUID (AD-7) — no new routes.

**Create request**
```json
POST /api/tenant/expenses
{
  "department": "Sales",
  "memo": "Client site visit — Austin",
  "items": [
    {"lineNumber": 1, "categoryCode": "TRAVEL", "expenseDate": "2026-08-10", "amount": 412.50, "description": "Flight"},
    {"lineNumber": 2, "categoryCode": "MEALS",  "expenseDate": "2026-08-10", "amount": 38.20,  "description": "Dinner"}
  ],
  "customFields": {}
}
→ 201 { "success": true, "expense": { "id": "…", "expenseNumber": "EXPN-000001",
        "status": "Draft", "total": 450.70 } }
```

---

## 7. Listing & Query Architecture

Reuses `query/` unchanged. New: `expense.resolver` (`FieldResolver` + `SortResolver` +
`SearchResolver`), `expense.Search` (keyset), mirroring `requisition.resolver` 1:1 on
its own columns.

**FieldResolver whitelist** (alias `exp`): `id`, `document_number`, `status`,
`claimant_id`, `department`, `total`, `created_at`, `updated_at`, `cf:<key>`.

**SortResolver whitelist:** `document_number`, `total`, `status`, `created_at`,
`updated_at` — each `NOT NULL`, paired with `expense_id` tiebreaker.

**SearchPredicate:** `expense_number`, `expense_department`, `expense_memo` ILIKE,
plus an `EXISTS` sub-select on `expense_item.description` (mirrors
`requisition.resolver`'s line-item `EXISTS`).

**Response envelope:** identical to every sibling — `{success, scope, records,
nextCursor, hasMore}`. Keyset only, no offset.

---

## 8. Validation Rules

**Header**
- `items` required, ≥1 line.
- Claimant (resolved from caller) must be an active, non-deleted employee (AD-8 layer
  2) — 400 otherwise.
- `customFields` validated against the legacy `expense` workflow's field definitions
  if seeded (≤15, type/required/enum/regex).

**Lines**
- `categoryCode`/`categoryId` required, must resolve to an active
  `lkp_expense_category` row.
- `expenseDate` required.
- `amount` ≥ 0.

**Transitions** — only moves in §5's map; else 409 `ErrInvalidTransition`. `SUBM→APPV`
gated per AD-4 (`ErrApprovalRequired` if unmet).

**Approve/Reject** — caller must be a configured approver if any are configured for
the current status (`ErrNotApprover` otherwise, 403); inactive approvers never count
or qualify (AD-8 layer 3).

**Update/Delete** — only while `DRFT`; else `ClientError` (400).

**Tenant/RBAC/IDOR/Active-user** — identical to every sibling plus AD-8: `authz.Check`
before every mutation; every single-record op scope/IDOR-guarded, 404 on denial;
`idor_denied`/`permission_denied`/`inactive_user_denied` logged; scope composed into
SQL, never filtered in Go.

---

## 9. Backend Implementation Map

| Concern | Action | Reference to mirror |
|---|---|---|
| RBAC | None — `ResourceExpense` already complete | — |
| Schema | Append to `database/migrations/tenant/schema.sql`: `EXPN` record type + statuses, `lkp_expense_category`, `expense`/`_item`/`_history`/`_approver`/`_approval` + indexes | §4 above, `requisition`'s block |
| Active-user helper | `userstore.IsActiveIdentity` | new, small addition to `userstore/store.go` |
| Expense package | New `expense/`: `types.go`, `calc.go`, `numbering.go`, `transitions.go`, `resolver.go`, `approval.go` (Approve + Reject), `store.go`, `store_create.go`, `store_get.go`, `store_update.go`, `store_search.go`, `store_transition.go` | `requisition/*` |
| Expense controller | New `controllers/expense.go`, `controllers/expense_audit.go` | `controllers/requisition.go`/`requisition_audit.go`, auth skeleton from `controllers/payment.go` |
| Attachment wiring | One new branch in `workflow.ResolveRecordAccess` | `vendor_bill` branch, `workflow/attachments.go:191-210` |
| Route registration | `main.go`, new block alongside Requisition/Vendor Credit | `main.go` requisition block |
| Tests | Table-driven for `calc`/`numbering`/`transitions`/`resolver`; `dbtest`-tagged `store_test.go` | `requisition/*_test.go`, `payment/store_test.go` |
| Review | **module-drift-checker**, **tenancy-security-reviewer**, **migration-auditor** before calling this done | — |

---

## 10. Open Decisions — Resolved During Brainstorming

1. **Header + lines, not a flat record** — user-selected, matches how multi-receipt
   trips are actually claimed.
2. **New `lkp_expense_category` lookup, not COA-direct or a bare Go enum** —
   user-selected; tenant-editable, with an optional (unused-for-now) COA link.
3. **Receipts reuse the generic attachment mechanism, not a dedicated table** —
   user-selected after research showed `vendor_bill`'s precedent made a bespoke table
   pure duplication.
4. **All three inactive-user enforcement layers** — user-selected; none existed as
   shared mechanisms, so all three are new, narrowly-scoped additions.
5. **`Reject` is a dedicated function, not folded into generic `Transition`** — the
   reference module (`requisition`) has no reject state to copy from; a dedicated
   function is required so a reason is always captured and quorum isn't required to
   reject.
