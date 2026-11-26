// Pure logic for the watchdog: turns raw Neon / Fly / website results into a
// snapshot and a list of problems. No I/O here, so it is unit-tested with
// `node --test` (see checks.test.js).

export const SNAPSHOT_VERSION = 1;

// Usage levels, as a percentage of a plan limit.
export const WARN_PERCENT = 80;
export const CRITICAL_PERCENT = 95;
export const EXCEEDED_PERCENT = 100;

// Neon Free plan allowances per project (override with wrangler vars).
export const DEFAULT_NEON_COMPUTE_LIMIT_CU_HOURS = 100;
export const DEFAULT_NEON_STORAGE_LIMIT_GB = 0.5;
export const DEFAULT_NEON_TRANSFER_LIMIT_GB = 5;

// A scale-to-zero backend awake longer than this in 24h gets a warning.
export const DEFAULT_AWAKE_WARN_HOURS = 6;
// Waking this many times in 24h means something calls it on a schedule.
export const FREQUENT_WAKES = 20;
// 5xx responses in 24h at or above this get a warning.
export const DEFAULT_ERRORS_WARN = 10;
// Crash exits older than this are history, not a current problem.
export const CRASH_LOOKBACK_MS = 24 * 60 * 60 * 1000;

const SECONDS_PER_HOUR = 3600;
const BYTES_PER_GB = 1e9;
const MINUTES_PER_DAY = 24 * 60;

export function numberOr(value, fallback) {
  const n = Number(value);
  return Number.isFinite(n) && value !== "" && value !== undefined && value !== null ? n : fallback;
}

export function splitList(value) {
  return String(value ?? "")
    .split(",")
    .map((s) => s.trim())
    .filter(Boolean);
}

export function levelFor(percent) {
  if (percent === null || percent === undefined) return "unknown";
  if (percent >= EXCEEDED_PERCENT) return "exceeded";
  if (percent >= CRITICAL_PERCENT) return "critical";
  if (percent >= WARN_PERCENT) return "warning";
  return "ok";
}

const LEVEL_RANK = { unknown: 0, ok: 1, warning: 2, critical: 3, exceeded: 4 };

export function worstLevel(levels) {
  return levels.reduce((worst, l) => (LEVEL_RANK[l] > LEVEL_RANK[worst] ? l : worst), "unknown");
}

function percentOf(used, limit) {
  if (used === null || !limit) return null;
  return Math.round((used / limit) * 1000) / 10;
}

function round(n, digits = 2) {
  if (n === null || n === undefined || !Number.isFinite(n)) return null;
  const f = 10 ** digits;
  return Math.round(n * f) / f;
}

// summarizeNeonProject maps a Neon GET /projects/{id} body to a snapshot row.
export function summarizeNeonProject(project, limits) {
  const computeCuHours = project.compute_time_seconds != null ? project.compute_time_seconds / SECONDS_PER_HOUR : null;
  const storageGb = project.synthetic_storage_size != null ? project.synthetic_storage_size / BYTES_PER_GB : null;
  const transferGb = project.data_transfer_bytes != null ? project.data_transfer_bytes / BYTES_PER_GB : null;
  const compute = { used: round(computeCuHours), limit: limits.computeCuHours, percent: percentOf(computeCuHours, limits.computeCuHours) };
  const storage = { used: round(storageGb, 3), limit: limits.storageGb, percent: percentOf(storageGb, limits.storageGb) };
  const transfer = { used: round(transferGb, 3), limit: limits.transferGb, percent: percentOf(transferGb, limits.transferGb) };
  return {
    id: project.id,
    name: project.name,
    plan: project.owner?.subscription_type ?? null,
    compute,
    storage,
    transfer,
    periodEnd: project.consumption_period_end ?? null,
    lastActiveAt: project.compute_last_active_at ?? null,
    level: worstLevel([levelFor(compute.percent), levelFor(storage.percent), levelFor(transfer.percent)]),
  };
}

// latestExit returns the newest "exit" event of a Fly machine, or null.
export function latestExit(machine) {
  const exits = (machine.events ?? []).filter((e) => e.type === "exit");
  if (exits.length === 0) return null;
  const e = exits.reduce((a, b) => ((b.timestamp ?? 0) > (a.timestamp ?? 0) ? b : a));
  const ev = e.request?.exit_event ?? {};
  return {
    code: ev.exit_code ?? null,
    oomKilled: Boolean(ev.oom_killed),
    requestedStop: Boolean(ev.requested_stop),
    restarts: e.request?.restart_count ?? null,
    at: e.timestamp ? new Date(e.timestamp).toISOString() : null,
  };
}

// isCrashing: the machine's last exit was a crash (non-zero, not a requested
// stop such as Fly's autostop) or an out-of-memory kill, within the lookback.
export function isCrashing(exit, nowMs) {
  if (!exit || !exit.at) return false;
  if (nowMs - Date.parse(exit.at) > CRASH_LOOKBACK_MS) return false;
  if (exit.oomKilled) return true;
  return exit.code !== null && exit.code !== 0 && !exit.requestedStop;
}

export function summarizeMachine(machine, nowMs) {
  const exit = latestExit(machine);
  return {
    id: machine.id,
    state: machine.state ?? "unknown",
    region: machine.region ?? null,
    lastExit: exit,
    crashing: isCrashing(exit, nowMs),
  };
}

// summarizeApp merges an app's machines with its 24h metrics (maps keyed by app).
export function summarizeApp(app, machines, metrics, nowMs) {
  const rows = machines.map((m) => summarizeMachine(m, nowMs));
  return {
    app,
    machines: rows,
    crashing: rows.some((m) => m.crashing),
    awakeHours24h: round(metrics.awake?.[app] ?? null, 1),
    wakes24h: metrics.wakes?.[app] != null ? Math.round(metrics.wakes[app]) : null,
    errors5xx24h: metrics.errors5xx?.[app] != null ? Math.round(metrics.errors5xx[app]) : null,
  };
}

// wakeIntervalMinutes is the average gap between wakes, used to explain why an
// app was awake so long ("about every 15 minutes" points at a scheduler).
export function wakeIntervalMinutes(wakes) {
  if (!wakes || wakes <= 0) return null;
  return Math.round(MINUTES_PER_DAY / wakes);
}

const NEON_LABELS = { compute: "compute", storage: "storage", transfer: "data transfer" };
const NEON_UNITS = { compute: "CU-hours", storage: "GB", transfer: "GB" };

// buildProblems lists everything worth an email. Keys are stable per problem
// (not per value) so the alert is sent once a day, not on every hourly check.
export function buildProblems({ sites, apps, databases, sourceErrors }, opts) {
  const problems = [];
  for (const s of sites) {
    if (!s.ok) {
      problems.push({
        key: `site:${s.url}:down`, severity: "critical", title: `Website down: ${s.url}`,
        detail: s.error ? `Request failed: ${s.error}` : `Returned HTTP ${s.status}.`,
      });
    }
  }
  for (const a of apps) {
    if (a.crashing) {
      const m = a.machines.find((x) => x.crashing);
      const why = m.lastExit.oomKilled ? "ran out of memory" : `exited with code ${m.lastExit.code}`;
      problems.push({
        key: `fly:${a.app}:crashing`, severity: "critical", title: `${a.app} is crashing`,
        detail: `Machine ${m.id} ${why} (restart count ${m.lastExit.restarts ?? "?"}) at ${m.lastExit.at}. Check the app's logs in the Fly dashboard.`,
      });
    }
    if (a.awakeHours24h !== null && a.awakeHours24h >= opts.awakeWarnHours) {
      const every = wakeIntervalMinutes(a.wakes24h);
      const why = a.wakes24h >= FREQUENT_WAKES
        ? ` It woke ${a.wakes24h} times, about every ${every} minutes, which points at something calling it on a schedule (cron, uptime monitor or a polling client).`
        : "";
      problems.push({
        key: `fly:${a.app}:awake`, severity: "warning", title: `${a.app} was awake ${a.awakeHours24h} h in the last 24 h`,
        detail: `It scales to zero when idle, so this is time something kept it running.${why}`,
      });
    }
    if (a.errors5xx24h !== null && a.errors5xx24h >= opts.errorsWarn) {
      problems.push({
        key: `fly:${a.app}:5xx`, severity: "warning", title: `${a.app} returned at least ${a.errors5xx24h} server errors in 24 h`,
        detail: "Counted at Fly's edge (5xx responses). Check the app's logs.",
      });
    }
  }
  for (const d of databases) {
    for (const part of ["compute", "storage", "transfer"]) {
      const lvl = levelFor(d[part].percent);
      if (lvl === "ok" || lvl === "unknown") continue;
      const verb = lvl === "exceeded" ? "is over its" : "has used";
      problems.push({
        key: `neon:${d.id}:${part}:${lvl}`, severity: lvl === "warning" ? "warning" : "critical",
        title: `Neon ${d.name} ${verb} ${NEON_LABELS[part]} limit (${d[part].percent}%)`,
        detail: `${d[part].used} of ${d[part].limit} ${NEON_UNITS[part]} this billing period` +
          (d.periodEnd ? `, resets ${d.periodEnd}.` : ".") +
          (lvl === "exceeded" ? " Neon pauses the project at the limit, so apps using it cannot start." : ""),
      });
    }
  }
  for (const e of sourceErrors) {
    problems.push({ key: `source:${e.source}`, severity: "warning", title: `Watchdog could not read ${e.source}`, detail: e.message });
  }
  return problems;
}

export function buildSnapshot({ nowMs, sites, apps, databases, sourceErrors }, opts) {
  const problems = buildProblems({ sites, apps, databases, sourceErrors }, opts);
  return {
    version: SNAPSHOT_VERSION,
    checkedAt: new Date(nowMs).toISOString(),
    sites, apps, databases, sourceErrors, problems,
  };
}

// newProblems filters out problems already alerted (keys present in seen).
export function newProblems(problems, seen) {
  return problems.filter((p) => !seen.has(p.key));
}
