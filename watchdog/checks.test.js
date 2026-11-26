import { test } from "node:test";
import assert from "node:assert/strict";

import {
  buildProblems, buildSnapshot, isCrashing, latestExit, levelFor, newProblems, numberOr,
  splitList, summarizeApp, summarizeNeonProject, wakeIntervalMinutes, worstLevel,
} from "./checks.js";

const NOW = Date.parse("2026-11-25T08:00:00Z");
const LIMITS = { computeCuHours: 100, storageGb: 0.5, transferGb: 5 };
const OPTS = { awakeWarnHours: 6, errorsWarn: 10 };

test("levelFor maps percentages to levels", () => {
  for (const [pct, want] of [[null, "unknown"], [0, "ok"], [79.9, "ok"], [80, "warning"], [95, "critical"], [100, "exceeded"], [110.4, "exceeded"]]) {
    assert.equal(levelFor(pct), want, `percent ${pct}`);
  }
});

test("worstLevel picks the most severe", () => {
  assert.equal(worstLevel(["ok", "exceeded", "warning"]), "exceeded");
  assert.equal(worstLevel(["unknown", "ok"]), "ok");
});

test("numberOr and splitList", () => {
  assert.equal(numberOr("14", 3), 14);
  assert.equal(numberOr("", 3), 3);
  assert.equal(numberOr(undefined, 3), 3);
  assert.equal(numberOr("abc", 3), 3);
  assert.deepEqual(splitList(" a, ,b ,"), ["a", "b"]);
  assert.deepEqual(splitList(undefined), []);
});

test("summarizeNeonProject converts units and flags an exceeded project", () => {
  const d = summarizeNeonProject({
    id: "holy-math", name: "dev-stonesuite-db", owner: { subscription_type: "free_v3" },
    compute_time_seconds: 110.36 * 3600, synthetic_storage_size: 232.2e6, data_transfer_bytes: 668.79e6,
    consumption_period_end: "2026-12-01T00:00:00Z",
  }, LIMITS);
  assert.equal(d.compute.used, 110.36);
  assert.equal(d.compute.percent, 110.4);
  assert.equal(d.storage.used, 0.232);
  assert.equal(d.storage.percent, 46.4);
  assert.equal(d.level, "exceeded");
  assert.equal(d.plan, "free_v3");
});

test("summarizeNeonProject tolerates missing usage fields", () => {
  const d = summarizeNeonProject({ id: "p", name: "p" }, LIMITS);
  assert.equal(d.compute.percent, null);
  assert.equal(d.level, "unknown");
});

const exitEvent = (ts, code, extra = {}) => ({
  type: "exit", timestamp: ts,
  request: { exit_event: { exit_code: code, oom_killed: false, requested_stop: false, ...extra }, restart_count: 10 },
});

test("latestExit picks the newest exit event", () => {
  const m = { events: [exitEvent(NOW - 5000, 0), { type: "start", timestamp: NOW }, exitEvent(NOW - 1000, 1)] };
  const e = latestExit(m);
  assert.equal(e.code, 1);
  assert.equal(e.restarts, 10);
  assert.equal(latestExit({ events: [] }), null);
  assert.equal(latestExit({}), null);
});

test("isCrashing: crash vs autostop vs OOM vs stale", () => {
  const at = (ms) => new Date(ms).toISOString();
  assert.equal(isCrashing({ code: 1, requestedStop: false, oomKilled: false, at: at(NOW - 60_000) }, NOW), true);
  assert.equal(isCrashing({ code: 0, requestedStop: true, oomKilled: false, at: at(NOW - 60_000) }, NOW), false);
  assert.equal(isCrashing({ code: 143, requestedStop: true, oomKilled: false, at: at(NOW - 60_000) }, NOW), false);
  assert.equal(isCrashing({ code: 137, requestedStop: true, oomKilled: true, at: at(NOW - 60_000) }, NOW), true);
  assert.equal(isCrashing({ code: 1, requestedStop: false, oomKilled: false, at: at(NOW - 2 * 86_400_000) }, NOW), false);
  assert.equal(isCrashing(null, NOW), false);
});

test("summarizeApp merges machines and metrics", () => {
  const a = summarizeApp("dev-stonesuite-api",
    [{ id: "m1", state: "stopped", region: "iad", events: [exitEvent(NOW - 1000, 1)] }],
    { awake: { "dev-stonesuite-api": 10.5636 }, wakes: { "dev-stonesuite-api": 95 } }, NOW);
  assert.equal(a.crashing, true);
  assert.equal(a.awakeHours24h, 10.6);
  assert.equal(a.wakes24h, 95);
  assert.equal(a.errors5xx24h, null);
});

test("wakeIntervalMinutes", () => {
  assert.equal(wakeIntervalMinutes(96), 15);
  assert.equal(wakeIntervalMinutes(0), null);
  assert.equal(wakeIntervalMinutes(null), null);
});

test("buildProblems covers every kind of problem with stable keys", () => {
  const apps = [
    summarizeApp("dev-stonesuite-api", [{ id: "m1", state: "stopped", events: [exitEvent(NOW - 1000, 1)] }],
      { awake: { "dev-stonesuite-api": 10.6 }, wakes: { "dev-stonesuite-api": 95 }, errors5xx: { "dev-stonesuite-api": 12 } }, NOW),
    summarizeApp("prod-stonesuite-api", [{ id: "m2", state: "stopped", events: [exitEvent(NOW - 1000, 0, { requested_stop: true })] }],
      { awake: { "prod-stonesuite-api": 0.3 }, wakes: { "prod-stonesuite-api": 4 } }, NOW),
  ];
  const databases = [summarizeNeonProject({ id: "dev", name: "dev-db", compute_time_seconds: 96 * 3600, synthetic_storage_size: 0.41e9, data_transfer_bytes: 0 }, LIMITS)];
  const sites = [{ url: "https://a", ok: true, status: 200 }, { url: "https://b", ok: false, status: null, error: "timeout" }];
  const problems = buildProblems({ sites, apps, databases, sourceErrors: [{ source: "Neon", message: "401" }] }, OPTS);
  const keys = problems.map((p) => p.key).sort();
  assert.deepEqual(keys, [
    "fly:dev-stonesuite-api:5xx", "fly:dev-stonesuite-api:awake", "fly:dev-stonesuite-api:crashing",
    "neon:dev:compute:critical", "neon:dev:storage:warning", "site:https://b:down", "source:Neon",
  ]);
  const awake = problems.find((p) => p.key === "fly:dev-stonesuite-api:awake");
  assert.match(awake.detail, /about every 15 minutes/);
  assert.equal(problems.find((p) => p.key === "site:https://b:down").severity, "critical");
});

test("buildSnapshot and newProblems", () => {
  const s = buildSnapshot({ nowMs: NOW, sites: [{ url: "https://b", ok: false, status: 503 }], apps: [], databases: [], sourceErrors: [] }, OPTS);
  assert.equal(s.version, 1);
  assert.equal(s.checkedAt, "2026-11-25T08:00:00.000Z");
  assert.equal(s.problems.length, 1);
  assert.equal(newProblems(s.problems, new Set(["site:https://b:down"])).length, 0);
  assert.equal(newProblems(s.problems, new Set()).length, 1);
});
