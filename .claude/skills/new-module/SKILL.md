---
name: new-module
description: Scaffold a new relational business module (like quote, estimate, payment, invoice, salesorder) end-to-end in StoneSuite Backend — package, controllers, routes, RBAC catalog, and schema. Use when adding a new document/record type with its own tables, status transitions and approval flow.
---

# Adding a new business module

A module is ~23 touch points and about 95% mechanical: `payment/`, `quote/`,
`estimate/`, `invoice/` and `salesorder/` are near-literal clones of each other
with **no shared abstraction** — no generic `Module[T]`, no store interface, no
route-registration helper. The four shared files you edit are append-only and
non-conflicting, so a module is cheap to add.

**Do not `sed`-clone an existing module.** Cloning is how the current drift got
here: `quote/` still uses estimate's `est` table alias, `quote/transitions.go`
says *"converting the quote into a quote"*, and both `quote` and `estimate`
silently dropped invoice's nil-guard on `custom_fields` (every PATCH omitting
it returned 500 until it was fixed) and the `inventory_item_unit_id` column in
their test seed. Work through the checklist instead; it encodes the corrected
skeleton.

Pick your reference module by shape, not by recency:
- **money + lines + approval + conversion** → `estimate/` or `quote/`
- **money + applications against another doc** → `payment/`
- **the most correct auth skeleton** → `controllers/payment.go` (it is the only
  one that logs `permission_denied`)

For file-by-file anatomy and the auth skeleton, read
`references/module-anatomy.md`. For the four shared-file edits, read
`references/wiring.md`. Read them when you reach the steps that cite them —
not up front.

## Checklist (make a TodoWrite item per step)

1. **Write the design spec first.** `docs/superpowers/specs/YYYY-MM-DD-<mod>-module-design.md`,
   following the house template (see any existing spec). It must open with
   "What already exists (reuse, do not recreate)" and justify every new table.
   Most modules need no new lookup tables — `lkp_record_type`,
   `lkp_record_status`, `lkp_unit`, `lkp_currency` already exist.

2. **Schema.** Add ~6 tables to `database/migrations/tenant/schema.sql`
   (header, `_item`, `_history`, `_approver`, `_approval`, optional
   `_conversion`) + partial indexes + 2 seed stanzas. See
   `references/wiring.md`. Follow `/add-migration`'s rules — every statement
   must be idempotent and safe to re-run.

3. **Pure logic first, with table-driven tests** (stdlib `testing`, no testify —
   that is this repo's convention for pure functions):
   `calc.go` / `money.go`, `numbering.go`, `transitions.go`, `resolver.go`.
   These need no database and are the cheapest place to be correct.

4. **Types.** `types.go` — `CreateXInput`, `UpdateXInput`, a shared `xFields`
   embed, `Line`, the `X` response, `Page`.

5. **Store.** `store.go` (shared helpers + `Get`) then one file per verb:
   `store_create.go`, `store_update.go`, `store_search.go`,
   `store_transition.go`. Keep them split — `salesorder/store.go` is 42KB
   unsplit and is the counter-example, not the model.

6. **Approval.** `approval.go` — mirror `estimate/approval.go` (AD-8).

7. **Controllers.** `controllers/<mod>.go` + `controllers/<mod>_audit.go`.
   Copy the auth skeleton from `references/module-anatomy.md`, NOT from
   quote/estimate. Every handler needs `authX(...)`; every single-record
   handler needs `authXByUUID(...)`.

8. **Wire the routes.** `main.go` — one constructor + ~9 `mux.Handle` lines
   inside the tenant block, each wrapped in `tenantChain` (main.go:370). See
   `references/wiring.md`.

9. **RBAC catalog.** `authz/catalog.go` — one `Resource` const + 5 rows
   (create/read/update/delete/transition). A permission that is not in the
   catalog cannot be granted.

10. **Generic router map (only if the JSONB router should serve it too).**
    `controllers/crm.go` `resourceForKey` — one `case`.
    `controllers/rbac_catalog_drift_test.go` asserts every mapped resource has
    all 5 CRM actions, so this is enforced for the generic router only.

11. **DB-backed tests.** `<mod>/store_test.go` with `//go:build dbtest` on line
    1 **and** the `TEST_DATABASE_URL` skip guard — both layers, matching
    `payment/store_test.go`. These do not compile into `go test ./...`; CI runs
    them in the `schema-apply` job.

12. **Verify.**
    ```bash
    go build ./... && go vet ./... && go test ./...
    ```
    Then, against a real database (the only thing that proves the store):
    ```bash
    docker run --rm -d -p 5433:5432 -e POSTGRES_PASSWORD=pg \
      --name ss-dev pgvector/pgvector:pg16
    psql "postgres://postgres:pg@localhost:5433/postgres" --single-transaction \
      -v ON_ERROR_STOP=1 -f database/migrations/tenant/schema.sql
    TEST_DATABASE_URL="postgres://postgres:pg@localhost:5433/postgres" \
      go test -tags dbtest ./<mod>/...
    ```
    `pgvector`, not stock `postgres` (the RAG tables need `CREATE EXTENSION
    vector`). `--single-transaction`, because that is what `tx.Exec` does —
    without it the `ON COMMIT DROP` temp tables vanish between statements and
    you get a false failure. **Give each package its own fresh database**:
    several dbtests assert on the absence of global config, so a shared
    database makes them order-dependent.

13. **Review.** Run `module-drift-checker`, then `tenancy-security-reviewer`,
    then `migration-auditor` on the diff.

## Red flags (stop and fix)

- **Any handler that skips `authX`.** No exceptions.
- **A single-record GET/PATCH/DELETE/transition without `authXByUUID`.** That
  is an IDOR hole. On scope denial return **404, not 403**, so ids cannot be
  enumerated.
- **`authX` that does not `logSecurityEvent(r, "permission_denied", ...)`.**
  Only `payment.go` gets this right; quote/estimate/invoice do not. Copy
  payment.
- **A source module's table alias or comments left behind.** Grep your new
  package for the reference module's name before you commit.
- **`in.CustomFields` passed straight into the update.** The column is
  `NOT NULL DEFAULT '{}'`; a nil map encodes as SQL NULL and the PATCH 500s.
  Guard it: `if custom == nil { custom = map[string]any{} }`.
- **Hand-rolled record filtering.** Everything goes through `query/` via
  `resolver.go`. An unresolved field key is a 400, never raw SQL.
- **Appending a `lkp_record_type` out of order.** The `lkp_record_status` seed
  (schema.sql, "-- 14. lkp tables") keys statuses to record types by
  **hardcoded integers**, relying on `SERIAL` assignment order. Insert out of
  order and every downstream status is silently mis-assigned. Append only.
- **A new module package with zero tests.** `vendors/` is the only one, and it
  is not a precedent.
