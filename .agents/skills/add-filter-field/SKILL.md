---
name: add-filter-field
description: Make a new field filterable or sortable in StoneSuite Backend's Record Filter Engine without breaking its security invariants. Use when extending record search/filter on workflow records or CRM records.
---

# Adding a filterable/sortable field

All record filtering goes through the store-agnostic `query` package and each
store's `FieldResolver`. Adding a field means teaching the resolver(s) a new whitelisted
key — never hand-rolling SQL. The filter-engine invariants are security-critical: a
mistake can turn a filter into a cross-scope leak or SQL injection. Preserve them.

## Where things live
- `query/` — store-agnostic builder, cursor, `FieldResolver` interface,
  `InvalidFilterError`, `MaxLimit`(100)/`DefaultLimit`(25). Imports nothing app-specific.
- `workflow/filter.go` — `recordResolver` for v1 JSONB records (`systemFields`
  map + `cf:`/`core:` namespaces, `jsonbExpr`).
- `crmstore/relational_filter.go` — resolver for v2 relational CRM records
  (`validCustomKey`, `customerFields` registry).
- Endpoints: `POST /api/tenant/workflows/{id}/records/search` (controllers/workflow.go),
  `POST /api/tenant/crm/{workflowKey}/records/search` (controllers/crm.go).

## Checklist (make a TodoWrite item per step)

1. **Decide the key namespace** the frontend will send (design-agnostic):
   - bare system key (`id`, `created_at`, `status`, `owner_user_id`, ...)
   - `cf:<key>` — a workflow custom field (must exist in `workflow_field_definitions`)
   - `core:<key>` — a v1 JSONB core field
   Each store's resolver maps this logical key to its own schema.

2. **Add to the resolver(s).** For a new system field in v1, add an entry to
   `systemFields` in `workflow/filter.go` with a SQL expression and `query.DataType`.
   For CRM, map it via the `customerFields` registry / `relational_filter.go`. Custom
   and core keys are handled generically — you usually don't touch the resolver for those.

3. **Keep keys on the whitelist.** A key that doesn't resolve MUST return
   `(.., .., false)` so the builder produces `*query.InvalidFilterError` (→ 400). Never
   let an unresolved client key reach SQL. Custom/core keys are interpolated ONLY after
   passing the identifier regex `^[a-z][a-z0-9_]{0,62}$` (`validFieldKey` /
   `validCustomKey`). Confirm any new interpolation path is regex-gated.

4. **Never interpolate values.** All client values are bound as `$n` by
   `query.Build(req, resolver, startIdx)`. Only validated identifiers may be
   interpolated into expressions (see `jsonbExpr`).

5. **Preserve filter ⨯ scope = AND.** The RBAC scope clause and the user filter are
   ANDed so a filter can only narrow the caller's permitted set. Do not introduce any
   `OR` that joins scope with filter. The scope-composition tests in
   `workflow/filter_test.go` MUST stay green.

6. **Sorting is restricted.** Only `created_at`, `updated_at`, `record_number` are
   sortable (stable, non-null → NULL-safe keyset comparison). Do NOT add a custom
   field / name / id as sortable without first solving NULL ordering for the keyset
   cursor — this is deliberately deferred.

7. **Pagination stays keyset.** No `OFFSET`, no `COUNT(*)`. Stores fetch `EffLimit+1`
   to set `hasMore` and mint `query.NextCursor`. Limit clamps to `MaxLimit`.

8. **Error mapping.** Ensure `*query.InvalidFilterError` maps to 400 at the controller
   (it already does in `crmFail` and the workflow search handler) — a bad filter is
   never a 500.

9. **Add tests** in `workflow/filter_test.go` / `crmstore/relational_filter_test.go` /
   `query/query_test.go`: the new key resolves; an unknown key 400s; scope-AND holds.
   Run `go test ./query/... ./workflow/... ./crmstore/...`.

## Optional: run the checker
After editing, run the `filter-invariant-checker` agent on the change.
