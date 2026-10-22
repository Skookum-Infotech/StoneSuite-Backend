# StoneSuite Backend

Go backend for **StoneSuite**, a multi-tenant, white-label dynamic CRM platform (database-per-tenant, dynamic workflow engine, dynamic RBAC, central auth with JWT + OAuth SSO).

The frontend (React + TypeScript + Vite) lives in a separate repo: [Skookum-Infotech/StoneSuite](https://github.com/Skookum-Infotech/StoneSuite).

This repo was split out of that monorepo's `backend/` directory, with full git history preserved.

## Quickstart

```bash
cp .env.example .env   # fill in CONTROL_PLANE_DB_URL, PROVISION_ADMIN_DB_URL, JWT_SECRET, etc.
go run .                # starts on :8080
go test ./...              # add -tags dbtest with TEST_DATABASE_URL for DB-backed tests
go build ./...
```

See [CLAUDE.md](./CLAUDE.md) (mirrored for Codex in [AGENTS.md](./AGENTS.md)) for architecture, RBAC/multi-tenancy invariants, and the Fly.io deployment runbook.

See [docs/SAML_SETUP.md](./docs/SAML_SETUP.md) for SAML 2.0 SSO setup (AWS Cognito, Microsoft Entra ID).

## Documentation

- [docs/architecture-overview.md](./docs/architecture-overview.md) — system architecture
- [docs/api-reference.md](./docs/api-reference.md), [docs/openapi.json](./docs/openapi.json), and the Postman collection in `docs/` (regenerate with `go run ./cmd/gen-apidocs`)
- [docs/customer-portal-api.md](./docs/customer-portal-api.md) — customer portal API
- End-user help docs (`docs/*.md`) are embedded into the binary and ingested for the AI assistant.

## Stack

Go 1.26 + `net/http` + PostgreSQL (pgx), deployed to Fly.io (scale-to-zero) against Neon Postgres. Email goes through the separate `stonesuite-notify` service; date-triggered automation is ticked every 15 minutes by the `automation-cron` Cloudflare Worker; AI/RAG uses a self-hosted Ollama app.

## Architectural Decisions

### Estimates vs. Quotes (Dual Tables vs Single Table)
In the StoneSuite CRM architecture, **Estimates** and **Quotes** are structurally identical (both support line items, configurable approval logic, taxes, and customer linking). While many CRMs utilize a **Single Table Approach** (where a single table manages both to maximize code reusability, seamless conversions, and simplified financial reporting), StoneSuite leverages a **Dual Table Approach** (`estimate` and `quote` are completely isolated tables and modules).

The Dual Table Approach was explicitly chosen to support:
1. **Isolated RBAC & Security:** Estimates and Quotes have strictly independent permission scopes (`estimate:read` vs `quote:read`). Users can be granted full access to draft Estimates but be explicitly denied access to binding Quotes.
2. **Dedicated API Filtering:** Isolated API endpoints (`/api/tenant/estimates` and `/api/tenant/quotes`) allow for independent keyset pagination and filtering rules, keeping the CRM's query engine clean and performant.
3. **Complex Lineage Tracking:** By separating the tables, a Quote natively supports an `estimate_id` lineage field, tracking exactly which early-stage Estimate evolved into a finalized Quote without polluting a single table's lifecycle.
