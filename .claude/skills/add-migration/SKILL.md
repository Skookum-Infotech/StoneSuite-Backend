---
name: add-migration
description: Add or change a database table/column/seed in StoneSuite Backend's canonical schema.sql files safely. Use when modifying the control-plane or tenant database schema.
---

# Changing the database schema

StoneSuite Backend uses a **single canonical `schema.sql` per scope**, NOT numbered
migrations. Each file is embedded into the Go binary and **re-applied in one
transaction (under an advisory lock) on every boot/provision**, on databases in any
prior state. So **editing the SQL file IS the migration**, and every statement must be
idempotent and safe to repeat.

- Control-plane DB (`stonesuite_cp`): `database/migrations/control_plane/schema.sql`
- Per-tenant DB template: `database/migrations/tenant/schema.sql`
- Applied by `ApplyControlPlaneMigrations` / `ApplyTenantSchema` (database/*.go).
  (Ignore the stale `migrations/README.md` and any mention of numbered `*.up.sql` /
  `*.down.sql` / `golang-migrate` — that model is gone.)

## Checklist (make a TodoWrite item per step)

1. **Pick the right scope.** Tenant-owned data (workflows, records, roles, users,
   teams, audit) → `tenant/schema.sql`. Platform/registry data (tenants, identities,
   invites, sso_configs, platform_admins) → `control_plane/schema.sql`. Never mix.

2. **Write idempotent DDL only:**
   - `CREATE TABLE IF NOT EXISTS ...`
   - `ALTER TABLE x ADD COLUMN IF NOT EXISTS col ... ;` (append columns this way)
   - `CREATE INDEX IF NOT EXISTS ...`
   - Constraints: guard so a re-run doesn't error (e.g. add via a `DO $$ ... $$`
     block that checks existence, or a uniquely-named `ADD CONSTRAINT` you know is new).
   A bare `CREATE TABLE` / `ADD COLUMN` that errors on the second run **breaks every
   existing tenant's boot** — the guard hook will block it.

3. **New NOT NULL column on an existing table MUST have a DEFAULT** (existing rows
   would otherwise violate it):
   ```sql
   ALTER TABLE leads ADD COLUMN IF NOT EXISTS priority TEXT NOT NULL DEFAULT 'normal';
   ```

4. **Idempotent seed writes:** `INSERT ... ON CONFLICT DO NOTHING` (or `DO UPDATE`).
   Never a plain INSERT that duplicates seed rows on re-apply.

5. **Nothing that can't run inside a transaction.** Forbidden: `CREATE INDEX
   CONCURRENTLY`, `VACUUM`, `ALTER SYSTEM`. The whole file runs in one tx — these abort it.

6. **Non-destructive / append-only.** Never `DROP TABLE`, `DROP COLUMN`, `TRUNCATE`,
   data-losing `ALTER COLUMN ... TYPE`, or column renames, and never add a `*.down.sql`.
   Recovery is via Neon point-in-time restore, not down SQL. If you truly must remove
   something, discuss first — it risks silent tenant data loss.

7. **Custom fields are data, not schema.** Workflow custom fields live in the
   `custom_fields` JSONB column + `workflow_field_definitions` rows (max 15 per
   workflow) — do NOT add real columns for them.

8. **Verify.** `go build ./... && go test ./...`. If you have a local
   DB, apply twice to prove idempotency:
   `docker compose up -d postgres` then boot the backend once, restart it, confirm no
   error on the second apply.

## Optional: run the auditor
After editing, run the `migration-auditor` agent on the change for an idempotency /
safety pass.
