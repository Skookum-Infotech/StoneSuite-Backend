# Payments Module — Implementation & Review

**Date:** 2026-07-13
**Branch:** `feat/payments-module` (off `develop` @ `9f4109c`), implemented in worktree `worktree-payments-module`
**Scope of this pass:** design + implement + review the Payments module (new feature, subagent-driven implementation task-by-task per `docs/superpowers/plans/2026-07-13-payments-module.md`).

---

## 1. What was built

A new `payment/` package (sibling of `invoice/`) plus HTTP handlers, formalizing the AR-balance design the Invoice module's own AD-5 deliberately deferred. `payment_application` (a new payment↔invoice ledger table) becomes the source of truth for `invoice.invoice_amount_paid`/`invoice_balance_due`; the invoice header is now a derived rollup, recomputed transactionally on every apply/unapply. The prior direct-write `invoice.RecordPayment` function is retired; the legacy `POST /invoices/{uuid}/payment` endpoint is preserved at the same path/shape as a thin wrapper (`payment.QuickPay`) over the new module.

**New tables** (`database/migrations/tenant/schema.sql`): `lkp_payment_method`, `payment`, `payment_application`, `payment_history` — reusing the already-seeded `PYMT` record type (`record_type_id=8`) and its `PEND/APPV/DEPO/VOID` status lifecycle.

**New package `payment/`:** `types.go`, `money.go`, `transitions.go`, `numbering.go`, `store.go` (Get + scanning), `store_create.go` (Create + inline applications), `apply.go` (Apply/Unapply — the novel AR-recompute core), `store_update.go` (Update/SoftDelete), `store_transition.go` (Transition + VOID cascade), `quickpay.go`, `resolver.go`, `search.go`.

**New/modified controllers:** `controllers/payment.go`, `payment_transition.go`, `payment_audit.go` (new); `controllers/invoice_payments.go` (new — AR reconciliation view on the invoice side); `controllers/invoice_transition.go`, `controllers/invoice.go` (modified — legacy endpoint rewired, `invoiceFail` extended); `main.go` (11 new routes registered).

**Retired:** `invoice.RecordPayment`, `invoice.payableStatuses`, and their two superseded tests (`TestRecordPayment`, `TestRecordPayment_RejectedBeforeSent`) — replaced by `payment.QuickPay`'s own coverage.

### Verification
- `go build ./...`, `go vet ./...`, `go test ./...` all green (fresh, uncached).
- `go vet -tags dbtest ./...` and `go test ./payment/... ./invoice/... -tags dbtest` compile clean; every dbtest-tagged integration test correctly reports `SKIP` (not silently passing, not failing) since `TEST_DATABASE_URL` is unset in this environment — no live Postgres was available for this implementation pass. **A live-DB run of the full dbtest suite is recommended before this ships to an environment with real traffic.**
- 24 commits on the branch, each independently reviewed (spec compliance + code quality) before the next task started, plus a final whole-branch pass (below).

---

## 2. Architecture

- **Hybrid PK, database-per-tenant, no `tenant_id` column** — identical convention to `invoice`/`sales_order`.
- **`payment` is a sibling of `invoice`, connected only through `payment_application`.** A payment belongs to a customer, not to any single invoice; it may fund zero, one, or several invoices over its lifetime (one payment, many invoices, with unapplied balance — the allocation model chosen during brainstorming).
- **AR is now derived, not stored-independently.** `payment.payment_applied_total`/`unapplied_amount` and `invoice.invoice_amount_paid`/`balance_due` are both stored rollups, recomputed transactionally from live `payment_application` rows on every `Apply`/`Unapply`/VOID-cascade — never written directly by any other code path after this branch.
- **Status lifecycle:** `PEND → APPV → DEPO`, or `VOID` from either `PEND`/`APPV`. Applying/unapplying money is decoupled from this approval lifecycle — only `VOID` blocks application; `PEND`/`APPV`/`DEPO` payments can all apply.
- **`deriveInvoiceStatus` deliberately bypasses `invoice.CanTransition`.** That map treats `PAID` as terminal (no way back out) and rejects `PART→SENT` — moves an `Unapply` legitimately needs. This module derives invoice status purely from the recomputed balance and writes it directly, the same way `invoice.RecordPayment` already bypassed the general transition map for its own forward-only auto-transitions.
- **Lock ordering is fixed: payment before invoice**, everywhere both are locked in one transaction (`Apply`, `Unapply`, the VOID cascade) — prevents a lock-order deadlock between concurrent calls. The VOID cascade additionally locks its invoices in `ORDER BY invoice_id` (fixed during implementation) to prevent a cross-payment AB/BA deadlock when two different payments both touch the same two invoices.
- **Security chain** (all `/api/tenant/payments...` + `/api/tenant/invoices/{uuid}/payments`): `tenantChain` → `authz.Check(ResourcePayment, action)` → scope filter on list/search → `recordInScope` IDOR guard (404 on deny) on single-record ops → security-event logging. `Apply`/`Unapply`/inline-`Create`-with-applications additionally require `invoice:update` + IDOR on the **target invoice**, since those mutate an invoice's AR balance as a side effect of a payment-side action — a caller who can edit their own payment must not be able to move money onto an invoice outside their scope.
- **Shared filter engine** — `payment/resolver.go` + `payment/search.go` reuse `query/` unchanged, mirroring `invoice/resolver.go`/`search.go` exactly.

### API surface (11 endpoints)
`GET /payments` (read) · `POST /payments/search` (read) · `POST /payments` (create, + `invoice:update` IDOR per inline application) · `GET|PATCH|DELETE /payments/{uuid}` (read/update/delete) · `POST /payments/{uuid}/transition` (transition) · `POST /payments/{uuid}/apply` / `.../unapply` (update, dual-resource IDOR) · `GET /payments/{uuid}/audit` (read) · `GET /invoices/{uuid}/payments` (read, invoice-side AR view) · `POST /invoices/{uuid}/payment` (unchanged path, now wraps `QuickPay`).

---

## 3. Consolidated review findings

Every task (24 commits) was independently reviewed for spec compliance + code quality before the next task started (using `tenancy-security-reviewer`, `filter-invariant-checker`, and general-purpose reviewers as appropriate per file). After all tasks landed, five review agents ran a **final whole-branch pass** looking specifically for cross-task interactions no single-task review could see: **migration-auditor**, **tenancy-security-reviewer**, **filter-invariant-checker**, a general-correctness reviewer, and a simplification/cleanup reviewer.

### Fixed during this pass

| # | Sev | Location | Finding | Fix (commit) |
|---|-----|----------|---------|---------------|
| C1 | **Critical** | `controllers/payment.go` `Create` | The final whole-branch security review found that `Create`'s optional inline `applications[]` were applied via the store's `Apply` with **no `invoice:update` permission or IDOR check on the target invoice** — only `payment:create` was checked. A caller holding just `payment:create` could move money onto, and mark PAID, any invoice sharing the same customer, with no `idor_denied`/`permission_denied` logged. This was a genuine gap between the design spec (§9's API table always specified this requirement) and the implementation task's brief, which omitted it. Caught only by the final cross-task review — each of the two contributing tasks (Create, and the `invoiceInScopeForUpdate` helper added later for `Apply`/`Unapply`) was individually correct; nothing wired the helper into `Create`'s path. | `1aa252b` — loop `invoiceInScopeForUpdate` over every inline application before the store call, mirroring `Apply`/`Unapply`. Independently re-verified: the loop runs for every entry, before any mutation, with no partial-write window. |
| I1 | Important | `invoice/store_update.go` `SoftDelete` | The final correctness review found `invoice.SoftDelete` had no guard against live `payment_application` rows referencing that invoice — asymmetric with the guard this same branch added on the payment side (`payment.SoftDelete`, spec AD-11: "every visible ledger row's parent must always be resolvable"). Soft-deleting an invoice with a live application stranded that application: `Unapply` couldn't resolve the now-hidden invoice, and the payment itself couldn't be deleted either (blocked by its own guard) — only a full `VOID` (reversing everything on that payment) could recover. | `863348b` — added the symmetric count-and-block guard, returning `invoice.ClientError` (400). New dbtest coverage in `invoice/store_delete_guard_test.go` — written as an external `invoice_test` package (not internal `invoice`) to avoid a real import cycle, since `payment/quickpay.go` already imports `invoice`. |
| M1 | Minor | `payment/apply.go` `Unapply` | An implementer's own self-review (Task 3.3) caught: `Unapply` set `application_deleted_at = NOW()` unconditionally but bound `application_deleted_by` via `nullableInt(actorEmployeeID)`, which returns SQL NULL when the actor id is `0` — violating the `chk_pay_app_soft_delete` CHECK (both columns must be null- or non-null-together). | Fixed within Task 3.3 (commit `ab3962c`) — added `systemEmployeeID`/`actorOrSystem()` helper, reused consistently by `store_transition.go`'s VOID cascade and `store_update.go`'s `SoftDelete` (never redeclared). |
| M2 | Minor | `payment/store_transition.go` VOID cascade | Task 3.5's review found the cascade's application-listing query had no `ORDER BY`, so two concurrent VOID transitions touching the same two invoices in opposite orders could deadlock (Postgres self-heals via abort+rollback, but it's a needless spurious failure). | Fixed directly by the controller — `ORDER BY invoice_id` added (commit `c7b5dd9`). |
| M3 | Minor | `payment/store.go`, `resolver.go`, `types_test.go` | Final simplification review found 3 new files weren't `gofmt`-clean, and `statusCodeByID` had no call site anywhere in the package (dead code). | Fixed directly by the controller (commit `bd65c0e`) — `gofmt -w` + deletion. |
| M4 | Minor | `controllers/invoice.go` `invoiceFail` | Task 4.5's review found the new `payment.ClientError` case had no test, unlike the identical case in `paymentFail`. | Fixed directly by the controller (commit `cd37cd6`) — added the missing test row. |

### Deferred (recorded, not blocking)

| # | Sev | Location | Finding | Why deferred |
|---|-----|----------|---------|---------------|
| D1 | Minor | `payment/apply.go` `Apply` | A sub-cent apply amount (`0 < amount < 0.005`) can round to `0.00` on insert and violate `chk_pay_app_amount_pos`, surfacing as a 500 instead of a clean 400. | Extreme edge case; a one-line `round2(amount) <= 0 → ClientError` guard is a safe, isolated follow-up. |
| D2 | Minor / product question | `payment/quickpay.go` | `QuickPay` promotes a payment `PEND→APPV` via a raw `UPDATE`, not `Transition` — so no `payment_history` row records that hop (only `'create'` and `'apply'` exist for a QuickPay-created payment). By the design's own choice (the legacy endpoint implies pre-confirmed money), but it's a real audit-trail completeness question worth a human call: does `payment_history` need to be a complete lifecycle record, or is "create + apply" sufficient for a quick-pay? | Product/audit-policy decision, not a code defect — flagging for the user rather than guessing. |
| D3 | Minor | `payment/quickpay.go`, `store_create.go` | `Create`'s inline applications and `QuickPay` both call `Create` (its own transaction) then `Apply` (a separate transaction) sequentially, not atomically — a later application failing doesn't roll back the header or earlier successful applications. Accepted trade-off (spec AD-5/§8), confirmed by final review to not be a data-corruption risk (leaves a valid, inspectable partial state), but it does mean the legacy `QuickPay` endpoint's old contract ("error ⇒ nothing created") is no longer strictly atomic. | Deliberate scope decision from the design spec; a pre-check of balance/payable-status before `Create` would restore atomicity if wanted later. |
| D4 | Minor | `payment/apply.go` `deriveInvoiceStatus` | An invoice that was `ODUE` (overdue) and then fully unapplied reverts to `SENT`, not back to `ODUE` — relies on a separate re-aging process to re-flag it if it's still overdue. | Confirmed intentional in the design (module doesn't own overdue detection); worth confirming a re-aging job actually exists elsewhere in the system. |
| D5 | Minor (style) | `controllers/payment.go` | One `idor_denied` log call uses the literal `"update"` instead of `string(authz.ActionUpdate)`; `payment_audit.go` uses `log.Printf` instead of `slog` (inherited verbatim from the already-shipped `invoice_audit.go`, not new debt). | Cosmetic; zero functional/security impact. |
| D6 | Discretionary refactor | `payment/apply.go` (306 lines), `store_transition.go` | Final simplification review suggested splitting `apply.go`'s four concerns (lock helpers / recompute helpers / status derivation / Apply+Unapply) into a `payment/rollup.go`, and extracting the VOID cascade's ~55-line inline body into a shared helper also usable by `Unapply` (the two bodies overlap). Also: `typeIDByCode`/`statusIDByCode` take `*pgxpool.Pool` and can't be reused inside an open transaction, so `recomputeInvoice`/`Transition` hand-roll the equivalent lookups instead. | Genuine but discretionary — none of it is a defect, all of it is a follow-up-PR-sized refactor, not blocking for this branch. |

### Verified NOT bugs (checked and cleared)
- float64 money math with `round2`/`+0.001`/`<0.005` fuzz tolerances — same accepted pattern as `invoice/calc.go`/`salesorder/calc.go`.
- Cross-payment AR summation (two payments partially paying one invoice, one reversed) — traced end-to-end by the final review; `recomputeInvoice` re-sums *all* live applications (not a delta), so this can't drift.
- No import cycle from `payment` importing `invoice` (one-directional; `invoice` never imports `payment`).
- `team` scope treated identically to `own` in `payment/search.go` — pre-existing, repo-wide pattern already accepted for `invoice`/`salesorder` (fails closed, no leak); not a payments-specific issue.

---

## 4. Recommended next steps
1. Run the full `dbtest` suite against a live tenant database before this ships anywhere with real traffic — no live Postgres was available during this implementation pass, so every integration test's actual runtime behavior (not just its compile-time shape) is currently unverified.
2. Decide D2 (should `QuickPay`'s `PEND→APPV` hop be recorded in `payment_history`?) with whoever owns the audit/compliance requirements.
3. Consider the D6 refactors (split `apply.go`, share the VOID-cascade/Unapply body) as a follow-up cleanup PR — genuine improvements, not urgent.
4. Credit Memo (`CRDT`) and Refund (`RFND`) remain out of scope, per the phased-scope decision made during brainstorming — their `lkp_record_type`/status rows are seeded and ready for a future module.
