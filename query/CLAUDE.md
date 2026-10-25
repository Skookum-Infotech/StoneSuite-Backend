# Record Filter Engine — Claude rules

_Loaded automatically when Claude reads files under `query/`. Moved verbatim from the root CLAUDE.md._

## Record Filter Engine (`query/`)
The `query` package is the **single, store-agnostic** way to do server-side filtering, sorting, and keyset pagination on records. Both record-list designs (v1 JSONB `workflow.ListRecordsFiltered`, v2 relational `relationalStore.SearchRecords`) route through it. Do not hand-roll record filtering elsewhere.
1. **Filter ⨯ scope is ANDed, never OR.** The RBAC scope clause and the user filter compose with `AND` — a filter can only narrow the caller's permitted set, never widen it. Keep `workflow/filter_test.go` green.
2. **Field keys are a whitelist via `query.FieldResolver`.** An unresolved key is `400` (`*query.InvalidFilterError`), never raw SQL.
3. **All values are parameterized** (`$n`). Never interpolate client values.
4. **Pagination is keyset (opaque base64 cursor), not offset.** Page size caps at `MaxLimit` (100), default 25.
5. **Sortable fields are restricted** to stable non-null columns (`created_at`, `updated_at`, `record_number`).
6. **Map errors correctly.** `*query.InvalidFilterError` → 400, never a 500.
7. **`query` imports nothing app-specific** — keep it dependency-free.
