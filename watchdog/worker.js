// stonesuite-watchdog: hourly health check that NEVER wakes the backend or the
// databases. It only reads Neon's API (usage per project), Fly's Machines API
// (machine state + crash exits), Fly's Prometheus (awake time, wakes, 5xx)
// and fetches the static websites served by Cloudflare Pages.
//
// Each run saves a snapshot to KV (WATCHDOG_KV, key SNAPSHOT_KEY), which the
// backend serves to Platform Admin > System Health (GET /api/platform/system-health).
// New problems are emailed via Resend at most once a day each; a daily report
// goes out at REPORT_HOUR_UTC.

import {
  buildSnapshot, newProblems, numberOr, splitList, summarizeApp, summarizeNeonProject,
  DEFAULT_AWAKE_WARN_HOURS, DEFAULT_ERRORS_WARN,
  DEFAULT_NEON_COMPUTE_LIMIT_CU_HOURS, DEFAULT_NEON_STORAGE_LIMIT_GB, DEFAULT_NEON_TRANSFER_LIMIT_GB,
} from "./checks.js";
import { alertEmail, dailyReportEmail } from "./report.js";

// Must match automation.WatchdogSnapshotKey in the backend (automation/kv.go).
export const SNAPSHOT_KEY = "watchdog_snapshot";
const ALERTED_PREFIX = "watchdog_alerted:";
const ALERT_TTL_SECONDS = 24 * 60 * 60;
const FETCH_TIMEOUT_MS = 15_000;
const DEFAULT_REPORT_HOUR_UTC = 14;

const NEON_API = "https://console.neon.tech/api/v2";
const FLY_MACHINES_API = "https://api.machines.dev/v1";
const FLY_PROMETHEUS_API = "https://api.fly.io/prometheus";
const RESEND_API = "https://api.resend.com/emails";

const PROM_QUERIES = {
  // Wall-clock machine-hours: CPU 0's total time across all modes is the time the machine ran.
  awake: 'sum by (app) (increase(fly_instance_cpu{cpu_id="0"}[24h])) / 100 / 3600',
  // Each start resets the counter, so resets = wakes.
  wakes: 'sum by (app) (resets(fly_instance_cpu{cpu_id="0",mode="idle"}[24h]))',
  errors5xx: 'sum by (app) (increase(fly_edge_http_responses_count{status=~"5.."}[24h]))',
};

function flyAuth(token) {
  // `fly tokens create readonly` prints "FlyV1 fm2_..."; the prefix is the scheme.
  return token.startsWith("FlyV1 ") ? token : `Bearer ${token}`;
}

async function getJson(url, headers) {
  const res = await fetch(url, { headers, signal: AbortSignal.timeout(FETCH_TIMEOUT_MS) });
  if (!res.ok) {
    const body = (await res.text()).slice(0, 200);
    throw new Error(`${new URL(url).host} returned HTTP ${res.status}: ${body}`);
  }
  return res.json();
}

async function checkSite(url) {
  const started = Date.now();
  try {
    const res = await fetch(url, { redirect: "follow", signal: AbortSignal.timeout(FETCH_TIMEOUT_MS) });
    return { url, ok: res.status < 400, status: res.status, ms: Date.now() - started };
  } catch (err) {
    return { url, ok: false, status: null, ms: Date.now() - started, error: String(err?.message ?? err) };
  }
}

async function readNeon(env, limits) {
  const headers = { Authorization: `Bearer ${env.NEON_API_KEY}`, Accept: "application/json" };
  const qs = env.NEON_ORG_ID ? `?org_id=${encodeURIComponent(env.NEON_ORG_ID)}` : "";
  const list = await getJson(`${NEON_API}/projects${qs}`, headers);
  // The list omits some usage fields, so read each project.
  const projects = await Promise.all(
    (list.projects ?? []).map((p) => getJson(`${NEON_API}/projects/${encodeURIComponent(p.id)}`, headers).then((r) => r.project)),
  );
  return projects.map((p) => summarizeNeonProject(p, limits));
}

async function readFlyApps(env) {
  const headers = { Authorization: flyAuth(env.FLY_API_TOKEN) };
  const named = splitList(env.FLY_APPS);
  if (named.length > 0) return named;
  const res = await getJson(`${FLY_MACHINES_API}/apps?org_slug=${encodeURIComponent(env.FLY_ORG_SLUG)}`, headers);
  return (res.apps ?? []).map((a) => a.name).sort();
}

async function readMachines(env, app) {
  const headers = { Authorization: flyAuth(env.FLY_API_TOKEN) };
  const machines = await getJson(`${FLY_MACHINES_API}/apps/${encodeURIComponent(app)}/machines`, headers);
  // The list may leave out events; fetch the machine itself when it does.
  return Promise.all(
    machines.map((m) => (m.events ? m : getJson(`${FLY_MACHINES_API}/apps/${encodeURIComponent(app)}/machines/${encodeURIComponent(m.id)}`, headers))),
  );
}

async function readFlyMetrics(env) {
  const headers = { Authorization: flyAuth(env.FLY_API_TOKEN) };
  const base = `${FLY_PROMETHEUS_API}/${encodeURIComponent(env.FLY_ORG_SLUG)}/api/v1/query?query=`;
  const out = {};
  for (const [name, q] of Object.entries(PROM_QUERIES)) {
    const res = await getJson(base + encodeURIComponent(q), headers);
    out[name] = Object.fromEntries((res.data?.result ?? []).map((r) => [r.metric.app, Number(r.value[1])]));
  }
  return out;
}

async function sendEmail(env, { subject, text, html }) {
  const to = splitList(env.ALERT_EMAIL_TO);
  if (!env.RESEND_API_KEY || to.length === 0 || !env.ALERT_EMAIL_FROM) {
    console.warn("email not configured (RESEND_API_KEY, ALERT_EMAIL_TO, ALERT_EMAIL_FROM); skipped:", subject);
    return false;
  }
  const res = await fetch(RESEND_API, {
    method: "POST",
    headers: { Authorization: `Bearer ${env.RESEND_API_KEY}`, "Content-Type": "application/json" },
    body: JSON.stringify({ from: env.ALERT_EMAIL_FROM, to, subject, text, html }),
    signal: AbortSignal.timeout(FETCH_TIMEOUT_MS),
  });
  if (!res.ok) throw new Error(`Resend returned HTTP ${res.status}: ${(await res.text()).slice(0, 200)}`);
  return true;
}

// collect runs every check; a failing source is recorded, not fatal, so one
// broken API never hides the others.
export async function collect(env, nowMs) {
  const sourceErrors = [];
  const attempt = async (source, fn, fallback) => {
    try {
      return await fn();
    } catch (err) {
      sourceErrors.push({ source, message: String(err?.message ?? err) });
      return fallback;
    }
  };
  const limits = {
    computeCuHours: numberOr(env.NEON_COMPUTE_LIMIT_CU_HOURS, DEFAULT_NEON_COMPUTE_LIMIT_CU_HOURS),
    storageGb: numberOr(env.NEON_STORAGE_LIMIT_GB, DEFAULT_NEON_STORAGE_LIMIT_GB),
    transferGb: numberOr(env.NEON_TRANSFER_LIMIT_GB, DEFAULT_NEON_TRANSFER_LIMIT_GB),
  };
  const sites = await Promise.all(splitList(env.SITE_URLS).map(checkSite));
  const databases = env.NEON_API_KEY ? await attempt("Neon", () => readNeon(env, limits), []) : [];
  let apps = [];
  if (env.FLY_API_TOKEN && env.FLY_ORG_SLUG) {
    const names = await attempt("Fly apps", () => readFlyApps(env), []);
    const metrics = await attempt("Fly metrics", () => readFlyMetrics(env), {});
    apps = await Promise.all(
      names.map(async (app) => summarizeApp(app, await attempt(`Fly machines (${app})`, () => readMachines(env, app), []), metrics, nowMs)),
    );
  }
  if (!env.NEON_API_KEY) sourceErrors.push({ source: "Neon", message: "NEON_API_KEY is not set." });
  if (!env.FLY_API_TOKEN || !env.FLY_ORG_SLUG) sourceErrors.push({ source: "Fly", message: "FLY_API_TOKEN or FLY_ORG_SLUG is not set." });
  return buildSnapshot({ nowMs, sites, apps, databases, sourceErrors }, {
    awakeWarnHours: numberOr(env.AWAKE_WARN_HOURS, DEFAULT_AWAKE_WARN_HOURS),
    errorsWarn: numberOr(env.ERRORS_WARN, DEFAULT_ERRORS_WARN),
  });
}

async function run(env, scheduledTimeMs) {
  if (!env.WATCHDOG_KV) throw new Error("WATCHDOG_KV binding is not configured");
  const snapshot = await collect(env, scheduledTimeMs);
  await env.WATCHDOG_KV.put(SNAPSHOT_KEY, JSON.stringify(snapshot));

  const seen = new Set();
  for (const p of snapshot.problems) {
    if ((await env.WATCHDOG_KV.get(ALERTED_PREFIX + p.key)) !== null) seen.add(p.key);
  }
  const fresh = newProblems(snapshot.problems, seen);
  if (fresh.length > 0 && (await sendEmail(env, alertEmail(fresh, snapshot, env.DASHBOARD_URL)))) {
    await Promise.all(fresh.map((p) => env.WATCHDOG_KV.put(ALERTED_PREFIX + p.key, snapshot.checkedAt, { expirationTtl: ALERT_TTL_SECONDS })));
  }
  if (new Date(scheduledTimeMs).getUTCHours() === numberOr(env.REPORT_HOUR_UTC, DEFAULT_REPORT_HOUR_UTC)) {
    await sendEmail(env, dailyReportEmail(snapshot, env.DASHBOARD_URL));
  }
  console.log(`watchdog: ${snapshot.problems.length} problem(s), ${fresh.length} new`);
}

export default {
  async scheduled(controller, env, ctx) {
    ctx.waitUntil(run(env, controller.scheduledTime));
  },
};
