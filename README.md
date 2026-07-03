# StoneSuite Backend

Go backend for **StoneSuite**, a multi-tenant, white-label dynamic CRM platform (database-per-tenant, dynamic workflow engine, dynamic RBAC, central auth with JWT + OAuth SSO).

The frontend (React + TypeScript + Vite) lives in a separate repo: [Skookum-Infotech/StoneSuite](https://github.com/Skookum-Infotech/StoneSuite).

This repo was split out of that monorepo's `backend/` directory, with full git history preserved.

## Quickstart

```bash
cp .env.example .env   # fill in CONTROL_PLANE_DB_URL, PROVISION_ADMIN_DB_URL, JWT_SECRET, etc.
go run .                # starts on :8080
go test ./...
go build ./...
```

See [CLAUDE.md](./CLAUDE.md) for architecture, RBAC/multi-tenancy invariants, and the Fly.io deployment runbook.

## Stack

Go 1.25 + `net/http` + PostgreSQL (pgx), deployed to Fly.io (scale-to-zero) against Neon Postgres.
