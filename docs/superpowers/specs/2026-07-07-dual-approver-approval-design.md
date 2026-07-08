# Dual-Approver Approval — Design

**Date:** 2026-07-07
**Scope:** Backend only (`stonesuite-backend` repo). Frontend (approver-count admin UI, pending-approvals inbox) lives in the separate `StoneSuite` repo and is out of scope here.
**Feature scope:** CRM `customer` records only — extends the existing `crm_workflow_approver` approval feature (DesignV2 relational store) finalized in [2026-07-06-approval-validation-messages-design.md](2026-07-06-approval-validation-messages-design.md). DesignV1 (`workflow_store.go`) has no approval concept and is unaffected.

## Background

The existing feature supports exactly one configured approver: whoever approves first immediately finalizes the record (`customer_approval_status: pending -> approved`). This design extends it so an admin can configure **up to 2** active approvers for a record type/status, and — when 2 are configured — **both** must approve before the record finalizes. The required-approval count is *derived* from how many approvers are actively configured, not stored separately, so:

- 1 approver configured -> unchanged existing behavior (single approval finalizes).
- 2 approvers configured -> both must approve; after the first, the record stays `pending`.

## Scenarios & Behavior

| # | Scenario | Condition | Response |
|---|---|---|---|
| 1 | Non-approver attempts to approve | Caller holds `customer:update` but is not a configured approver | 403, existing `ErrNotApprover` message (unchanged) |
| 2 | First of two approvers approves | Record `pending`, 2 active approvers configured, caller is one of them, hasn't approved yet | 200, record returned with `customer_approval_status: "pending"` still (not finalized) |
| 3 | Second of two approvers approves | Record `pending`, caller is the other configured approver, hasn't approved yet | 200, record returned with `customer_approval_status: "approved"` (finalized) |
| 4 | Approver tries to approve again | Caller already has a `customer_approval` row for this record, record still `pending` (other approver hasn't gone yet) | 409, new message: "You have already approved this document. Waiting on the other assigned approver." |
| 5 | Already fully approved | `customer_approval_status == "approved"` | 409, existing `ErrAlreadyApproved` message (unchanged) |
| 6 | No approver configured | Zero active approvers for this record type | 409, existing `ErrNoApproverConfigured` message (unchanged) |
| 7 | Admin configures a 3rd approver | 2 active approvers already configured for the record-type/status combo | 409, new message: "Maximum of 2 approvers can be configured for this workflow." |
| 8 | Approver checks their queue | Caller is a configured approver on N pending customer records, has already approved some of them | `GET /api/tenant/crm/approvals/pending` excludes records the caller already approved, includes the rest |

## Components

### 1. Schema — `customer_approval` table (new tenant migration)

```sql
CREATE TABLE IF NOT EXISTS customer_approval (
    customer_approval_id    SERIAL PRIMARY KEY,
    customer_id             INTEGER NOT NULL REFERENCES customer(customer_id),
    approver_employee_id    INTEGER NOT NULL REFERENCES employee(employee_id),
    approved_at             TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
    CONSTRAINT uq_customer_approval UNIQUE (customer_id, approver_employee_id)
);
CREATE INDEX IF NOT EXISTS idx_customer_approval_customer ON customer_approval (customer_id);
```

One row per (record, approver). The `UNIQUE` constraint is the DB-level backstop against double-approval by the same person (app-level check is the primary guard; the constraint catches races). `customer.customer_approval_status` / `customer_is_approved` / `customer_approved_by` / `customer_approved_at` remain the **summary** columns, written only once the record is fully approved (last approver's id/timestamp) — unchanged shape for existing readers.

### 2. `Approve()` — `crmstore/relational_store.go`

Extends the existing method (crmstore/relational_store.go:680):

1. Existing guards unchanged: record is `customer`, status is `pending`, caller resolves to an employee, caller is a configured active approver (`ErrNotApprover` otherwise).
2. **New:** if caller already has a `customer_approval` row for this record -> `ErrAlreadyApprovedByYou`.
3. `INSERT INTO customer_approval (customer_id, approver_employee_id) VALUES (...) ON CONFLICT DO NOTHING` (constraint backstop for a concurrent double-submit).
4. `requiredApprovals` := `COUNT(DISTINCT approver_employee_id)` from `crm_workflow_approver` matching this record's type + status (same wildcard-or-exact predicate as today's `isConfiguredApprover`).
5. `approvalsSoFar` := `COUNT(*) FROM customer_approval WHERE customer_id = $1`.
6. If `approvalsSoFar >= requiredApprovals` -> finalize (existing UPDATE: `customer_is_approved=TRUE, customer_approval_status='approved', customer_approved_by=$empID, customer_approved_at=NOW()`, version bump).
7. Else -> leave the customer row's approval columns untouched (stays `pending`).
8. `writeHistory(ctx, pool, id, "approve", empID)` runs in both branches — both approvals are audit-visible.

One code path handles both the 1-approver and 2-approver cases; there's no `if requiredApprovals == 1` branch.

### 3. `IsApprover` (`canApprove` flag) — `crmstore/relational_store.go`

Adds one more condition to the existing check: caller must **not** already have a `customer_approval` row for this record. So after approver A approves, their own `GET` shows `canApprove: false`; approver B's `GET` still shows `canApprove: true` (record is still `pending`).

### 4. New endpoint — `GET /api/tenant/crm/approvals/pending`

New `Store` method:
```go
// PendingApprovals lists pending customer records where actorIdentityID is a
// configured active approver who has not yet approved. DesignV1 returns an
// empty slice (approval unsupported).
PendingApprovals(ctx context.Context, pool *pgxpool.Pool, actorIdentityID string) ([]workflow.Record, error)
```
Query: customer rows where `customer_approval_status = 'pending'`, caller's `empID` matches an active `crm_workflow_approver` row for the record's type/status, and no `customer_approval` row exists yet for `(customer_id, empID)`. Registered on `CRMOps` (same handler group as other CRM record endpoints), requires `customer:read`. Not routed through the generic `query` filter engine — this is a fixed, non-user-configurable filter (approver-relevant pending records only), consistent with how `hasAnyActiveApprover`/`isConfiguredApprover` are already thin dedicated queries rather than going through `query.FieldResolver`.

### 5. Approver cap — `controllers/crm_admin.go` `CreateApprover`

Before inserting, compute the same "effective active approver count" for the target record-type/status (mirrors step 4 of `Approve()`) and reject with `ErrTooManyApprovers` (409) if it's already 2.

### 6. Sentinel errors — `crmstore/store.go`

```go
ErrAlreadyApprovedByYou = errors.New("you have already approved this document. Waiting on the other assigned approver.")
ErrTooManyApprovers     = errors.New("maximum of 2 approvers can be configured for this workflow.")
```

### 7. HTTP mapping — `controllers/crm.go` / `controllers/crm_admin.go`

`crmFail` gains:
```go
case errors.Is(err, crmstore.ErrAlreadyApprovedByYou):
    fail(w, http.StatusConflict, err.Error())
```
`CreateApprover` maps `ErrTooManyApprovers` to `409` directly (it doesn't go through `crmFail`, which is CRM-record-specific).

## Error Handling Summary (new additions only — existing table from the 2026-07-06 design is unchanged)

| Error | HTTP | Message |
|---|---|---|
| `ErrAlreadyApprovedByYou` | 409 | "You have already approved this document. Waiting on the other assigned approver." |
| `ErrTooManyApprovers` | 409 | "Maximum of 2 approvers can be configured for this workflow." |

## Testing

- Table-driven `testify` tests extending `crmstore/relational_store_test.go`'s existing `approvalDecision` coverage with the new required-vs-so-far branch (e.g., `approvalsSoFar < requiredApprovals -> nil, stays pending` vs `approvalsSoFar >= requiredApprovals -> finalize`) and the "already approved by you" branch, kept pure/DB-free like the existing tests.
- Existing tests (single-approver paths) must stay green — 1-approver-configured behavior is unchanged.

## Out of Scope

- Any change to `workflow_records` (DesignV1) — approval remains customer-only.
- Frontend changes (separate repo): admin UI capping the approver picker at 2, "My Pending Approvals" inbox screen, "waiting on other approver" state display.
- Configurable required-count beyond "number of active approvers, capped at 2" — no separate threshold setting.
- Rejection/decline flow — only approval is in scope, matching the original requirements.
- Re-evaluating already-partial approvals when an approver config is deleted mid-flight (e.g., 2 configured, 1 approved, admin removes the other approver) — the finalization check only runs on the next `Approve()` call, not reactively on config changes.
