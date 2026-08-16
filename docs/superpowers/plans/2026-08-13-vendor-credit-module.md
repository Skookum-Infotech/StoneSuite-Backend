# Vendor Credit Module — Implementation Plan

Spec: [docs/superpowers/specs/2026-08-13-vendor-credit-module-design.md](../specs/2026-08-13-vendor-credit-module-design.md)

Every step names its reference file(s) to mirror. Unless noted, swap
`credit_memo`→`vendor_credit`, `creditmemo`→`vendorcredit`, `invoice`→`vendorbill`,
`customer`→`vendor` mechanically, then drop everything line-item/tax/discount-related
(this module is header-only, AD-1).

All steps are **sequential** — each one either creates a file the next step's mirror
target imports, or edits a file a later step's tests exercise. There is no independent
fan-out here (unlike a cross-cutting fix hand-ported across sibling modules): this is
one new module built bottom-up.

---

## Step 1 — RBAC: add `ActionApprove` for `ResourceVendorCredit`

**File:** `authz/catalog.go`

Add one line directly after the existing `ResourceVendorCredit` block (currently lines
327-331):

```go
{ResourceVendorCredit, ActionApprove},
```

Add a comment above it matching the style already used for `ResourceCreditMemo`
(lines 266-270) / `ResourceRefund` (277-282) / `ResourceItemReceipt` (307-313):
approving is what authorizes real credit against AP, so it's a distinct capability from
`ActionTransition` — a role can hold create/read/update without being able to approve
its own drafts.

No other file changes. `authz/catalog_test.go` likely has a table asserting the full
catalog shape or count — check it and update if it enumerates permissions explicitly.

---

## Step 2 — Schema migration

**File:** `database/migrations/tenant/schema.sql`
**Skill:** invoke `add-migration` for this step.

Append, after the `vendor_bill`/`vendor_payment` blocks (search for
`idx_vpay_approval_payment`, the last line of that block, and insert after it):

1. `ALTER TABLE vendor_bill ADD COLUMN IF NOT EXISTS vendor_bill_credit_total DECIMAL(15,2) NOT NULL DEFAULT 0;`
2. `CREATE TABLE IF NOT EXISTS vendor_credit (...)` — full DDL in spec §4.3.
3. `CREATE TABLE IF NOT EXISTS vendor_credit_history (...)` — spec §4.3.
4. `CREATE TABLE IF NOT EXISTS vendor_credit_application (...)` + the
   `uq_vcrd_app_live_pair` unique partial index — spec §4.3.
5. The nine indexes in spec §4.4.

**Do not** add any `lkp_record_type` or `lkp_record_status` rows — `VCRD` (record_type
id 17) and its `DRFT/APPV/APPL/VOID` statuses are already seeded
(`tenant/schema.sql:710,752`). Confirm this by grepping the file for `VCRD` before
writing anything — if a future schema change has since removed or altered those rows,
stop and report back rather than re-adding them differently.

Verify the migration is idempotent per CLAUDE.md: `CREATE TABLE IF NOT EXISTS`,
`ADD COLUMN IF NOT EXISTS`, no bare `ADD CONSTRAINT` (this module's CHECK constraints
are all inside `CREATE TABLE` bodies, which is fine — the idempotency risk is only for
constraints added to an *existing* table after the fact).

---

## Step 3 — Extend `vendorbill/balance.go` for the credit rollup

**Files:** `vendorbill/balance.go`, `vendorbill/balance_test.go`
**Reference:** `invoice/balance.go` (has this exact shape already — `AmountPaid` +
`CreditTotal` kept separate, `settled()` helper, `DeriveStatus(currentCode, settled,
grandTotal)`).

Changes to `vendorbill/balance.go`:

1. Add `CreditTotal float64` to the `Locked` struct.
2. `BalanceDue()`: change to `l.GrandTotal - l.AmountPaid - l.CreditTotal`.
3. Add a `settled()` method: `round2(l.AmountPaid + l.CreditTotal)` (mirror
   `invoice.Locked.settled()`).
4. `LockForUpdate` and `LockForUpdateByID`: add `vb.vendor_bill_credit_total` to both
   `SELECT` lists and their `Scan(...)` calls.
5. `DeriveStatus`: change signature from `(currentCode string, amountPaid, grandTotal
   float64)` to `(currentCode string, settled, grandTotal float64)`. The body's logic is
   unchanged — it already just compares a single number against `grandTotal`; only the
   parameter's meaning changes (was cash-only, now cash+credit).
6. `RecomputeBalance`: add a third query summing live `vendor_credit_application` rows
   (mirror the existing `vendor_payment_application` query, joined the same way,
   filtered on `application_deleted_at IS NULL` — a `vendor_credit_application` has no
   separate refund ledger, so there is no fourth "refunded" subtraction here, unlike the
   payment-application query's `- refunded`). Call the local var `creditTotal`. Pass
   `updated.settled()` (not raw `amountPaid`) into `DeriveStatus`. Add
   `vendor_bill_credit_total = $N` to the `UPDATE vendor_bill SET ...` statement and
   thread `creditTotal` through as a new bind arg.

Update `vendorbill/balance_test.go`'s `TestDeriveStatus` table to pass a `settled` value
instead of `amountPaid` (rename the test's local variable/field for clarity; the actual
float64 values in existing cases are unchanged since they were always cash-only,
i.e. `settled == amountPaid` in every pre-existing case).

**Do not** touch `vendorpayment/*.go` or `vendorbill/store_payment.go` — they call
`RecomputeBalance(ctx, tx, l, action, actorEmployeeID)` positionally and that signature
does not change; only `Locked`'s field set and `DeriveStatus`'s signature change, and
`DeriveStatus` has exactly one other caller (the test above).

---

## Step 4 — `vendorcredit` package: types, numbering, transitions, shared store helpers

**Files (new):** `vendorcredit/types.go`, `vendorcredit/numbering.go`,
`vendorcredit/transitions.go`, `vendorcredit/store.go`
**Reference:** `creditmemo/types.go`, `creditmemo/numbering.go` (actually check
`vendorbill/numbering.go` — simpler, no separate file needed beyond the prefix
constant), `creditmemo/transitions.go`, `creditmemo/store.go`, plus
`vendorbill/store.go` for the exact shared-helper shapes (`buildInsert`,
`buildUpdateSet`, `colVal`, `nullableInt`, `actorOrSystem`, `isForeignKeyViolation`,
`recordTypeIDByCode`, `statusIDByCode`, `validateCustom`).

**`types.go`** — package doc comment mirroring `vendorbill/types.go`'s (relational
sibling, spec pointer). Define:
- `VendorRef {ID, Name string}`.
- `Application {ID, VendorBillID, VendorBillNumber string; Amount float64; CreatedAt
  time.Time}` (mirror `creditmemo.Application`, swap `InvoiceID/InvoiceNumber` for
  `VendorBillID/VendorBillNumber`).
- `VendorCredit` struct: `ID, Number string`; `StatusCode, StatusName string`; `Vendor
  VendorRef`; `OwnerUserID string` (unexported-from-JSON via `json:"-"`, backs IDOR),
  `OwnerEmployeeID *int`; `ReferenceNumber, Reason, Memo, InternalNotes string`;
  `CreditDate time.Time`; `GrandTotal, AppliedTotal, UnappliedAmount float64`;
  `CustomFields map[string]any`; `Applications []Application`; `CreatedAt, UpdatedAt
  time.Time`; `RecordVersion int`.
- `CreateVendorCreditInput {VendorUUID string; ReferenceNumber, CreditDate, Reason,
  Memo, InternalNotes string; OwnerEmployeeID *int; Amount float64; CustomFields
  map[string]any}`.
- `UpdateVendorCreditInput` — same fields minus `VendorUUID` (vendor fixed at creation,
  AD-12), `Amount` as `*float64` so a PATCH that omits it leaves the stored amount
  unchanged (mirror how `creditmemo.UpdateCreditMemoInput` uses `*float64` for
  `Adjustment`).
- `Page {Records []VendorCredit; NextCursor string; HasMore bool}`.

**`numbering.go`** — mirror `vendorbill/numbering.go` exactly:
`const numberPrefix = "VCR"`, `FormatNumber(serialID int64) string` →
`fmt.Sprintf("%s-%06d", numberPrefix, serialID)` → `VCR-000001`.

**`transitions.go`** — mirror `creditmemo/transitions.go`, with the map from spec §5:
```go
var allowedTransitions = map[string]map[string]bool{
    "DRFT": {"APPV": true, "VOID": true},
    "APPV": {"APPL": true, "VOID": true},
    "APPL": {},
    "VOID": {},
}
```
Same `ErrInvalidTransition`, `CanTransition`, `ValidateTransition` shape.

**`store.go`** — mirror `vendorbill/store.go`'s helper section (not its vendor/line
helpers, which are Vendor-Bill-specific — copy the *generic* ones):
- `ErrNotFound`, `ClientError`/`Error()`/`IsClientError`.
- `const vcrdRecordTypeCode = "VCRD"`, `const draftStatusCode = "DRFT"`.
- `nullableInt`, `const systemEmployeeID = 1`, `actorOrSystem`, `isForeignKeyViolation`,
  `colVal`, `buildInsert`, `buildUpdateSet`, `recordTypeIDByCode`, `statusIDByCode`
  (all byte-for-byte portable from `vendorbill/store.go` — they're generic over table
  name).
- `round2` (from `vendorbill/calc.go` — just the one function; this module has no
  `ComputeHeader`/`ComputeLine`/`LineMoney`, AD-1).
- New helper `activeVendorSnapshot(ctx, q, vendorUUID) (id int, name string, err
  error)`: like `vendorbill.vendorSnapshot` but additionally joins
  `lkp_record_status rs ON rs.record_status_id = v.vendor_status` and requires
  `rs.record_status_code = 'ACT_'`; on a status mismatch return
  `ClientError{Msg: "Vendor is not active."}` (distinct message from the not-found
  case, `ClientError{Msg: "Unknown vendor."}`) — this is spec AD-8, and the two
  messages matter for the "inactive vendor" test asserting on error text.
- `validateCustom` — mirror `vendorbill.validateCustom`, workflow key `"vendor_credit"`.
- `func internalIDByUUID(ctx, pool, id string) (int, string, error)` — mirror
  `creditmemo.internalIDByUUID` (returns internal id + current status code, `ErrNotFound`
  on no rows).
- `func Get(ctx, pool, uuid string) (*VendorCredit, error)` — load header by
  `vendor_credit_uuid`, join `lkp_record_status` for status code/name, then load live
  `vendor_credit_application` rows joined to `vendor_bill` for
  `VendorBillNumber`/display, into `.Applications`. Mirror the query shape of
  `vendorbill`'s own `Get` (in `store_get.go` — read it for the exact join/scan
  pattern) crossed with `creditmemo`'s `Applications` load (find it — likely inline in
  `creditmemo`'s `Get`, search `creditmemo/*.go` for `func Get(` since it wasn't in the
  files read during design).

---

## Step 5 — `vendorcredit` package: Create, Update, Transition

**Files (new):** `vendorcredit/store_create.go`, `vendorcredit/store_update.go`,
`vendorcredit/store_transition.go`
**Reference:** `creditmemo/store_create.go`, `creditmemo/store_update.go`,
`creditmemo/store_transition.go`; `vendorbill/store_create.go` for the
`colVal`/`buildInsert` insert style.

**`store_create.go`** — `Create(ctx, pool, in CreateVendorCreditInput, actorEmployeeID
int) (*VendorCredit, error)`:
- Validate `VendorUUID` non-blank, `Amount > 0`.
- `activeVendorSnapshot` (Step 4) for the vendor id + name snapshot — this is the
  "reject inactive vendor" gate (AD-8).
- `validateCustom`.
- Resolve `VCRD` record type id, `DRFT` status id.
- Single-row insert via `buildInsert("vendor_credit", cv, "vendor_credit_id,
  vendor_credit_uuid")` with `vendor_credit_applied_total = 0`,
  `vendor_credit_unapplied_amount = Amount` at insert time.
- Set `vendor_credit_number` post-insert via `FormatNumber`.
- Insert a `vendor_credit_history` row, `action='create'`, `from_status_id=NULL`.
- No line handling (AD-1) — this is meaningfully shorter than
  `creditmemo.Create`/`vendorbill.Create`.

**`store_update.go`** — `Update(ctx, pool, id string, in UpdateVendorCreditInput,
actorEmployeeID int) (*VendorCredit, error)`:
- `internalIDByUUID`; reject (`ClientError`) if status is not `DRFT` — spec explicitly
  scopes Update to Draft only (stricter than `creditmemo.Update`, which allows
  non-monetary edits outside DRFT; this module has no non-monetary/monetary split to
  preserve since there are no lines, so "Draft only" per the request applies to the
  whole record).
- Apply `in.Amount` if non-nil (re-derive `unapplied_amount = amount - applied_total`;
  since status is DRFT, `applied_total` is always 0 at this point per the transition
  map, so this is just `unapplied_amount = amount`, but compute it from the live
  `applied_total` column rather than assuming zero, for defensiveness).
- Update the other editable fields unconditionally (`referenceNumber`, `creditDate`,
  `reason`, `memo`, `internalNotes`, `ownerEmployeeId`, `customFields` — nil-guard the
  map like `creditmemo.Update` does).
- Insert a `vendor_credit_history` row, `action='update'`, `from_status_id=to_status_id`
  (mirror `creditmemo.Update`'s self-referencing history insert).
- `SoftDelete(ctx, pool, id string, actorEmployeeID int) error` also lives here (mirror
  `creditmemo.SoftDelete`): reject (409-mapped `ClientError`) if any live
  `vendor_credit_application` references this credit; otherwise set
  `vendor_credit_deleted_at/_by`.

**`store_transition.go`** — `Transition(ctx, pool, id, toStatusCode string,
actorEmployeeID int) (*VendorCredit, error)`, mirror `creditmemo.Transition`
byte-for-byte with `vendor_bill` swapped for `invoice`:
- Validate via `ValidateTransition`.
- Lock the credit row first (`vendor_credit < vendor_bill`, AD-6).
- If `toStatusCode == "VOID"`: list live `vendor_credit_application.vendor_bill_id`
  `ORDER BY vendor_bill_id` (global lock-order tiebreaker), for each:
  `vendorbill.LockForUpdateByID`, soft-delete the application row, call
  `vendorbill.RecomputeBalance(ctx, tx, l, "reverse", actorEmployeeID)`. After the loop,
  if any were reversed, directly reset `vendor_credit_applied_total = 0,
  vendor_credit_unapplied_amount = vendor_credit_grand_total` (mirror
  `creditmemo.Transition`'s direct reset rather than routing back through a recompute
  helper that would re-derive APPV).
- Update `vendor_credit_status`, insert history row `action='transition'`.

---

## Step 6 — `vendorcredit/apply.go`: Apply and Reverse

**File (new):** `vendorcredit/apply.go`
**Reference:** `creditmemo/apply.go` byte-for-byte, `invoice`→`vendorbill`,
`credit_memo`→`vendor_credit`, `Unapply`→`Reverse` (rename per spec AD-4 — the request's
API vocabulary).

- `appliableStatuses = map[string]bool{"APPV": true, "APPL": true}` (identical to
  `creditmemo`'s).
- `lockedCredit` struct (mirror `lockedMemo`): `internalID, vendorID int; statusCode
  string; grandTotal float64`.
- `lockVendorCreditForUpdate(ctx, tx, creditUUID string) (lockedCredit, error)` — `FOR
  UPDATE OF vc`, mirror `lockCreditMemoForUpdate`.
- `recomputeCredit(ctx, tx, lc lockedCredit, action string, actorEmployeeID int) error`
  — sum live `vendor_credit_application.application_amount` for this credit, derive
  `applied`/`unapplied`, re-derive `APPV`↔`APPL` the same way `recomputeMemo` does,
  update the row, insert a history row. Mirror `recomputeMemo` exactly.
- `Apply(ctx, pool, creditUUID, billUUID string, amount float64, actorEmployeeID int)
  (*VendorCredit, error)`:
  1. `amount <= 0` → `ClientError`.
  2. Begin tx.
  3. `lockVendorCreditForUpdate` (credit first, AD-6).
  4. Reject (`ClientError`, maps to 409 via the same `creditMemoFail`-style mapping in
     the controller) unless `appliableStatuses[lc.statusCode]`.
  5. `vendorbill.LockForUpdate(ctx, tx, billUUID)` (bill second, always last).
  6. Reject (400) on `li.VendorID != lc.vendorID` — vendor mismatch.
  7. Reject (409) unless `vendorbill.PayableStatuses[li.StatusCode]`.
  8. Sum live applications for this credit → `unapplied`; `capAmt =
     min(unapplied, li.BalanceDue())`; reject (400, "Amount exceeds available credit or
     vendor bill balance.") if `amount > capAmt+0.001` — **never clamp**. This cap
     check, combined with the row locks taken above, is what makes the concurrent-apply
     test deterministic: two simultaneous callers serialize on the bill's `FOR UPDATE`
     row lock, so the second to actually execute re-reads the post-first-commit balance.
  9. Upsert the live `(credit, bill)` application row (find existing live row → `UPDATE
     ... SET application_amount = application_amount + $1`; else `INSERT`) — relies on
     `uq_vcrd_app_live_pair`.
  10. `recomputeCredit(..., "apply", ...)`.
  11. `vendorbill.RecomputeBalance(ctx, tx, li, "credit", actorEmployeeID)`.
  12. Commit; return `Get(ctx, pool, creditUUID)`.
- `Reverse(ctx, pool, creditUUID, billUUID string, actorEmployeeID int) (*VendorCredit,
  error)` — mirror `creditmemo.Unapply`: lock credit then bill (no status gate — must
  always be possible), soft-delete the live application row for that pair (404
  `ClientError` if none), `recomputeCredit(..., "reverse", ...)`,
  `vendorbill.RecomputeBalance(ctx, tx, li, "uncredit", actorEmployeeID)`, commit, return
  `Get(...)`.

---

## Step 7 — `vendorcredit` search

**Files (new):** `vendorcredit/resolver.go`, `vendorcredit/store_search.go`
**Reference:** `vendorbill/resolver.go` (structure) trimmed to spec §7's field list (no
line-item `EXISTS` sub-select — no lines in this module); `creditmemo/search.go` for the
`Search(ctx, pool, scope, identityID string, req query.Request) (Page, error)` shape.

`resolver.go` — table alias `vc`, fields: `id, document_number, record_number,
vendor_id, status, reference_number, credit_date, reason, grand_total, applied_total,
unapplied_amount, owner_id, created_at, updated_at, cf:<key>`; sort whitelist
`document_number, credit_date, grand_total, unapplied_amount, status, vendor_id,
created_at, updated_at`; search predicate over `vendor_credit_number`,
`vendor_credit_reference_number`, `vendor_credit_reason`, `vendor_credit_memo`,
`vendor_credit_vendor_name` ILIKE, plus an `EXISTS` on `vendor` name fields (mirror
`vendorbill.resolver.SearchPredicate`'s vendor `EXISTS` clause, drop its line-item
`EXISTS`).

`store_search.go` — mirror `vendorbill/store_search.go` (check it — not read during
design, but it will follow the same `query.Search`/scope-composition shape every
sibling's `Search` uses).

---

## Step 8 — Controllers

**Files (new):** `controllers/vendorcredit.go`, `controllers/vendorcredit_transition.go`,
`controllers/vendorcredit_audit.go`
**Reference:** `controllers/creditmemo.go`, `controllers/creditmemo_transition.go`,
`controllers/vendorbill_audit.go` (audit pattern, since `creditmemo_audit.go` also
exists and is equally valid to mirror — check both, they should agree).

**`vendorcredit.go`** — `VendorCreditOps` struct, mirror `CreditMemoOps` exactly:
- `NewVendorCreditOps()`.
- `authVendorCredit(w, r, action)` — `authz.Check(..., authz.ResourceVendorCredit,
  action)`, `logSecurityEvent(..., "permission_denied", ...)` on denial.
- `authVendorCreditByUUID(w, r, uuid, action)` — scope/IDOR guard via `recordInScope`,
  404 (not 403) on denial, `logSecurityEvent(..., "idor_denied", ...)`.
- `vendorBillInScopeForUpdate(w, r, pool, identityID, billUUID string) bool` — mirror
  `invoiceInScopeForUpdate`: `authz.ResourceVendorBill` + `ActionUpdate`, then IDOR via
  `vendorbill.Get` + `recordInScope`. This is the dual-permission check for
  Apply/Reverse (AD from the vendor-payment spec, same reasoning: a caller who can edit
  their own credit but can't see the target bill must not move money onto it).
- `vendorCreditFail(w, err, serverMsg)` — mirror `creditMemoFail`: `ErrNotFound`→404,
  `ErrInvalidTransition`→409, `vendorcredit.ClientError`→400, `vendorbill.ClientError`→
  400 (Apply/Reverse reach into `vendorbill` for the lock/rollup, same as
  `creditMemoFail` catching `invoice.ClientError`), `*query.InvalidFilterError`→400,
  else 500.
- `Create`, `Get`, `Update`, `Delete`, `List`, `Search` — mirror the `CreditMemoOps`
  methods 1:1 (no `Applications` field to reject on Create since this module doesn't
  accept inline applications on create either — new credits start DRFT and can't move
  money, same reasoning as `creditmemo`'s AD-7 comment, so there's nothing to reject:
  `CreateVendorCreditInput` never had an `Applications` field to begin with, Step 4).

**`vendorcredit_transition.go`** — mirror `creditmemo_transition.go`:
- `vcTransitionRequest {ToStatusCode string}`.
- `const approvalTargetStatus = "APPV"`, `actionForTransition(toStatusCode) authz.Action`
  — `ActionApprove` iff target is `APPV`, else `ActionTransition`.
- `Transition` handler — decode body first (status selects the permission), then auth,
  then `vendorcredit.Transition`, then audit.
- `vcApplyRequest {VendorBillUUID string; Amount float64}`, `Apply` handler — auth
  `ActionUpdate` + IDOR on the credit, `vendorBillInScopeForUpdate` on the bill, call
  `vendorcredit.Apply`.
- `vcReverseRequest {VendorBillUUID string}`, `Reverse` handler — same dual-check
  shape, call `vendorcredit.Reverse`. (Named `Reverse` per spec AD-4, not `Unapply`.)

**`vendorcredit_audit.go`** — mirror `controllers/vendorbill_audit.go`:
- `vcSnapshot(c *vendorcredit.VendorCredit) map[string]any` — id, number, status,
  vendorId, grandTotal, appliedTotal, unappliedAmount.
- `auditVC(r, pool, identityID, action, recordID string, old, new
  *vendorcredit.VendorCredit)` — `workflow.LogAuditFull(..., "vendor_credit", recordID,
  "vendor_credit", ...)`.
- `auditVCDelete(...)`.
- `Audit` handler — `GET .../audit`, `loadAuditEntries`, same envelope as
  `VendorBillOps.Audit`.

---

## Step 9 — Route registration

**File:** `main.go`
**Reference:** lines 849-861 (the Vendor Bill block) as the closest shape (Create/Get/
Update/Delete/List/Search/Transition/Audit — Vendor Credit has no `/payment` sub-routes,
but does have `/apply`/`/reverse` like Vendor Payment's `/apply`/`/unapply`, lines
876-877).

Add a new block, alongside the existing Vendor Bill / Vendor Payment blocks:

```go
vcOps := controllers.NewVendorCreditOps()
mux.Handle("GET /api/tenant/vendor-credits", tenantChain(vcOps.List))
mux.Handle("POST /api/tenant/vendor-credits/search", tenantChain(vcOps.Search))
mux.Handle("POST /api/tenant/vendor-credits", tenantChain(vcOps.Create))
mux.Handle("GET /api/tenant/vendor-credits/{uuid}", tenantChain(vcOps.Get))
mux.Handle("PATCH /api/tenant/vendor-credits/{uuid}", tenantChain(vcOps.Update))
mux.Handle("DELETE /api/tenant/vendor-credits/{uuid}", tenantChain(vcOps.Delete))
mux.Handle("POST /api/tenant/vendor-credits/{uuid}/transition", tenantChain(vcOps.Transition))
mux.Handle("POST /api/tenant/vendor-credits/{uuid}/apply", tenantChain(vcOps.Apply))
mux.Handle("POST /api/tenant/vendor-credits/{uuid}/reverse", tenantChain(vcOps.Reverse))
mux.Handle("GET /api/tenant/vendor-credits/{uuid}/audit", tenantChain(vcOps.Audit))
```

Match whatever comment banner style precedes the Vendor Bill/Vendor Payment blocks
(check for a `// ===== Purchases: Vendor Bill =====`-style comment above line 849 and
follow it).

---

## Step 10 — Unit tests (table-driven, pure functions)

**Files (new):** `vendorcredit/transitions_test.go`, `vendorcredit/numbering_test.go`,
`vendorcredit/resolver_test.go`
**Reference:** `creditmemo/transitions_test.go`, `vendorbill/numbering_test.go`,
`vendorbill/resolver_test.go` — same table shapes, new module's data.

Also update `vendorbill/balance_test.go` (done in Step 3) — confirm it still compiles
and its cases still pass with the renamed parameter.

No `//go:build dbtest` tag on these — they're pure-function tests, always run.

---

## Step 11 — Integration tests (dbtest-tagged)

**File (new):** `vendorcredit/store_test.go` (mirror the `//go:build dbtest` header,
`testPool`, and `seedVendor` helpers from `vendorbill/store_test.go` verbatim — this
module also needs a `seedVendorBill` helper that creates a live `vendor_bill` in
`APPV`/`PART` status with a known `grand_total`, since every Apply/Reverse test needs
one to target).

Cover every scenario the request names, each as its own `t.Run`:

1. **Create — inactive vendor rejected.** Seed a vendor with `vendor_status = INA_`
   (mirror `seedVendor` but pass the `INA_` status id); `Create` must return a
   `ClientError` containing "not active" (400 at the controller layer). Also cover
   deleted vendor (`vendor_deleted_at` set) separately, since that's the pre-existing
   `ClientError{Msg: "Unknown vendor."}` path.
2. **Apply — inactive/deleted/cancelled/wrong-status bill rejected.** Seed bills in
   `DRFT`, `PAID`, `VOID`, and soft-deleted, assert `Apply` rejects each with a
   `vendorbill.ClientError` (409-mapped) or not-found, per which case.
3. **Apply — inactive vendor payment does not inflate the deductible balance.**
   (Proves spec AD-9, no new production code — this is a regression guard on existing
   `vendorbill.RecomputeBalance` behavior.) Seed a bill, record a live
   `vendor_payment`+application against it, void the payment (cascading its application
   to soft-deleted per `vendor_payment` AD-8), then apply a vendor credit for the bill's
   *full* `grand_total` and assert it succeeds — if the voided payment's application
   were still counted, the credit would be over-capped and wrongly rejected.
4. **Cross-tenant / unknown-id access.** Name this `t.Run("not found - cross-tenant",
   ...)` mirroring `tenancy/sso_config_test.go:84`'s naming and its documented reasoning
   (a foreign tenant's id is architecturally indistinguishable from an id that doesn't
   exist, since each tenant is a separate database) — assert `Get`/`Apply`/`Reverse`
   all return `ErrNotFound`/`ClientError` (never a 500, never partial data) for a
   random, never-issued UUID.
5. **Over-credit rejected, never clamped.** Apply an amount 1 cent above
   `min(unapplied, balanceDue)`; assert `ClientError` and that neither the credit's
   `applied_total` nor the bill's `balance_due` changed (re-`Get` both, compare against
   pre-call values).
6. **Concurrent credit application.** Seed one credit with unapplied amount X and one
   bill with balance X, where X is small enough that only one of two simultaneous
   `Apply(X)` calls can succeed without over-crediting. Launch both against the same
   pool via two goroutines + a `sync.WaitGroup`, both requesting the full X. Assert:
   exactly one succeeds and the other gets a `ClientError` (cap exceeded) OR — if the
   two calls interleave such that both see the same amount available and race to
   commit — assert the *final* state after both complete never has `applied_total >
   grand_total` and never has bill `balance_due < 0`, i.e. assert the invariant
   post-hoc rather than which specific goroutine won, since `FOR UPDATE` lock
   contention makes goroutine ordering nondeterministic by design.
7. **Approval flow.** `DRFT` credit; `Transition(..., "APPV", ...)` succeeds and status
   becomes `Approved`; a second `Transition(..., "APPV", ...)` on an already-`APPV`
   credit is rejected (`ErrInvalidTransition`, not in the map from `APPV`).
8. **Reverse flow.** Apply, assert bill balance reduced and credit `unappliedAmount`
   reduced; `Reverse`, assert both restored to pre-Apply values; assert credit status
   drops back from `APPL` to `APPV` if it had reached full consumption.
9. **Cancellation.** `Cancel` (`Transition(..., "VOID", ...)`) from `DRFT` with no
   applications — trivial VOID. Then from `APPV` with one live application — assert the
   cascade reverses it (bill balance restored, credit rollup reset to
   `applied=0/unapplied=grandTotal`) before landing on `VOID`. Then assert `APPL →
   VOID` is rejected directly (`ErrInvalidTransition`) — must `Reverse` first.
10. **Rollback on failure.** Force a failure mid-`Apply` (e.g. call `Apply` with a
    syntactically valid but non-existent `billUUID` after a *prior*, otherwise-valid
    partial state exists — the existing `vendorbill.LockForUpdate` returning
    `ClientError{Msg: "Unknown or deleted vendor bill."}` at step 5 of the Apply
    sequence, Step 6 of this plan — must leave the credit's `applied_total`/
    `unapplied_amount` and any prior application row completely untouched; re-`Get` the
    credit after the failed call and assert it matches the pre-call snapshot exactly).
11. **Permission validation.** This one belongs at the controller layer, not the store
    layer — see the note below.

**File (new):** `controllers/vendorcredit_dbtest_test.go` (mirror
`controllers/saml_dbtest_test.go` or another controller-level dbtest suite's harness
setup for constructing a request with a JWT/identity in context — find whichever
existing controller dbtest is most current and mirror its harness, since
`controllers/creditmemo*.go` has no dbtest file to copy from; check for one on a
sibling like `controllers/vendorbill_*` or `controllers/purchaseorder_*` first).

12. **RBAC permission denial.** A role with `vendor_credit:read` only (no `:create`)
    gets 403 from `Create`; a role with `:update`+`:transition` but not `:approve` gets
    403 from `Transition(..., "APPV", ...)` specifically (while a plain `VOID`
    transition with the same role succeeds) — this is the concrete assertion that
    Step 1's `ActionApprove` split actually gates something.

If step 12's controller-level harness doesn't have a clean existing pattern to mirror,
it is acceptable to instead prove RBAC denial at the `authz` package level (call
`authz.Check` directly against a role granted only a subset of
`vendor_credit:*` permissions and assert `Allowed == false` for the ungranted actions) —
report back which approach was taken and why.

---

## Verification (go-verifier, Phase 3)

- `go build ./...`
- `go vet ./...`
- `go test ./...`
- `go test -tags dbtest ./...` (requires `TEST_DATABASE_URL`; skips cleanly otherwise
  per every existing dbtest file's `t.Skip`)
- Confirm every scenario in Step 11 has a corresponding `t.Run` and actually asserts the
  invariant named, not just "no error".

## Review fan-out (Phase 4) — expected to trigger

- `migration-auditor` (Step 2 touches `database/migrations/tenant/schema.sql`).
- `module-drift-checker` (new module package + `controllers/*.go` +
  `authz/catalog.go` + `main.go`).
- `tenancy-security-reviewer` (new `controllers/`, new `*store*`/`vendorcredit/`
  package).
- `filter-invariant-checker` (new `vendorcredit/resolver.go` +
  `vendorcredit/store_search.go`).
