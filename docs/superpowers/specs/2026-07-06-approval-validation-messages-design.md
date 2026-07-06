# Approval Validation Messages — Design

**Date:** 2026-07-06
**Scope:** Backend only (`stonesuite-backend` repo). Frontend (Approve button visibility, toast/dialog rendering) lives in the separate `StoneSuite` repo and is out of scope here.
**Feature scope:** CRM `customer` records only — the existing `crm_workflow_approver` approval feature (DesignV2 relational store). DesignV1 (`workflow_store.go`) has no approval concept and is unaffected beyond a stub `IsApprover` implementation.

## Background

StoneSuite already has a working approval feature for CRM customer records:

- `crm_workflow_approver` table configures which employee(s) may approve records of a given record type (optionally scoped to a specific CRM status).
- `customer.customer_approval_status` is `none | pending | approved`; a customer enters `pending` when transitioned to Closed-Won.
- `crmstore.Store.Approve()` ([relational_store.go:659](../../../crmstore/relational_store.go)) enforces "record is pending" and "caller is a configured approver," returning `ErrNotApprover` or a `ClientError` otherwise.
- `POST /api/tenant/crm/records/{id}/approve` ([crm.go:501](../../../controllers/crm.go)) calls it, mapping errors via `crmFail`.

This design tightens the validation branching and message text to match five explicit scenarios, and adds one new field (`canApprove`) so the frontend can decide whether to render the Approve button.

## Scenarios & Behavior

| # | Scenario | Condition | Response |
|---|---|---|---|
| 1 | Unauthorized user attempts to approve | Record is `pending`, caller holds `customer:update` permission but is not in `crm_workflow_approver` for this record | 403, `"You are not authorized to approve this document. Only the assigned approver(s) can approve it."` |
| 2 | Non-approver views the document | Caller has read access but is not a configured approver | `GET` response includes `"approvalStatus": "pending"` (existing field, renamed in JSON — see below) and `"canApprove": false`. No behavior change beyond exposing the flag; frontend renders "Pending Approval" and hides the Approve button. |
| 3 | Assigned approver views/approves | Caller is a configured approver, record is `pending` | `GET` response includes `"canApprove": true`. `POST .../approve` succeeds, returns updated record. |
| 4 | Already approved | `customer_approval_status == "approved"`, someone calls approve again | 409, `"This document has already been approved."` |
| 5 | No approver configured | `customer_approval_status == "pending"`, zero active rows in `crm_workflow_approver` for this record type (regardless of caller) | 409, `"No approver is configured for this workflow. Please contact your administrator."` |

Note: a record that hasn't reached `pending` yet (`approval_status == "none"`) keeps the existing, unchanged message `"This record is not pending approval."` (400) — not one of the five named scenarios, no reason to change it.

Permission gating stays as-is: `POST .../approve` still requires `customer:update` (`authCRMByRecordID`) before reaching approval-specific logic. A caller entirely lacking that permission gets the existing generic 403 ("You do not have permission to update customer.") — that's a distinct, pre-existing RBAC concern, not the approver-specific message in scenario 1.

## Components

### 1. Pure decision function — `crmstore/relational_store.go`

```go
func approvalDecision(status string, anyApproverConfigured, callerIsApprover bool) error
```

Called by `Approve()` after its DB lookups. Returns:
- `status == "approved"` → `ErrAlreadyApproved`
- `status == "pending"` && `!anyApproverConfigured` → `ErrNoApproverConfigured`
- `status == "pending"` && `!callerIsApprover` → `ErrNotApprover`
- `status == "pending"` && `callerIsApprover` → `nil` (proceed)
- else (`status == "none"`) → existing `ClientError{Msg: "This record is not pending approval."}`

Pure, no DB — table-driven `testify` tests cover every branch directly.

### 2. Approver-configuration checks — `crmstore/relational_store.go`

`Approve()`'s existing single query (which already joins caller + record + config) splits into two small queries via one shared helper:
- `hasAnyActiveApprover(ctx, pool, recordTypeCode string) (bool, error)` — any active row for CUST, any status.
- `isConfiguredApprover(ctx, pool, id, empID string) (bool, error)` — existing per-caller check, extracted from current inline query.

`Approve()` calls both, then `approvalDecision(...)`, then proceeds with the UPDATE only on `nil`.

### 3. New Store method — `IsApprover`

Added to `crmstore.Store`:
```go
// IsApprover reports whether identityID is a configured approver for record id.
// Used by GET handlers to expose canApprove without mutating anything.
IsApprover(ctx context.Context, pool *pgxpool.Pool, id, identityID string) (bool, error)
```
- `relationalStore.IsApprover` — resolves the caller's employee id, then reuses `isConfiguredApprover`. Returns `false, nil` if the record isn't a customer record (mirrors `Approve`'s own-workflow guard) or if the caller has no employee record.
- `workflowStore.IsApprover` (DesignV1) — returns `false, nil` (unsupported, not an error — this is a read-only informational flag, not an action).

### 4. Sentinel errors — `crmstore/store.go`

```go
ErrAlreadyApproved      = errors.New("this document has already been approved")
ErrNoApproverConfigured = errors.New("no approver is configured for this workflow")
```
`ErrNotApprover`'s message text is updated to `"you are not authorized to approve this document. Only the assigned approver(s) can approve it."` (message strings are lowercase-first per Go convention for `errors.New`; `crmFail` uses `err.Error()` directly today so the leading-lowercase form will render as-is — matches existing codebase behavior for other sentinels).

`IsClientError` is unaffected (already excludes these via explicit `errors.Is` cases in `crmFail`, not the generic 400 fallback).

### 5. HTTP mapping — `controllers/crm.go`

`crmFail` gains two cases:
```go
case errors.Is(err, crmstore.ErrAlreadyApproved):
    fail(w, http.StatusConflict, err.Error())
case errors.Is(err, crmstore.ErrNoApproverConfigured):
    fail(w, http.StatusConflict, err.Error())
```
placed before the existing `ErrNotApprover` (403) and `IsClientError` (400) cases.

### 6. Security logging — `controllers/crm.go`

`ApproveRecord` logs on denial, matching the CLAUDE.md rule that permission denials go through `logSecurityEvent`:
```go
if errors.Is(err, crmstore.ErrNotApprover) {
    logSecurityEvent(r, "approval_denied", "identity", identityID, "record", id)
}
```
placed just before the existing `crmFail(w, err, ...)` call in `ApproveRecord`.

### 7. `canApprove` field — `controllers/crm.go` `GetRecord`

After the existing `st.GetRecord` call, when `key == "customer"` (the `key` already returned by `authCRMByRecordID`, currently discarded), call `st.IsApprover(ctx, pool, id, identityID)` and add it to the response:
```go
resp := map[string]any{"success": true, "record": rec}
if key == "customer" {
    canApprove, err := st.IsApprover(r.Context(), pool, id, identityID)
    if err != nil {
        fail(w, http.StatusInternalServerError, "Failed to load record.")
        return
    }
    resp["canApprove"] = canApprove
}
writeJSON(w, http.StatusOK, resp)
```
Only added to the single-record `GET`, not list/search endpoints (avoids N+1 queries; the Approve button is only relevant on the detail view).

## Data Flow (approve attempt)

```
POST /api/tenant/crm/records/{id}/approve
  → authCRMByRecordID (customer:update permission + IDOR scope check)   [existing, unchanged]
  → st.Approve(ctx, pool, id, identityID)
      → GetRecord(id)                         → 404 if missing          [existing]
      → guard: record.WorkflowID == "customer" → 400 otherwise          [existing, unchanged]
      → hasAnyActiveApprover(CUST)             → anyApproverConfigured
      → isConfiguredApprover(id, callerEmpID)  → callerIsApprover
      → approvalDecision(status, anyApproverConfigured, callerIsApprover)
          → error   → return to controller → crmFail → logSecurityEvent (if ErrNotApprover)
          → nil     → UPDATE customer SET approved... → writeHistory → return updated record
  → controller writes 200 + record, or mapped error status + message
```

## Error Handling Summary

| Error | HTTP | Message |
|---|---|---|
| `ErrRecordNotFound` | 404 | `"Record not found."` (unchanged) |
| `ErrAlreadyApproved` | 409 | `"This document has already been approved."` |
| `ErrNoApproverConfigured` | 409 | `"No approver is configured for this workflow. Please contact your administrator."` |
| `ErrNotApprover` | 403 | `"You are not authorized to approve this document. Only the assigned approver(s) can approve it."` |
| `ClientError` (not pending / not a customer record) | 400 | unchanged existing text |

All responses keep the existing `{"success": false, "message": "..."}` envelope via `fail()`.

## Testing

- **Unit (pure, no DB):** table-driven `testify` tests for `approvalDecision` in `crmstore/relational_store_test.go`, covering: pending+configured+caller-is-approver (nil), pending+configured+caller-not-approver (`ErrNotApprover`), pending+no-approver-configured (`ErrNoApproverConfigured`), approved (`ErrAlreadyApproved`), none (`ClientError`).
- **Existing tests must stay green** — no changes to `Approve()`'s externally observable behavior for the currently-passing paths (customer-only guard, forward-only rules, etc.).
- No integration/DB tests are added for `hasAnyActiveApprover` / `isConfiguredApprover` / `IsApprover` themselves, consistent with the existing lack of a DB test harness in `crmstore` — these are thin query wrappers, same pattern as the rest of the file.

## Out of Scope

- Any change to `workflow_records` (DesignV1) — approval remains customer-only.
- Frontend changes (separate repo).
- Blocking the Closed-Won transition itself when no approver is configured (scenario 5 fires only on approve-attempt, per decision above).
- New RBAC permission/action for "approve" specifically — reuses existing `customer:update`.
