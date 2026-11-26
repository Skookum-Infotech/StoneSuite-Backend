import { test, beforeEach, afterEach } from "node:test";
import assert from "node:assert/strict";

import worker, { SNAPSHOT_KEY } from "./worker.js";

// Fake KV with the subset of the Workers KV API the watchdog uses.
function fakeKV() {
  const store = new Map();
  return { store, get: async (k) => (store.has(k) ? store.get(k) : null), put: async (k, v) => { store.set(k, v); } };
}

const HOUR = 60 * 60 * 1000;
const NOW = Date.parse("2026-11-25T13:00:00Z");
let realFetch;
let emails;

function flyMachine() {
  return [{ id: "m1", state: "stopped", region: "iad", events: [{ type: "exit", timestamp: NOW - 60_000, request: { exit_event: { exit_code: 1 }, restart_count: 10 } }] }];
}

beforeEach(() => {
  emails = [];
  realFetch = globalThis.fetch;
  globalThis.fetch = async (url, init = {}) => {
    const u = String(url);
    // Match on the parsed host and path, never a substring of the URL string.
    const { hostname, pathname } = new URL(u);
    const json = (body) => new Response(JSON.stringify(body), { status: 200 });
    if (hostname === "site.example") return new Response("<html>", { status: 200 });
    if (hostname === "console.neon.tech" && pathname === "/api/v2/projects") return json({ projects: [{ id: "dev" }] });
    if (hostname === "console.neon.tech" && pathname === "/api/v2/projects/dev") return json({ project: { id: "dev", name: "dev-db", compute_time_seconds: 110 * 3600, synthetic_storage_size: 1e8, data_transfer_bytes: 1e8 } });
    if (hostname === "api.machines.dev" && pathname === "/v1/apps") return json({ apps: [{ name: "dev-api" }] });
    if (hostname === "api.machines.dev" && pathname === "/v1/apps/dev-api/machines") return json(flyMachine());
    if (hostname === "api.fly.io" && pathname.startsWith("/prometheus/")) return json({ data: { result: [{ metric: { app: "dev-api" }, value: [0, "11"] }] } });
    if (u === "https://api.resend.com/emails") {
      assert.equal(init.headers.Authorization, "Bearer re_test");
      emails.push(JSON.parse(init.body));
      return json({ id: "e1" });
    }
    throw new Error(`unexpected fetch ${u}`);
  };
});

afterEach(() => { globalThis.fetch = realFetch; });

const ENV = {
  FLY_API_TOKEN: "FlyV1 fm2_test", FLY_ORG_SLUG: "skookum", NEON_API_KEY: "neon_test", NEON_ORG_ID: "org-x",
  SITE_URLS: "https://site.example/", RESEND_API_KEY: "re_test", ALERT_EMAIL_TO: "team@example.com",
  ALERT_EMAIL_FROM: "Alerts <alerts@example.com>", REPORT_HOUR_UTC: "14",
};

async function runAt(env, ms) {
  const pending = [];
  await worker.scheduled({ scheduledTime: ms }, env, { waitUntil: (p) => pending.push(p) });
  await Promise.all(pending);
}

test("an hourly run saves a snapshot and emails new problems once", async () => {
  const env = { ...ENV, WATCHDOG_KV: fakeKV() };
  await runAt(env, NOW);
  const snap = JSON.parse(env.WATCHDOG_KV.store.get(SNAPSHOT_KEY));
  const keys = snap.problems.map((p) => p.key).sort();
  assert.deepEqual(keys, ["fly:dev-api:5xx", "fly:dev-api:awake", "fly:dev-api:crashing", "neon:dev:compute:exceeded"]);
  assert.equal(emails.length, 1);
  assert.match(emails[0].subject, /critical/);
  assert.deepEqual(emails[0].to, ["team@example.com"]);

  // Next hour, same problems: no second alert. 14:00 UTC is the report hour.
  await runAt(env, NOW + HOUR);
  assert.equal(emails.length, 2);
  assert.match(emails[1].subject, /^\[StoneSuite\] Daily health report: 4 problems/);
});

test("missing email settings skip sending without failing the run", async () => {
  const env = { ...ENV, ALERT_EMAIL_TO: "", WATCHDOG_KV: fakeKV() };
  await runAt(env, NOW);
  assert.equal(emails.length, 0);
  assert.ok(env.WATCHDOG_KV.store.has(SNAPSHOT_KEY));
  // Not marked as alerted, so it is sent once email is configured.
  assert.ok(![...env.WATCHDOG_KV.store.keys()].some((k) => k.startsWith("watchdog_alerted:")));
});

test("a failing source is reported, not fatal", async () => {
  const env = { ...ENV, NEON_API_KEY: "", WATCHDOG_KV: fakeKV() };
  await runAt(env, NOW);
  const snap = JSON.parse(env.WATCHDOG_KV.store.get(SNAPSHOT_KEY));
  assert.ok(snap.sourceErrors.some((e) => e.source === "Neon"));
  assert.equal(snap.apps.length, 1);
});
