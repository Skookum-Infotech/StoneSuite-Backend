# stonesuite-watchdog

A Cloudflare Worker that checks every environment **hourly without waking the
backend or the databases**. It emails the team when something breaks and once
a day with a summary.

It only talks to:
- the **Neon API**, for usage per project (compute CU-hours, storage, data transfer) against the plan limits;
- the **Fly Machines API**, for machine state and crash exits (exit code, OOM, restart count);
- **Fly Prometheus**, for each app over the last 24 h: awake hours (CPU time on core 0), wakes (counter resets) and 5xx responses;
- the **websites**, which are static pages on Cloudflare Pages.

Each run saves a snapshot to Cloudflare KV (key `watchdog_snapshot`). The
backend serves it at `GET /api/platform/system-health`, and Platform Admin >
System Health shows it.

## When it emails

| Email | When |
|---|---|
| Alert | A new problem appears. Each problem is sent at most once every 24 h while it lasts. |
| Daily report | Every day at `REPORT_HOUR_UTC` (14:00 UTC), even when everything is OK. |

Problems it raises:
- website down (critical)
- app crashing or out of memory (critical)
- Neon usage at 80% (warning), 95% or over 100% (critical)
- app awake 6 h or more in 24 h, with wake frequency as the likely cause (warning)
- 10 or more 5xx responses in 24 h (warning)
- an API it could not read (warning)

## One-time setup

1. **KV namespace:** use the same namespace as `automation-cron`. Paste its id into `wrangler.toml` (`[[kv_namespaces]] id`).
   - The backend reads it with its existing `AUTOMATION_KV_NAMESPACE_ID`, `AUTOMATION_KV_API_TOKEN` and `CLOUDFLARE_ACCOUNT_ID` Fly secrets.
   - That token needs **read** access as well as write ("Workers KV Storage: Edit" covers both).
2. **Fly org slug:** run `fly orgs list` and put the slug in `FLY_ORG_SLUG`.
3. **Email settings:**
   - `ALERT_EMAIL_TO`: the team mailbox (comma-separate several).
   - `ALERT_EMAIL_FROM`: a sender on the Resend-verified domain notify uses.
   - `DASHBOARD_URL`: the System Health page on the environment admins use.
4. **Secrets.** Run these in this folder:
   ```sh
   fly tokens create readonly -o <org-slug>   # copy the whole "FlyV1 fm2_..." string
   wrangler secret put FLY_API_TOKEN
   wrangler secret put NEON_API_KEY           # Neon console > Organization settings > API keys
   wrangler secret put RESEND_API_KEY
   ```
5. **Deploy:** `wrangler deploy`.
6. **Check it:** in the Cloudflare dashboard, open Workers > stonesuite-watchdog > Logs. The next hourly run should log `watchdog: N problem(s), M new`, and System Health should show data.

After upgrading a Neon plan, raise `NEON_COMPUTE_LIMIT_CU_HOURS` and the other limits so the percentages stay right.

## Tests

```sh
cd watchdog && npm test   # node --test, no dependencies
```

Local run against the real APIs: `wrangler dev --test-scheduled`, then
`curl "http://localhost:8787/__scheduled?cron=0+*+*+*+*"`.
