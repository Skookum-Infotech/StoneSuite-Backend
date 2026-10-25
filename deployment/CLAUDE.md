# Deployment (Fly.io) — Claude rules

_Loaded automatically when Claude reads files under `deployment/`. Moved verbatim from the root CLAUDE.md._

## Deployment (Fly.io)

**Stack:** Fly.io (single Go app, `iad` region, **scale-to-zero** — the Machine stops when idle and auto-starts on the next request, ~1-2s cold start; costs nothing at idle) + Neon Postgres (single project, ~30 tenant databases + control-plane DB).

**Deploy pipeline:** `.github/workflows/deploy-backend.yml` runs on every PR merged to `master`: test → `flyctl deploy` → health-check `GET /api` → auto-rollback on failure. `.github/workflows/ci-backend.yml` gates PRs and pushes to `develop`/`master` with build/vet/test + `govulncheck`.

**One-time setup:**
```bash
brew install flyctl
fly auth login
```

**Secrets** (`fly secrets set ...`): `CONTROL_PLANE_DB_URL`, `PROVISION_ADMIN_DB_URL`, `JWT_SECRET`, `SECRET_ENCRYPTION_KEY`, `NOTIFY_URL` (`https://stonesuite-notify.fly.dev` for **every** env — the one shared notify service), `NOTIFY_API_KEY` (the `X-Internal-Secret` shared secret `stonesuite-notify` checks; must match that app's `INTERNAL_SERVICE_SECRET`); optional `R2_ACCOUNT_ID`/`R2_ACCESS_KEY_ID`/`R2_SECRET_ACCESS_KEY`/`CLOUDFLARE_API_TOKEN`, `SENTRY_DSN`, `METRICS_TOKEN`, `AXIOM_TOKEN`/`AXIOM_DATASET`, `AUTOMATION_TICK_SECRET` (≥32 chars; must equal the `automation-cron` Worker's secret — unset disables date-triggered automation, so scheduled vendor payments never dispatch), `AUTOMATION_DEFAULT_TIMEZONE` (default `America/Chicago`, for tenants with no company-profile timezone), `AUTOMATION_KV_NAMESPACE_ID` + `AUTOMATION_KV_API_TOKEN` (with `CLOUDFLARE_ACCOUNT_ID`; all three or none — lets the platform switch mirror into the Worker's KV namespace; the token needs only Workers KV Storage: Edit, not the broad R2 token). (`SMTP_*`/`SENDER_*`/`RESEND_API_KEY` are dead config — every email now goes through `stonesuite-notify`, no direct SMTP/Resend path remains in this repo.) If `NOTIFY_URL`/`NOTIFY_API_KEY` are unset or wrong, `services.SendNotification` fails and document-send returns 502; if they're right but `stonesuite-notify`'s own downstream (Resend) is failing, sends return 200 and the email silently never arrives — check `fly logs -a stonesuite-notify` and the Resend dashboard.

**GitHub Actions secret required:** `FLY_API_TOKEN` (repo secret, `environment: Prod`) — needed for `deploy-backend.yml` to run `flyctl deploy`.

**Manual deploy:**
```bash
fly deploy
fly logs
fly status
```

`fly.toml` is source-controlled — never edit deploy config via the Fly dashboard. `CORS_ORIGIN`/`FRONTEND_URL` must match the deployed frontend's exact origin (Cloudflare Pages URL); a mismatch blocks all browser API calls with CORS errors.

**Never use down-migrations** — recovery is via Neon point-in-time restore or branching.
