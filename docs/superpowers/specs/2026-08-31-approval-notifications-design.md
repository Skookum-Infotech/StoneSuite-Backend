# In-App Approval Notifications — Design Spec

**Date:** 2026-08-31 · **Status:** Approved; ready for implementation
**Repo affected:** `StoneSuite-Backend` only. `stonesuite-notify` needs no changes — its in-app
bell, preferences, and delivery reliability layer already support everything this needs
(verified against `develop`: `POST /api/notifications/internal` always writes an in-app row
for any recipient with a `UserID`, independent of the `Channels` field, which only gates
whether email is additionally attempted).

## 1. Problem

Every relational document module (quote, estimate, salesorder, invoice, payment, creditmemo,
refund) has an approval-gate workflow — a record enters a gated status, routes to configured
approvers, and either advances or gets sent back — but none of it raises any notification
today, in-app or email. Approvers don't know something is waiting on them; owners don't know
their record cleared or got sent back.

`vendors` (the vendor-directory module) has no approval workflow at all (`ACT_`/`ONHD`/`INA_`
only, no approver table) and is out of scope — there is nothing to hook.

## 2. Current state (verified against `develop`)

- `stonesuite-notify`'s in-app row is created for every recipient with a `UserID`, gated only
  by that recipient's own `InAppEnabled` preference (default on) — **not** by the caller's
  `Channels` field, which solely controls whether email is attempted. This is why the one
  existing real-user notification (`notifyOwnerOfSend`, document-send owner ping) already shows
  in the bell today even though it passes `Channels: []string{"email"}`.
- 4 of the 7 in-scope modules (invoice, payment, creditmemo, refund) do not have their own
  `Approve` logic — they're thin wrappers around the shared `approvalchain.Approve`/`finalize`
  engine (`approvalchain/engine.go`). Only quote, estimate, and salesorder still carry their own
  copy-pasted `approval.go` (pre-dates the engine, per that package's doc comment).
- No module's `Transition` is centralized — even invoice hand-rolls its own
  (`invoice/store_transition.go`), calling `approvalchain.CheckTransitionGate` /
  `approvalchain.ActiveApproverCount` but doing its own SQL and commit.
- Every in-scope table already has a uniform `<table>_owner_id` (employee FK) and
  `<table>_number` column (verified in `schema.sql`), matching each module's existing
  `OwnerUserID` field (used today for the IDOR scope check).
- `approvalchain.registry` also lists modules outside this spec's scope (purchase_order,
  requisition, vendor_bill, vendor_payment, expense, installation, vendor_credit) whose
  `engine.Approve` calls must not change behavior.

## 3. Events and hook points

| Event | Hook point | Recipients |
|---|---|---|
| Submitted for approval | Each module's own `Transition`, when the move lands in a status with configured approvers (`targetApprovers > 0`) | All active approvers configured for that status |
| Approved | `Approve`/`finalize` (shared engine for invoice/payment/creditmemo/refund; hand-ported into quote/estimate/salesorder's own `approval.go`), once quorum is met or a super-admin overrides | The record's owner |
| Rejected / sent back | Each module's own `Transition`, when a record sitting in a gated-unapproved status moves to `VOID`/`CANC`/`RJCT` — the existing `approvalchain.AlwaysAllowedExitCodes`, i.e. escaping the gate rather than clearing it | The record's owner |
| Partial sign-off reminder | `Approve`, after recording one sign-off, when quorum still isn't met | Remaining (not-yet-signed) active approvers |

Because `Transition` isn't centralized, the "submitted"/"rejected" hooks are hand-ported into
all 7 modules regardless. The "approved"/"reminder" hooks land in exactly one place
(`approvalchain/engine.go`) for invoice/payment/creditmemo/refund, and are hand-ported into
quote/estimate/salesorder's own `approval.go` (3 more call sites) — 3 hand-ports instead of 7,
consistent with the repo's clone-discipline rule (no shared base, but shared *pure* helpers are
fine, same precedent as `invoice/store_line_resolve.go`).

## 4. Design

### 4.1 `approvalchain` package additions

- `RecordSpec` gains `OwnerColumn` (e.g. `"quote_owner_id"`) and `NumberColumn` (e.g.
  `"quote_number"`).
- `ModuleConfig` gains `DisplayName` (human label for notification text, e.g. `"Invoice"`).
- These three fields are populated **only** for the 4 engine-based in-scope entries (invoice,
  payment, credit_memo, refund) in `registry.go`. Every other registry entry (purchase_order,
  requisition, vendor_bill, vendor_payment, expense, installation, vendor_credit) keeps them at
  the zero value.
- New file `approvalchain/notify.go` holds the recipient-resolution and notification-sending
  helpers (`NotifyApprovalRequested`, `NotifyApproved`, `NotifyApprovalRejected`,
  `NotifyRemainingApprovers`), each taking primitive parameters (table/column names, ids,
  strings) rather than a `ModuleConfig` — this lets quote/estimate/salesorder's hand-rolled code
  call the exact same functions the engine uses, without needing a registry entry shaped for the
  engine.
- Every one of these functions **no-ops immediately if `displayName == ""`** — this is the guard
  that keeps `engine.Approve` behavior-identical for every out-of-scope registered module; the
  engine hook always passes `cfg.DisplayName`, so an empty one (the zero value for every
  unconfigured entry) skips notification entirely, before any query runs.
- Recipient resolution mirrors the existing `GetInfo`/`GetApprovalInfo` join
  (`<approver_table> → employee → users`) but additionally selects `u.email`; owner resolution
  is the same join keyed by `OwnerColumn` instead. Both are best-effort: a resolution failure is
  logged and the notification is skipped, never propagated as an error from `Transition`/
  `Approve` — sending a notification is not part of either function's contract, matching
  `notifyOwnerOfSend`'s existing failure semantics.
- Tenant id comes from `tenancy.TenantFromContext(ctx)` inside the notify helpers themselves —
  **no signature change** to any `Transition`/`Approve`/`finalize` function or their call sites.
  This is a new (but unproblematic) dependency direction: `approvalchain` already sits below
  `controllers`, and `tenancy` is a foundational, dependency-free package with no risk of an
  import cycle.
- Actor attribution (`NotificationRequest.ActorUserID`) is resolved best-effort from
  `actorEmployeeID` via the same `employee → users` join; left empty if unresolvable.

### 4.2 Hook wiring

- `approvalchain.Approve`/`finalize`: after a successful `tx.Commit()`,
  - if `ApproveOutcome.Finalized` → `NotifyApproved` to the owner (resolved via
    `cfg.Record.OwnerColumn`).
  - else (sign-off recorded but quorum not met) → `NotifyRemainingApprovers`.
- Each of the 7 modules' own `Transition` (quote, estimate, salesorder, invoice, payment,
  creditmemo, refund): after a successful `tx.Commit()`,
  - if `targetApprovers > 0` (entering a gate) → `NotifyApprovalRequested`.
  - if `requiredHere > 0 && approvalStatus != approved && toStatusCode` is in
    `AlwaysAllowedExitCodes` (escaping a gated-unapproved status) → `NotifyApprovalRejected`.
- quote/estimate/salesorder's own `approval.go` `Approve`/`finalize*` functions get the same two
  hooks as `engine.finalize`, hand-ported with their literal table/column names (they don't have
  a `ModuleConfig` to read from).

### 4.3 Notification content

Every call is a `services.SendNotification` — same wire shape and pattern as the existing
`notifyOwnerOfSend`:

| Field | Value |
|---|---|
| `EventType` | `"<resource>.approval_requested"` / `"<resource>.approved"` / `"<resource>.approval_rejected"` (the reminder reuses `approval_requested` — same ask, different trigger) |
| `Resource` | the module's registry/workflow key (`"quote"`, `"invoice"`, …) |
| `ResourceID` | the record's UUID |
| `Recipients` | one `{UserID, Email}` per resolved contact |
| `Title` | e.g. `"Quote Q-000123 needs your approval"` / `"Invoice INV-000045 was approved"` / `"Refund RFND-000012 was sent back"` |
| `Body` | short fixed string (`"Submitted for approval."` / `"Approved."` / `"Sent back for changes."`), mirroring `documents.go`'s `Body: "Document sent."` convention |
| `Channels` | `["email"]` — the in-app row is created regardless (§2); this only decides whether email is additionally attempted, per each recipient's own preference |

## 5. Out of scope

- `vendors` module (no approval workflow to hook into).
- Any change to `stonesuite-notify` (already supports everything this needs).
- The other `approvalchain` registry entries (purchase_order, requisition, vendor_bill,
  vendor_payment, expense, installation, vendor_credit) — deliberately left unconfigured
  (§4.1's guard), a fast follow if wanted later.
- A frontend delivery-status or approval-notification UI beyond the existing bell — same
  no-frontend-surface precedent as the two prior Notify migration specs.

## 6. Testing

- Table-driven tests for the new pure/near-pure helpers in `approvalchain/notify.go`
  (recipient-resolution SQL shape, the `displayName == ""` no-op guard, event/title/body
  construction).
- `dbtest`-tagged tests asserting the right `SendNotification` call fires on transition/approve,
  for at least one engine-based module (invoice) and one hand-rolled module (quote) — mirroring
  `document_send_dbtest_test.go`'s existing coverage of `notifyOwnerOfSend`.
- After implementation: `module-drift-checker` per touched module, `tenancy-security-reviewer`
  once across the branch.

## 7. Addendum (2026-09-01): Record Created event

A 5th event was added, extending §3's table:

| Event | Hook point | Recipients |
|---|---|---|
| Created | Each module's own `Create()`, after a successful `tx.Commit()` | The actor who performed the create (not necessarily the record's owner — they can diverge via an explicit owner override on create) |

Unlike the four events above, Created fires for **all 14** relational document modules
(estimate, quote, salesorder, invoice, payment, creditmemo, refund, purchaseorder,
requisition, vendorbill, vendorpayment, vendorcredit, expense, fabrication) — every module has
a `Create()`, so there's no equivalent to the "7 in scope, others excluded" split the original
four events used.

`approvalchain/notify.go` gained `NotifyCreated(ctx, pool, EventContext)`, using the same
`EventContext` struct and the same `DisplayName == ""` no-op guard as every other `Notify*`
function — no new fields needed. It also gained `actorContact`, a sibling to the existing
`ownerContact`/`resolveActorUserID`: it resolves `ActorEmployeeID` to a full `{UserID, Email}`
contact (not just a bare user id) since the actor is the *recipient* here, not just
notification metadata. `ec.OwnerColumn` is left unset at every `notifyCreated` call site — it's
simply unused by this event.

Wiring mirrors `notifyTransition`'s existing hand-port pattern exactly: a one-line
`notifyCreated(...)` call right after each module's `tx.Commit()` succeeds, before its final
`return Get(...)`. The three pre-engine modules (estimate/quote/salesorder) get a `notifyCreated`
function in their own local `notify.go`; the other 11 get one in `store_transition.go` (where
their existing `notifyTransition` already lives), reading `cfg := moduleConfig()` the same way.
`payment` and `vendorpayment` are the only two modules where the hook runs before their inline
post-commit `Applications` loop rather than immediately before `return Get(...)` — the record is
already committed and real at that point, so "created" firing regardless of that loop's outcome
is correct, not a shortcut.
