# Database Migrations

StoneSuite uses [golang-migrate](https://github.com/golang-migrate/migrate) via Docker — no local installation needed.

---

## Prerequisites

- Docker running
- `make` installed (`choco install make` on Windows, or use Git Bash)
- Postgres container up: `docker compose up -d postgres`

---

## Quick Reference

| Command | What it does |
|---|---|
| `make migrate-create name=<name>` | Create a new migration pair |
| `make migrate-up` | Apply all pending migrations |
| `make migrate-down` | Roll back the last migration |
| `make migrate-version` | Show the current migration version |
| `make migrate-force v=<n>` | Force-set version (fixes dirty state) |

---

## Workflow

### 1. Create a migration

```bash
make migrate-create name=add_orders_table
```

This generates two files in this directory:

```
000002_add_orders_table.up.sql
000002_add_orders_table.down.sql
```

### 2. Write the SQL

**`up.sql`** — the change you want to apply:
```sql
CREATE TABLE orders (
    id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
    customer_id UUID NOT NULL REFERENCES customers(id) ON DELETE CASCADE,
    status VARCHAR(50) NOT NULL DEFAULT 'pending',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
```

**`down.sql`** — the exact reverse:
```sql
DROP TABLE IF EXISTS orders;
```

> Always write the `down.sql`. It's your rollback safety net.

### 3. Apply

```bash
make migrate-up
```

### 4. Roll back if something goes wrong

```bash
make migrate-down   # undoes the last migration
```

---

## File Naming

Files follow the pattern `<version>_<description>.<direction>.sql`:

```
000001_initial_schema.up.sql
000001_initial_schema.down.sql
000002_add_orders_table.up.sql
000002_add_orders_table.down.sql
```

Use descriptive, snake_case names:

| Scenario | Name |
|---|---|
| New table | `add_orders_table` |
| New column | `add_status_to_leads` |
| Drop column | `remove_fax_from_leads` |
| New index | `add_leads_email_index` |
| Rename table | `rename_users_to_accounts` |

---

## Troubleshooting

### Dirty state

If a migration fails halfway, the DB is marked **dirty** and further `migrate-up` calls are blocked.

1. Manually fix the partial change in Postgres (undo whatever partially ran)
2. Force-set the version back to the last clean state:

```bash
make migrate-force v=1   # replace 1 with the last successful version
```

3. Re-run the migration after fixing the SQL:

```bash
make migrate-up
```

### Check current version

```bash
make migrate-version
```

### Migration already applied

golang-migrate tracks applied versions in a `schema_migrations` table. It will skip already-applied migrations automatically — you can always run `migrate-up` safely.

---

## Migration History

| Version | Description |
|---|---|
| 000001 | Initial schema — users, customers, contacts, onboarding, leads |
