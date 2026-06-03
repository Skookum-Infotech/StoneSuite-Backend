# Database Migrations

StoneSuite is **multi-tenant (database-per-tenant)**, so there are two migration
sets — no single shared application database:

| Set | Path | Applies to |
|---|---|---|
| **Control plane** | `control_plane/` | the shared `stonesuite_cp` DB (tenant registry, identities, invites, platform admins) |
| **Tenant template** | `tenant/` | each per-tenant DB (`tenant_<slug>`): users, roles, workflows, records |

Tenant migrations are also **embedded into the Go binary** (`database/tenant_migrations.go`)
and applied automatically by the provisioner when a tenant database is created.

Migrations use [golang-migrate](https://github.com/golang-migrate/migrate) via Docker —
no local installation needed.

---

## Prerequisites

- Docker running
- `make` installed
- Postgres container up: `docker compose up -d postgres`
- Control-plane DB created: `docker exec stonesuite-db createdb -U stonesuite stonesuite_cp`

---

## Quick Reference

| Command | What it does |
|---|---|
| `make migrate-cp-create name=<name>` | Create a control-plane migration pair |
| `make migrate-cp-up` | Apply control-plane migrations to `stonesuite_cp` |
| `make migrate-cp-down` | Roll back the last control-plane migration |
| `make migrate-tenant-create name=<name>` | Create a tenant-template migration pair |
| `make migrate-tenant-up db="postgres://…/tenant_acme?sslmode=disable"` | Apply tenant migrations to ONE tenant DB |

> New tenants get the full tenant-template set automatically at provisioning time.
> Use `migrate-tenant-up` to bring an **existing** tenant DB up to date after adding a
> tenant migration. (A fan-out runner across all tenants is a Phase 4+ enhancement.)

---

## Workflow (example: add a field to every tenant)

```bash
make migrate-tenant-create name=add_priority_to_records
```

Write the `up.sql` / `down.sql` in `tenant/`, then either re-provision (new tenants pick
it up automatically) or apply to existing tenants with `migrate-tenant-up`. Always write
the `down.sql` — it's your rollback safety net.

---

## File Naming

Files follow `<version>_<description>.<direction>.sql`, e.g.:

```
control_plane/000002_tenant_metadata.up.sql
tenant/000003_tenant_workflow.up.sql
```

Use descriptive, snake_case names (`add_sso_configs`, `add_priority_to_records`).

---

## Troubleshooting

### Dirty state
If a migration fails halfway, golang-migrate marks the DB **dirty** and blocks further
`up` calls. Fix the partial change manually, force-set the version to the last clean one,
then re-run:

```bash
docker run --rm --network host -v "$PWD/backend/database/migrations/control_plane:/migrations" \
  migrate/migrate -path=/migrations -database "$CP_DB_URL" force <version>
```

### macOS note
The control-plane / tenant targets use `--network host`. On Docker Desktop, `localhost`
host networking can be unreliable; if so, apply a migration directly:

```bash
docker exec -i stonesuite-db psql -U stonesuite -d stonesuite_cp < control_plane/000002_tenant_metadata.up.sql
```

---

## Migration History

**Control plane (`control_plane/`)**

| Version | Description |
|---|---|
| 000001 | Control-plane schema — tenants, identities, invites, SSO configs, platform admins, audit |
| 000002 | Add `tenants.metadata` JSONB (rich company-onboarding details) |

**Tenant template (`tenant/`)**

| Version | Description |
|---|---|
| 000001 | Tenant base — users, profile |
| 000002 | Dynamic RBAC — roles, role_permissions, user_roles, teams |
| 000003 | Workflow engine — workflows, states, transitions, fields, records, history |
