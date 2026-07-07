# Approval Validation Messages Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Tighten the CRM customer-record approval flow's validation branching and error messages to cover five explicit scenarios (unauthorized approver, pending-view, assigned-approver, already-approved, no-approver-configured), and expose a `canApprove` flag on record reads so the frontend can decide whether to show the Approve button.

**Architecture:** All changes live in `crmstore` (DesignV2 relational store + `Store` interface + sentinel errors) and `controllers/crm.go` (HTTP mapping). A new pure function `approvalDecision` centralizes the branching so it's unit-testable without a database. No schema changes, no new endpoints, no frontend changes (separate repo).

**Tech Stack:** Go, pgx/v5, testify (table-driven unit tests).

## Global Constraints

- Message text must match exactly (from the spec):
  - Unauthorized approver → `"You are not authorized to approve this document. Only the assigned approver(s) can approve it."` (403)
  - Already approved → `"This document has already been approved."` (409)
  - No approver configured → `"No approver is configured for this workflow. Please contact your administrator."` (409)
- Sentinel error strings passed to `errors.New` are lowercase-first per existing codebase convention (e.g. `ErrNotApprover`'s current text) — `crmFail` renders `err.Error()` directly, so the leading character of each sentinel string must be lowercase even though the rendered message to the client starts uppercase per the exact text above. Concretely: `errors.New("you are not authorized to approve this document. Only the assigned approver(s) can approve it.")` etc. — first letter lowercase, rest exactly as specified.
- Scope: CRM `customer` records only (DesignV2 `relationalStore`). DesignV1 `workflowStore` gets a stub `IsApprover` returning `false, nil` — no behavior change there.
- No new RBAC permission/action — reuse existing `customer:update` gate in `authCRMByRecordID`.
- Every changed/added Go function that touches state must keep existing error-wrapping conventions (`fmt.Errorf("...: %w", err)`).
- `canApprove` is added only to the single-record `GET` response, not list/search endpoints.
- Existing tests in `crmstore/relational_store_test.go` must stay green throughout.

---

## Task 1: Sentinel errors for the three new/changed outcomes

**Files:**
- Modify: `crmstore/store.go:22-31`
- Test: `crmstore/relational_store_test.go` (new test function, no DB needed)

**Interfaces:**
- Produces: `crmstore.ErrAlreadyApproved`, `crmstore.ErrNoApproverConfigured` (both `error`, package-level vars in `crmstore`). `crmstore.ErrNotApprover` (existing var) gets its message text changed in place — same identifier, same import path, no signature change.

- [ ] **Step 1: Edit the sentinel error block**

Current code at `crmstore/store.go:22-31`:
```go
// Sentinel errors.
var (
	// ErrNotSupported is returned by a design that does not implement an op
	// (e.g. Approve on DesignV1).
	ErrNotSupported = errors.New("operation not supported for this database design")
	// ErrRecordNotFound is returned when a record id matches nothing.
	ErrRecordNotFound = errors.New("record not found")
	// ErrNotApprover is returned when the caller is not a configured approver.
	ErrNotApprover = errors.New("you are not a configured approver for this record")
)
```

Replace with:
```go
// Sentinel errors.
var (
	// ErrNotSupported is returned by a design that does not implement an op
	// (e.g. Approve on DesignV1).
	ErrNotSupported = errors.New("operation not supported for this database design")
	// ErrRecordNotFound is returned when a record id matches nothing.
	ErrRecordNotFound = errors.New("record not found")
	// ErrNotApprover is returned when the caller is not a configured approver.
	ErrNotApprover = errors.New("you are not authorized to approve this document. Only the assigned approver(s) can approve it.")
	// ErrAlreadyApproved is returned when a record has already been approved.
	ErrAlreadyApproved = errors.New("this document has already been approved.")
	// ErrNoApproverConfigured is returned when a record is pending approval but
	// no active approver is configured for its record type.
	ErrNoApproverConfigured = errors.New("no approver is configured for this workflow. Please contact your administrator.")
)
```

- [ ] **Step 2: Update `IsClientError` doc/behavior check (no functional change needed, verify only)**

`crmstore/store.go:39-43` currently:
```go
// IsClientError reports whether err should surface as a 400 to the client.
func IsClientError(err error) bool {
	var ce ClientError
	return errors.As(err, &ce) || errors.Is(err, ErrNotApprover)
}
```
`ErrNotApprover` maps to 403 explicitly in `crmFail` (checked *before* `IsClientError` in the switch — see Task 4), so its inclusion here is dead weight for the 400 path but harmless (unreachable because `crmFail`'s switch checks `ErrNotApprover` first). Leave this function unchanged — do not add `ErrAlreadyApproved`/`ErrNoApproverConfigured` here, since Task 4 gives them their own explicit 409 cases before the `IsClientError` fallback, exactly like `ErrNotApprover`.

- [ ] **Step 3: Write a test confirming the new sentinels exist and are distinct**

Add to `crmstore/relational_store_test.go` (append at end of file):
```go
func TestApprovalSentinelErrorsAreDistinct(t *testing.T) {
	assert.NotEqual(t, ErrNotApprover.Error(), ErrAlreadyApproved.Error())
	assert.NotEqual(t, ErrNotApprover.Error(), ErrNoApproverConfigured.Error())
	assert.NotEqual(t, ErrAlreadyApproved.Error(), ErrNoApproverConfigured.Error())
}
```

- [ ] **Step 4: Run the test**

Run: `go test ./crmstore/... -run TestApprovalSentinelErrorsAreDistinct -v`
Expected: FAIL to compile (`ErrAlreadyApproved`/`ErrNoApproverConfigured` undefined) until Step 1 is applied; after Step 1, PASS.

- [ ] **Step 5: Commit**

```bash
git add crmstore/store.go crmstore/relational_store_test.go
git commit -m "feat(crm): add ErrAlreadyApproved/ErrNoApproverConfigured, tighten ErrNotApprover message"
```

---

## Task 2: Pure `approvalDecision` function + table-driven tests

**Files:**
- Modify: `crmstore/relational_store.go` (add function near `Approve`, i.e. just above line 659)
- Test: `crmstore/relational_store_test.go`

**Interfaces:**
- Consumes: nothing (pure function, three primitive args).
- Produces: `func approvalDecision(status string, anyApproverConfigured, callerIsApprover bool) error` — package-level function in `crmstore`, used by `Approve()` in Task 3.

- [ ] **Step 1: Write the failing tests**

Append to `crmstore/relational_store_test.go`:
```go
func TestApprovalDecision(t *testing.T) {
	tests := []struct {
		name                   string
		status                 string
		anyApproverConfigured  bool
		callerIsApprover       bool
		wantErr                error
		wantNil                bool
	}{
		{"pending, configured, caller is approver -> proceed", "pending", true, true, nil, true},
		{"pending, configured, caller not approver -> not authorized", "pending", true, false, ErrNotApprover, false},
		{"pending, nobody configured -> no approver configured", "pending", false, false, ErrNoApproverConfigured, false},
		{"pending, nobody configured, but caller flag true (impossible in practice) -> still blocked", "pending", false, true, ErrNoApproverConfigured, false},
		{"already approved -> already approved", "approved", true, true, ErrAlreadyApproved, false},
		{"already approved, caller not approver -> still already approved", "approved", true, false, ErrAlreadyApproved, false},
		{"not yet pending -> not pending approval", "none", true, false, nil, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := approvalDecision(tc.status, tc.anyApproverConfigured, tc.callerIsApprover)
			if tc.wantNil {
				assert.NoError(t, err)
				return
			}
			if tc.wantErr != nil {
				assert.ErrorIs(t, err, tc.wantErr)
				return
			}
			// "none" status case: expect a ClientError, not one of the sentinels.
			assert.Error(t, err)
			assert.False(t, errors.Is(err, ErrNotApprover))
			assert.False(t, errors.Is(err, ErrAlreadyApproved))
			assert.False(t, errors.Is(err, ErrNoApproverConfigured))
			var ce ClientError
			assert.True(t, errors.As(err, &ce))
			assert.Equal(t, "This record is not pending approval.", ce.Msg)
		})
	}
}
```

Add `"errors"` to the test file's import block if not already present (it currently only imports `testing` and `testify/assert` — check the top of `crmstore/relational_store_test.go` before editing; add `"errors"` as a new import line if missing).

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./crmstore/... -run TestApprovalDecision -v`
Expected: FAIL to compile — `approvalDecision` undefined.

- [ ] **Step 3: Implement `approvalDecision`**

Add to `crmstore/relational_store.go` immediately above the existing `Approve` method (before line 659):
```go
// approvalDecision is the pure branching logic behind Approve: given the
// record's current approval status and two configuration/authorization
// facts, it decides whether the approval may proceed. Kept side-effect-free
// so every branch is unit-testable without a database.
func approvalDecision(status string, anyApproverConfigured, callerIsApprover bool) error {
	switch status {
	case "approved":
		return ErrAlreadyApproved
	case "pending":
		if !anyApproverConfigured {
			return ErrNoApproverConfigured
		}
		if !callerIsApprover {
			return ErrNotApprover
		}
		return nil
	default:
		return ClientError{Msg: "This record is not pending approval."}
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./crmstore/... -run TestApprovalDecision -v`
Expected: PASS (all 7 subtests).

- [ ] **Step 5: Commit**

```bash
git add crmstore/relational_store.go crmstore/relational_store_test.go
git commit -m "feat(crm): add pure approvalDecision function with table-driven tests"
```

---

## Task 3: Wire `approvalDecision` into `Approve`, add `IsApprover` to both stores

**Files:**
- Modify: `crmstore/store.go:56-95` (interface)
- Modify: `crmstore/relational_store.go:659-702` (Approve) and add helpers below it
- Modify: `crmstore/workflow_store.go:235-238` (stub `IsApprover`)

**Interfaces:**
- Consumes: `approvalDecision` (Task 2), `ErrAlreadyApproved`/`ErrNoApproverConfigured`/`ErrNotApprover` (Task 1), existing `s.employeeIDByIdentity`, `s.GetRecord`, `s.writeHistory`.
- Produces: `Store.IsApprover(ctx context.Context, pool *pgxpool.Pool, id, identityID string) (bool, error)` — new interface method, implemented by both `relationalStore` and `workflowStore`. Used by `controllers/crm.go` in Task 5.

- [ ] **Step 1: Add `IsApprover` to the `Store` interface**

In `crmstore/store.go`, immediately after the `Approve` method doc/signature (currently the last method, ending at line 94-95):
```go
	// Approve approves a Closed-Won customer if the caller is a configured
	// approver. DesignV1 returns ErrNotSupported.
	Approve(ctx context.Context, pool *pgxpool.Pool, id, approverIdentityID string) (*workflow.Record, error)
	// IsApprover reports whether identityID is a configured approver for record
	// id. Read-only — used to expose a canApprove flag on record reads without
	// mutating anything. DesignV1 always returns false, nil (unsupported).
	IsApprover(ctx context.Context, pool *pgxpool.Pool, id, identityID string) (bool, error)
}
```
(i.e. add the new method + its doc comment right before the interface's closing `}`.)

- [ ] **Step 2: Extract the approver-check queries as standalone helpers, and rewrite `Approve`**

Replace the current `Approve` method (`crmstore/relational_store.go:659-702`):
```go
func (s *relationalStore) Approve(ctx context.Context, pool *pgxpool.Pool, id, approverIdentityID string) (*workflow.Record, error) {
	rec, err := s.GetRecord(ctx, pool, id)
	if err != nil {
		return nil, err
	}
	if rec.WorkflowID != "customer" {
		return nil, ClientError{Msg: "Only customer records require approval."}
	}
	if rec.CoreFields["approval_status"] != "pending" {
		return nil, ClientError{Msg: "This record is not pending approval."}
	}
	empID, found := s.employeeIDByIdentity(ctx, pool, approverIdentityID)
	if !found {
		return nil, ErrNotApprover
	}
	// The approver must be configured for CUST (optionally for this exact status).
	var allowed bool
	err = pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM crm_workflow_approver a
			JOIN lkp_record_type rt ON rt.record_type_id = a.record_type_id
			JOIN customer r ON r.customer_uuid = $1
			WHERE rt.record_type_code = 'CUST'
			  AND a.approver_employee_id = $2 AND a.is_active
			  AND (a.crm_status_id IS NULL OR a.crm_status_id = r.customer_crm_status)
		)`, id, empID).Scan(&allowed)
	if err != nil {
		return nil, fmt.Errorf("check approver: %w", err)
	}
	if !allowed {
		return nil, ErrNotApprover
	}
	if _, err := pool.Exec(ctx, `
		UPDATE customer SET
			customer_is_approved = TRUE, customer_approval_status = 'approved',
			customer_approved_by = $2, customer_approved_at = NOW(),
			customer_updated_at = NOW(),
			customer_record_version = customer_record_version + 1
		WHERE customer_uuid = $1`, id, empID); err != nil {
		return nil, fmt.Errorf("approve customer record: %w", err)
	}
	s.writeHistory(ctx, pool, id, "approve", empID)
	return s.GetRecord(ctx, pool, id)
}
```

with:
```go
func (s *relationalStore) Approve(ctx context.Context, pool *pgxpool.Pool, id, approverIdentityID string) (*workflow.Record, error) {
	rec, err := s.GetRecord(ctx, pool, id)
	if err != nil {
		return nil, err
	}
	if rec.WorkflowID != "customer" {
		return nil, ClientError{Msg: "Only customer records require approval."}
	}
	status, _ := rec.CoreFields["approval_status"].(string)

	anyApproverConfigured, err := s.hasAnyActiveApprover(ctx, pool, "CUST")
	if err != nil {
		return nil, fmt.Errorf("check any approver configured: %w", err)
	}

	empID, found := s.employeeIDByIdentity(ctx, pool, approverIdentityID)
	var callerIsApprover bool
	if found {
		callerIsApprover, err = s.isConfiguredApprover(ctx, pool, id, empID)
		if err != nil {
			return nil, fmt.Errorf("check configured approver: %w", err)
		}
	}

	if err := approvalDecision(status, anyApproverConfigured, callerIsApprover); err != nil {
		return nil, err
	}

	if _, err := pool.Exec(ctx, `
		UPDATE customer SET
			customer_is_approved = TRUE, customer_approval_status = 'approved',
			customer_approved_by = $2, customer_approved_at = NOW(),
			customer_updated_at = NOW(),
			customer_record_version = customer_record_version + 1
		WHERE customer_uuid = $1`, id, empID); err != nil {
		return nil, fmt.Errorf("approve customer record: %w", err)
	}
	s.writeHistory(ctx, pool, id, "approve", empID)
	return s.GetRecord(ctx, pool, id)
}

// IsApprover reports whether identityID is a configured approver for record
// id. Unlike Approve, this never mutates state and never errors on "not an
// approver" — it's a read-only check for the caller's UI affordances.
func (s *relationalStore) IsApprover(ctx context.Context, pool *pgxpool.Pool, id, identityID string) (bool, error) {
	rec, err := s.GetRecord(ctx, pool, id)
	if err != nil {
		return false, err
	}
	if rec.WorkflowID != "customer" {
		return false, nil
	}
	empID, found := s.employeeIDByIdentity(ctx, pool, identityID)
	if !found {
		return false, nil
	}
	return s.isConfiguredApprover(ctx, pool, id, empID)
}

// hasAnyActiveApprover reports whether at least one active approver is
// configured for recordTypeCode (e.g. "CUST"), regardless of caller or status.
func (s *relationalStore) hasAnyActiveApprover(ctx context.Context, pool *pgxpool.Pool, recordTypeCode string) (bool, error) {
	var exists bool
	err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM crm_workflow_approver a
			JOIN lkp_record_type rt ON rt.record_type_id = a.record_type_id
			WHERE rt.record_type_code = $1 AND a.is_active
		)`, recordTypeCode).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check any approver: %w", err)
	}
	return exists, nil
}

// isConfiguredApprover reports whether empID is configured (and active) as an
// approver for record id, at its current record type + status.
func (s *relationalStore) isConfiguredApprover(ctx context.Context, pool *pgxpool.Pool, id string, empID int) (bool, error) {
	var allowed bool
	err := pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM crm_workflow_approver a
			JOIN lkp_record_type rt ON rt.record_type_id = a.record_type_id
			JOIN customer r ON r.customer_uuid = $1
			WHERE rt.record_type_code = 'CUST'
			  AND a.approver_employee_id = $2 AND a.is_active
			  AND (a.crm_status_id IS NULL OR a.crm_status_id = r.customer_crm_status)
		)`, id, empID).Scan(&allowed)
	if err != nil {
		return false, fmt.Errorf("check approver: %w", err)
	}
	return allowed, nil
}
```

- [ ] **Step 3: Add the DesignV1 stub `IsApprover`**

In `crmstore/workflow_store.go`, immediately after the existing `Approve` stub (lines 235-238):
```go
// Approve is not part of the DesignV1 workflow model.
func (s *workflowStore) Approve(ctx context.Context, pool *pgxpool.Pool, id, approverIdentityID string) (*workflow.Record, error) {
	return nil, ErrNotSupported
}

// IsApprover is not part of the DesignV1 workflow model; always false, not an
// error, since this is a read-only UI-affordance check, not an action.
func (s *workflowStore) IsApprover(ctx context.Context, pool *pgxpool.Pool, id, identityID string) (bool, error) {
	return false, nil
}
```

- [ ] **Step 4: Compile-check both store implementations satisfy the interface**

Run: `go build ./crmstore/...`
Expected: builds clean (the `var _ Store = (*relationalStore)(nil)` assertion in `relational_store.go:23` and the analogous one in `workflow_store.go`, if present, will fail to compile otherwise — confirm both files have such an assertion; if `workflow_store.go` lacks one, that's fine, `go build` still fails to compile the package if either concrete type used as a `Store` doesn't satisfy it — check via `go vet ./crmstore/...` too).

Run: `go vet ./crmstore/...`
Expected: no errors.

- [ ] **Step 5: Run the full existing crmstore test suite to confirm no regressions**

Run: `go test ./crmstore/... -v`
Expected: PASS — all prior tests plus the new `TestApprovalDecision` and `TestApprovalSentinelErrorsAreDistinct`.

- [ ] **Step 6: Commit**

```bash
git add crmstore/store.go crmstore/relational_store.go crmstore/workflow_store.go
git commit -m "feat(crm): wire approvalDecision into Approve, add IsApprover to Store interface"
```

---

## Task 4: HTTP error mapping for the two new sentinels

**Files:**
- Modify: `controllers/crm.go:204-221` (`crmFail`)

**Interfaces:**
- Consumes: `crmstore.ErrAlreadyApproved`, `crmstore.ErrNoApproverConfigured` (Task 1).
- Produces: nothing new consumed elsewhere — this is the terminal HTTP-mapping layer.

- [ ] **Step 1: Edit `crmFail`**

Current (`controllers/crm.go:204-221`):
```go
// crmFail maps a store error to an HTTP response (400 for client errors).
func crmFail(w http.ResponseWriter, err error, serverMsg string) {
	switch {
	case errors.Is(err, crmstore.ErrRecordNotFound):
		fail(w, http.StatusNotFound, "Record not found.")
	case errors.Is(err, crmstore.ErrNotApprover):
		fail(w, http.StatusForbidden, err.Error())
	case crmstore.IsClientError(err):
		fail(w, http.StatusBadRequest, err.Error())
	default:
		var ife *query.InvalidFilterError
		if errors.As(err, &ife) {
			fail(w, http.StatusBadRequest, ife.Error())
			return
		}
		fail(w, http.StatusInternalServerError, serverMsg)
	}
}
```

Replace with:
```go
// crmFail maps a store error to an HTTP response (400 for client errors).
func crmFail(w http.ResponseWriter, err error, serverMsg string) {
	switch {
	case errors.Is(err, crmstore.ErrRecordNotFound):
		fail(w, http.StatusNotFound, "Record not found.")
	case errors.Is(err, crmstore.ErrNotApprover):
		fail(w, http.StatusForbidden, err.Error())
	case errors.Is(err, crmstore.ErrAlreadyApproved):
		fail(w, http.StatusConflict, err.Error())
	case errors.Is(err, crmstore.ErrNoApproverConfigured):
		fail(w, http.StatusConflict, err.Error())
	case crmstore.IsClientError(err):
		fail(w, http.StatusBadRequest, err.Error())
	default:
		var ife *query.InvalidFilterError
		if errors.As(err, &ife) {
			fail(w, http.StatusBadRequest, ife.Error())
			return
		}
		fail(w, http.StatusInternalServerError, serverMsg)
	}
}
```

- [ ] **Step 2: Build to confirm no syntax errors**

Run: `go build ./controllers/...`
Expected: builds clean.

- [ ] **Step 3: Commit**

```bash
git add controllers/crm.go
git commit -m "fix(crm): map ErrAlreadyApproved/ErrNoApproverConfigured to 409"
```

---

## Task 5: Security-event logging on unauthorized approval attempts + `canApprove` on GET

**Files:**
- Modify: `controllers/crm.go:353-365` (`GetRecord`)
- Modify: `controllers/crm.go:498-518` (`ApproveRecord`)

**Interfaces:**
- Consumes: `st.IsApprover` (Task 3), `logSecurityEvent` (existing, `controllers/security_log.go:19`).
- Produces: `"canApprove"` key in the JSON body of `GET /api/tenant/crm/records/{id}` when the record is a customer record — the field the frontend repo will read.

- [ ] **Step 1: Update `GetRecord` to add `canApprove`**

Current (`controllers/crm.go:353-365`):
```go
// GetRecord GET /api/tenant/crm/records/{id}
func (h *CRMOps) GetRecord(w http.ResponseWriter, r *http.Request) {
	st, pool, _, _, ok := h.authCRMByRecordID(w, r, r.PathValue("id"), authz.ActionRead)
	if !ok {
		return
	}
	rec, err := st.GetRecord(r.Context(), pool, r.PathValue("id"))
	if err != nil {
		crmFail(w, err, "Failed to load record.")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "record": rec})
}
```

Replace with:
```go
// GetRecord GET /api/tenant/crm/records/{id}
func (h *CRMOps) GetRecord(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	st, pool, key, identityID, ok := h.authCRMByRecordID(w, r, id, authz.ActionRead)
	if !ok {
		return
	}
	rec, err := st.GetRecord(r.Context(), pool, id)
	if err != nil {
		crmFail(w, err, "Failed to load record.")
		return
	}
	resp := map[string]any{"success": true, "record": rec}
	if key == "customer" {
		canApprove, err := st.IsApprover(r.Context(), pool, id, identityID)
		if err != nil {
			crmFail(w, err, "Failed to load record.")
			return
		}
		resp["canApprove"] = canApprove
	}
	writeJSON(w, http.StatusOK, resp)
}
```

- [ ] **Step 2: Add security-event logging to `ApproveRecord`**

Current (`controllers/crm.go:498-518`):
```go
// ApproveRecord POST /api/tenant/crm/records/{id}/approve
// Approves a Closed-Won customer if the caller is a configured approver. Only
// supported on the v2 design; v1 returns 400 (not supported).
func (h *CRMOps) ApproveRecord(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	st, pool, key, identityID, ok := h.authCRMByRecordID(w, r, id, authz.ActionUpdate)
	if !ok {
		return
	}
	rec, err := st.Approve(r.Context(), pool, id, identityID)
	if errors.Is(err, crmstore.ErrNotSupported) {
		fail(w, http.StatusBadRequest, "Approval is not available for this workspace's design.")
		return
	}
	if err != nil {
		crmFail(w, err, "Failed to approve record.")
		return
	}
	auditCRM(r, pool, identityID, "approve", key, id, nil, rec)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "record": rec})
}
```

Replace with:
```go
// ApproveRecord POST /api/tenant/crm/records/{id}/approve
// Approves a Closed-Won customer if the caller is a configured approver. Only
// supported on the v2 design; v1 returns 400 (not supported).
func (h *CRMOps) ApproveRecord(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	st, pool, key, identityID, ok := h.authCRMByRecordID(w, r, id, authz.ActionUpdate)
	if !ok {
		return
	}
	rec, err := st.Approve(r.Context(), pool, id, identityID)
	if errors.Is(err, crmstore.ErrNotSupported) {
		fail(w, http.StatusBadRequest, "Approval is not available for this workspace's design.")
		return
	}
	if errors.Is(err, crmstore.ErrNotApprover) {
		logSecurityEvent(r, "approval_denied", "identity", identityID, "record", id)
	}
	if err != nil {
		crmFail(w, err, "Failed to approve record.")
		return
	}
	auditCRM(r, pool, identityID, "approve", key, id, nil, rec)
	writeJSON(w, http.StatusOK, map[string]any{"success": true, "record": rec})
}
```

- [ ] **Step 3: Build**

Run: `go build ./controllers/...`
Expected: builds clean. (`id` was already a local var in `GetRecord`'s original single-use form via `r.PathValue("id")` called twice; the rewrite introduces one `id :=` and reuses it — verify no "declared and not used" errors.)

- [ ] **Step 4: Run full test suite**

Run: `go test ./... `
Expected: PASS, no regressions.

- [ ] **Step 5: Commit**

```bash
git add controllers/crm.go
git commit -m "feat(crm): expose canApprove on record reads, log approval_denied security event"
```

---

## Task 6: Full verification pass

**Files:** none (verification only)

- [ ] **Step 1: Run the full test suite**

Run: `go test ./... -v 2>&1 | tail -100`
Expected: all packages PASS, specifically `crmstore` and `controllers`.

- [ ] **Step 2: Run `go vet` and `go build` across the whole module**

Run: `go build ./... && go vet ./...`
Expected: clean, no errors.

- [ ] **Step 3: Run golangci-lint if available**

Run: `golangci-lint run ./crmstore/... ./controllers/...`
Expected: no new lint findings introduced by this change. (If `golangci-lint` isn't installed, skip this step and note it in the final report.)

- [ ] **Step 4: Manual sanity check of the five scenarios against the spec table**

Re-read `docs/superpowers/specs/2026-07-06-approval-validation-messages-design.md` scenario table and confirm each row is covered by a task above:
1. Unauthorized approver → Task 3 (`approvalDecision`) + Task 4 (403 mapping, pre-existing) + Task 5 (security log). ✓
2. Non-approver views → Task 5 (`canApprove: false` in `GetRecord`). ✓
3. Assigned approver → Task 3 (`approvalDecision` returns nil) + Task 5 (`canApprove: true`). ✓
4. Already approved → Task 1 (`ErrAlreadyApproved`) + Task 3 (`approvalDecision`) + Task 4 (409 mapping). ✓
5. No approver configured → Task 1 (`ErrNoApproverConfigured`) + Task 3 (`hasAnyActiveApprover` + `approvalDecision`) + Task 4 (409 mapping). ✓

- [ ] **Step 5: No commit needed for this task** (verification only — if any step fails, fix in the relevant task's files and re-commit there, don't create a new "fix everything" commit).

---

## Notes for the executing engineer

- This plan does not touch the frontend repo. `canApprove` and the exact message strings are the contract the frontend team consumes — don't rename them without checking with them first.
- `Approve()`'s early guard `if rec.WorkflowID != "customer"` and the pre-existing "not a customer, only customer records need approval" `ClientError` message are unchanged — they're for a different, non-spec'd scenario (calling approve on a lead/prospect record) and stay as-is.
- Do not add a DB integration test harness as part of this work — `crmstore` has none today (verified during design), and `approvalDecision` was deliberately extracted to be pure so it doesn't need one.
