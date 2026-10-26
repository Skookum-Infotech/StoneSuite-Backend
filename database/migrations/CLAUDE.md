# Database & Migrations — Claude rules

_Loaded automatically when Claude reads files under `database/migrations/`. Moved verbatim from the root CLAUDE.md._

## Database & Migrations
1. **Control-plane migrations are idempotent.** Use `CREATE TABLE IF NOT EXISTS`, `ON CONFLICT DO NOTHING`.
2. **Append columns with `ALTER TABLE x ADD COLUMN IF NOT EXISTS`** — that is the mechanism `tenant/schema.sql` is built on (see its header) and what `/add-migration` prescribes. **Never edit an existing `CREATE TABLE` body to add a column:** `CREATE TABLE IF NOT EXISTS` is a no-op on every existing tenant, so the column would reach fresh databases only and diverge permanently. Still forbidden: `DROP TABLE`, `DROP COLUMN`, `TRUNCATE`, data-losing `ALTER COLUMN ... TYPE`, renames, and down-migrations.
3. **`ADD CONSTRAINT` is not idempotent** — a bare one errors on the second boot and breaks every tenant. Guard new CHECKs with a `DO $$` / `pg_constraint` existence check.
4. **Changing an existing index means correcting it in place, not dropping and recreating it at EOF.** Recreating at the end leaves the old definition upstream, so the next boot rebuilds the old index before the drop runs — harmless on empty tables, and a boot-blocking failure once real data violates the old key. Correct the canonical statement; use an appended `DROP INDEX IF EXISTS` only to retire the old *name*.
5. **Tenant `schema_version` table tracks all applied versions.**
6. **Never run raw SQL in handlers.** Always use prepared statements (pgx named params).
7. **All queries accept `context.Context` as first parameter.**
