# Module anatomy

## The 16 files in a module package

Compare against `estimate/` or `payment/`. Roles:

| File | Role | Pure? |
|---|---|---|
| `types.go` | Wire DTOs: `CreateXInput`, `UpdateXInput`, shared `xFields` embed, `Line`, `X` response, `Page` | yes |
| `calc.go` (`money.go` in payment) | `ComputeLine`, `ComputeHeader` — money math | **yes** |
| `numbering.go` | `numberPrefix` const + `FormatNumber(serialID)` → `QUOT-000001` | **yes** |
| `transitions.go` | static `allowedTransitions` map, `CanTransition`, `ValidateTransition`, `ErrInvalidTransition` | **yes** |
| `resolver.go` | `filterFields` / `sortFields` whitelists for the `query` engine, custom-field escape hatch, global-search SQL | **yes** |
| `approval.go` | AD-8 gate: `approverCount`, `signOffCount`, `ErrNotApprover`, `ErrApprovalRequired`, `ErrApprovalNotRequired` | mixed |
| `store.go` | shared helpers + `Get`: `ClientError`, `buildInsert`, `buildUpdateSet`, `recordTypeIDByCode`, `statusIDByCode`, `customerSnapshot`, `resolveInventoryItem`, `scanX`, `loadLines` | no |
| `store_create.go` / `store_update.go` / `store_search.go` / `store_transition.go` | one verb each | no |
| `*_test.go` (5) | calc, numbering, resolver, transitions (stdlib, table-driven) + store (`//go:build dbtest`) | — |

The four pure files are where correctness is cheap. Test them table-driven with
stdlib `testing` — 67 of 82 test files use stdlib only; testify appears mainly
in `controllers/` and `authz/`.

Per-domain variance is normal: payment swaps `calc.go` → `money.go` and adds
`apply.go` / `quickpay.go`; salesorder adds `allocation.go`; invoice adds
`store_line_resolve.go`.

## The controller auth skeleton

`controllers/payment.go` is the reference — it is the **only** module that logs
`permission_denied`. Copy from it, not from quote/estimate.

### `authX` — resource/action half

```go
func (h *XOps) authX(w http.ResponseWriter, r *http.Request, action authz.Action) (*pgxpool.Pool, string, authz.Scope, bool) {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return nil, "", "", false
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return nil, "", "", false
	}
	decision, err := authz.Check(r.Context(), pool, payload.ID, authz.ResourceX, action)
	if err != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, "", "", false
	}
	if !decision.Allowed {
		// REQUIRED. quote/estimate/invoice omit this; payment does not.
		logSecurityEvent(r, "permission_denied",
			"identity", payload.ID, "resource", string(authz.ResourceX), "action", string(action))
		fail(w, http.StatusForbidden, "You do not have permission to "+string(action)+" xs.")
		return nil, "", "", false
	}
	return pool, payload.ID, decision.Scope, true
}
```

### `authXByUUID` — row-level half, the IDOR guard

```go
func (h *XOps) authXByUUID(w http.ResponseWriter, r *http.Request, uuid string, action authz.Action) (*pgxpool.Pool, string, authz.Scope, bool) {
	pool, identityID, scope, ok := h.authX(w, r, action)
	if !ok {
		return nil, "", "", false
	}
	if scope == authz.ScopeAll {
		return pool, identityID, scope, true
	}
	rec, err := x.Get(r.Context(), pool, uuid)
	if errors.Is(err, x.ErrNotFound) {
		fail(w, http.StatusNotFound, "X not found.")
		return nil, "", "", false
	}
	if err != nil {
		fail(w, http.StatusInternalServerError, "Failed to load x.")
		return nil, "", "", false
	}
	allowed, aerr := recordInScope(r.Context(), pool, scope, identityID, rec.OwnerUserID, rec.TeamID)
	if aerr != nil {
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return nil, "", "", false
	}
	if !allowed {
		logSecurityEvent(r, "idor_denied",
			"identity", identityID, "record", uuid, "resource", string(authz.ResourceX),
			"action", string(action), "scope", string(scope))
		// 404, NOT 403 — a 403 confirms the id exists and lets it be enumerated.
		fail(w, http.StatusNotFound, "X not found.")
		return nil, "", "", false
	}
	return pool, identityID, scope, true
}
```

**On `recordInScope`'s signature:** it is
`recordInScope(ctx, pool, scope, identityID, ownerUserID)` — five arguments, **no
`teamID`**. The team RBAC scope was retired in `2dd211f`; the model is now
two-level (`all` / `own`) and fail-closed, so an unrecognized scope narrows to
owner-only. Older notes describing a sixth `teamID` argument are stale — passing
one will not compile. `TeamID` still exists as a *record data field*
(`crmstore/store.go`, `ai/chunk.go`); that is unrelated to RBAC scope.

### `xFail` — error → status mapping

```go
func xFail(w http.ResponseWriter, err error, msg string) {
	switch {
	case errors.Is(err, x.ErrNotFound):
		fail(w, http.StatusNotFound, "X not found.")
	case errors.Is(err, x.ErrInvalidTransition), errors.Is(err, x.ErrApprovalRequired):
		fail(w, http.StatusConflict, err.Error())
	case errors.Is(err, x.ErrNotApprover):
		fail(w, http.StatusForbidden, err.Error())
	case x.IsClientError(err):
		fail(w, http.StatusBadRequest, err.Error())
	default:
		var ife *query.InvalidFilterError
		if errors.As(err, &ife) {
			fail(w, http.StatusBadRequest, ife.Error()) // 400, never 500
			return
		}
		fail(w, http.StatusInternalServerError, msg)
	}
}
```

Handlers: `List`, `Search`, `Create`, `Get`, `Update`, `Delete`, `Transition`,
`Approve`, plus `Audit` in `<mod>_audit.go`.

### Scope filtering on list/read

Never filter in Go. Pass the scope into the store and let SQL do it:

```go
x.Search(ctx, pool, string(scope), identityID, req)   // store ANDs owner_id = $N when scope != all
```

Filter ⨯ scope is **ANDed, never OR** — a user filter can only narrow the
caller's permitted set.

### A note on `Approve`

Existing modules enforce `authz.ActionTransition` on `Approve`, not
`ActionApprove`. That is deliberate, not a bug: `ActionApprove` exists
(`authz/catalog.go:66`) but is granted only to `{ResourceRecord,
ActionApprove}`, so document modules cannot grant it independently. Follow the
existing convention unless you are also extending the catalog — and if you do,
raise it as a decision rather than a drive-by change.
