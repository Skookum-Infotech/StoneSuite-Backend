# Wiring a module: the four shared-file edits

All four are append-only and non-conflicting. That is why a module is cheap to
add — and also why nothing stops you forgetting one. A module missing its
catalog rows compiles, boots, and 403s at runtime.

## 1. `main.go` — routes

One constructor + ~9 `mux.Handle` lines inside the tenant block. Copy the
payment block (main.go:569) for shape:

```go
xOps := controllers.NewXOps()
mux.Handle("GET    /api/tenant/xs",                  tenantChain(xOps.List))
mux.Handle("POST   /api/tenant/xs/search",           tenantChain(xOps.Search))
mux.Handle("POST   /api/tenant/xs",                  tenantChain(xOps.Create))
mux.Handle("GET    /api/tenant/xs/{uuid}",           tenantChain(xOps.Get))
mux.Handle("PATCH  /api/tenant/xs/{uuid}",           tenantChain(xOps.Update))
mux.Handle("DELETE /api/tenant/xs/{uuid}",           tenantChain(xOps.Delete))
mux.Handle("POST   /api/tenant/xs/{uuid}/transition",tenantChain(xOps.Transition))
mux.Handle("POST   /api/tenant/xs/{uuid}/approve",   tenantChain(xOps.Approve))
mux.Handle("GET    /api/tenant/xs/{uuid}/audit",     tenantChain(xOps.Audit))
```

`tenantChain` (main.go:370) is
`middleware.RequireAuth(tenantRateLimiter.PerTenant(resolver.Middleware(h)))`.

**Every tenant route must go through `tenantChain`.** A route registered bare
has no auth, no rate limit, and — critically — no TenantResolver, so
`tenancy.PoolFromContext` fails and the handler cannot reach any database. That
is the single most dangerous thing to get wrong here.

There is no DI container, no registry, no interface to satisfy. If your module
touches another (payment registers `GET /api/tenant/invoices/{uuid}/payments`),
add that route next to the owning module's block and note it in the spec.

## 2. `authz/catalog.go` — RBAC

Two edits in one file. A `Resource` const in the grouped block:

```go
ResourceX Resource = "x"
```

and 5 rows in `var catalog = []Permission{...}`:

```go
{ResourceX, ActionCreate},
{ResourceX, ActionRead},
{ResourceX, ActionUpdate},
{ResourceX, ActionDelete},
{ResourceX, ActionTransition},
```

`validResources` / `validActions` derive automatically from `catalog`, so this
is the only place to register. Permissions are stored per-tenant (`roles`,
`role_permissions`, `user_roles`) and resolved by `authz.Check` against the
tenant pool. Wildcards (`ResourceAny` / `ActionAny`) are seed-only and rejected
by `IsValidPermission`.

Do **not** add `{ResourceX, ActionApprove}` reflexively — see the note at the
end of `module-anatomy.md`.

## 3. `controllers/crm.go` — `resourceForKey` (optional)

Only if the generic JSONB router should also serve this resource. One `case`.
`controllers/rbac_catalog_drift_test.go` AST-parses this switch and asserts
every mapped resource has all 5 CRM actions in the catalog — so drift is caught
here automatically, but **only for the generic router**, never for the
relational modules.

## 4. `database/migrations/tenant/schema.sql` — tables

Single canonical 3.5k-line file, `go:embed`-ed and applied **whole, in one
`tx.Exec`**, under an advisory lock, on every boot and every provision
(`database/tenant_migrations.go:34`). There are no numbered migrations and no
down files: editing the file IS the migration. One syntax error anywhere and no
tenant gets a schema.

Per module: ~6 tables, ~12 partial indexes, 2 seed stanzas.

### Header table shape

```sql
CREATE TABLE IF NOT EXISTS x (
    x_id              SERIAL       PRIMARY KEY,
    x_uuid            UUID         NOT NULL DEFAULT gen_random_uuid(),
    x_number          VARCHAR(20)  NOT NULL,
    record_type       INTEGER      NOT NULL REFERENCES lkp_record_type(record_type_id),
    x_status          INTEGER      NOT NULL REFERENCES lkp_record_status(record_status_id),
    x_approval_status VARCHAR(10)  NOT NULL DEFAULT 'none',
    -- ... domain columns ...
    x_custom_fields   JSONB        NOT NULL DEFAULT '{}',
    x_created_at      TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    x_created_by      INTEGER          NULL REFERENCES employee(employee_id),
    x_updated_at      TIMESTAMP    NOT NULL DEFAULT CURRENT_TIMESTAMP,
    x_updated_by      INTEGER          NULL REFERENCES employee(employee_id),
    x_deleted_at      TIMESTAMP        NULL,
    x_deleted_by      INTEGER          NULL REFERENCES employee(employee_id),
    x_record_version  INTEGER      NOT NULL DEFAULT 1,

    CONSTRAINT uq_x_uuid   UNIQUE (x_uuid),
    CONSTRAINT uq_x_number UNIQUE (x_number),
    CONSTRAINT chk_x_approval_status CHECK (x_approval_status IN ('none','pending','approved')),
    CONSTRAINT chk_x_soft_delete CHECK (
        (x_deleted_at IS NULL AND x_deleted_by IS NULL) OR
        (x_deleted_at IS NOT NULL AND x_deleted_by IS NOT NULL)
    )
);
```

`x_custom_fields JSONB NOT NULL DEFAULT '{}'` — remember the nil-guard in
`store_update.go`, or every PATCH omitting it 500s.

Then `x_item`, `x_history`, `x_approver`, `x_approval`, and optionally
`x_conversion`.

### Indexes — partial on live rows

```sql
CREATE INDEX IF NOT EXISTS idx_x_customer ON x (x_customer_id) WHERE x_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_x_status   ON x (x_status)      WHERE x_deleted_at IS NULL;
CREATE INDEX IF NOT EXISTS idx_x_custom_gin ON x USING GIN (x_custom_fields);
```

Plus keyset pairs `(sort_col, pk)` for each sortable column — pagination is
keyset, never offset.

### Seeds — and the trap

```sql
INSERT INTO lkp_record_type (...) VALUES
    ('XXXX', 'x', 'X', TRUE, TRUE, 1)
ON CONFLICT DO NOTHING;
```

⚠️ **The `lkp_record_status` seed keys statuses to record types by hardcoded
integers**, relying on `SERIAL` assignment order from the `lkp_record_type`
insert:

```sql
('DRFT', 'Draft', 4, TRUE, TRUE, 1), ('PAPV', 'Pending Approval', 4, ...),
```

`4` means "whatever `record_type_id` the 4th inserted row happens to have".
Insert a record type out of order, or above an existing one, and **every
downstream status is silently re-pointed at the wrong record type** — no error,
just wrong data in every tenant. Append your record type at the END of the
existing VALUES list and append your statuses with the next integer. Never
reorder.

## Verify the wiring

```bash
go build ./... && go vet ./... && go test ./...
```

Then prove the schema against a real database — `go test ./...` cannot, because
the dbtests are excluded at compile time:

```bash
docker run --rm -d -p 5433:5432 -e POSTGRES_PASSWORD=pg \
  --name ss-dev pgvector/pgvector:pg16
psql "postgres://postgres:pg@localhost:5433/postgres" --single-transaction \
  -v ON_ERROR_STOP=1 -f database/migrations/tenant/schema.sql
# again — re-apply must be a no-op, since it runs on every boot
psql "postgres://postgres:pg@localhost:5433/postgres" --single-transaction \
  -v ON_ERROR_STOP=1 -f database/migrations/tenant/schema.sql
```

CI's `schema-apply` job does exactly this on every PR.
