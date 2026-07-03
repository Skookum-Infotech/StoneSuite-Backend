---
name: new-tenant-endpoint
description: Scaffold a tenant-scoped backend HTTP endpoint with the mandatory security chain (TenantResolver, permission check, scope filter, IDOR guard, security logging). Use when adding any handler under /api/tenant/ in StoneSuite Backend.
---

# Adding a tenant-scoped endpoint

Every `/api/tenant/` route in StoneSuite Backend must go through the same security
chain. Skipping any link is a security bug (cross-tenant leak, permission bypass, or
IDOR). Follow this exactly; copy from the canonical examples in
`controllers/workflow.go`.

## Checklist (make a TodoWrite item per step)

1. **Pick resource + action + scope-shape.** Decide the `authz.Resource` and
   `authz.Action` the endpoint enforces, and whether it's a list/read (apply scope
   filter) or single-record access (apply IDOR guard) or a mutation (require + then act).
   Add the resource/action to the `authz` catalog if new.

2. **Write the handler** as `func (h *XOps) Name(w http.ResponseWriter, r *http.Request)`.
   First line is always authorization — use the `authorize` helper, which resolves the
   tenant pool, checks the permission, and returns the caller's scope:

   ```go
   func (h *XOps) GetThing(w http.ResponseWriter, r *http.Request) {
       pool, scope, identityID, ok := h.authorize(w, r, authz.ResourceX, authz.ActionRead)
       if !ok {
           return // authorize already wrote 401/403/500
       }
       // ... handler body ...
   }
   ```
   `authorize` (controllers/workflow.go) does: `middleware.GetUserFromContext` →
   `tenancy.PoolFromContext` → `authz.Check(...)`. Never re-implement it; never read
   the body before authorizing.

3. **For a MUTATION (POST/PATCH/DELETE/transition):** the `authorize` call IS the
   `Require(resource, action)` check — it must precede any write. No mutation path may
   skip it.

4. **For a SINGLE-record GET/PATCH/DELETE/transition:** after loading the record,
   enforce ownership scope (IDOR guard) BEFORE returning or mutating it:
   ```go
   rec, err := workflow.GetRecord(r.Context(), pool, r.PathValue("id"))
   // ... map ErrRecordNotFound -> 404, other err -> 500 ...
   if !h.enforceRecordScope(w, r, pool, scope, identityID, rec, authz.ActionRead) {
       return // returns 404 (NOT 403) on scope denial + logs idor_denied
   }
   ```
   `enforceRecordScope` calls `recordInScope(...)` (controllers/scope.go) and on denial
   returns **404, not 403**, so ids can't be enumerated. CRM handlers get this via
   `authCRMByRecordID` instead.

5. **For a LIST/read:** apply scope filtering — never an unscoped query. Resolve the
   caller's user/team via `callerScope(...)` and pass the scope into the store
   (`ListRecordsFiltered` / `SearchRecords`) so the RBAC clause is ANDed on. Do not
   filter in Go after fetching everything.

6. **Validate input** with `json.NewDecoder(r.Body).Decode(&req)`; on error
   `fail(w, http.StatusBadRequest, "Invalid request body.")`. For custom_fields,
   validate against `workflow_field_definitions` before save (see the `/add-migration`
   and customFields validators).

7. **Respond** only via the helpers in `controllers/tenant.go`:
   `writeJSON(w, http.StatusOK, map[string]any{"success": true, "record": rec})` on
   success, `fail(w, status, "...")` on error. Every response is
   `{success: bool, message?: string, ...}`.

8. **Register the route** in `main.go` using `tenantChain`, which wires
   `RequireAuth → per-tenant rate limit → resolver.Middleware`:
   ```go
   mux.Handle("GET /api/tenant/things/{id}", tenantChain(xOps.GetThing))
   ```
   Put more specific patterns before catch-alls. Never register a `/api/tenant/` route
   without `tenantChain` (or the equivalent `RequireAuth + resolver.Middleware`).

9. **Log security events** for denials: `enforceRecordScope` already logs
   `idor_denied`. For other notable events use `logSecurityEvent(r, "<event>", kv...)`.
   Never log passwords or raw tokens.

10. **Test** — table-driven where pure. At minimum cover: no-permission → 403,
    out-of-scope single record → 404 (not 403), happy path → 200. Run
    `go test ./... && go vet ./...`.

## Red flags (stop and fix)
- A `/api/tenant/` handler that reads `r.Body` before `authorize`.
- A single-record handler with no `enforceRecordScope` / `authCRMByRecordID`.
- A 403 (instead of 404) returned on scope denial.
- A list handler that fetches all rows then filters in Go.
- A route registered without `tenantChain`.
