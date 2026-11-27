import { test } from "node:test";
import assert from "node:assert/strict";

import { buildSnapshot } from "./checks.js";
import { alertEmail, dailyReportEmail, escapeHtml } from "./report.js";

const NOW = Date.parse("2026-11-25T14:00:00Z");
const OPTS = { awakeWarnHours: 6, errorsWarn: 10 };

function snapshot(sites) {
  return buildSnapshot({
    nowMs: NOW, sites,
    apps: [{ app: "dev-stonesuite-api", machines: [{ id: "m1", state: "stopped", crashing: false, lastExit: null }], crashing: false, awakeHours24h: 1.2, wakes24h: 3, errors5xx24h: 0 }],
    databases: [{ id: "d", name: "dev-db", level: "ok", compute: { used: 10, limit: 100, percent: 10 }, storage: { used: 0.1, limit: 0.5, percent: 20 }, transfer: { used: null, limit: 5, percent: null } }],
    sourceErrors: [],
  }, OPTS);
}

test("escapeHtml escapes markup", () => {
  assert.equal(escapeHtml(`<a href="x">'&'</a>`), "&lt;a href=&quot;x&quot;&gt;&#39;&amp;&#39;&lt;/a&gt;");
  assert.equal(escapeHtml(null), "");
});

test("alertEmail leads with the critical count and escapes titles", () => {
  const s = snapshot([{ url: "https://x/<script>", ok: false, status: 503 }]);
  const mail = alertEmail(s.problems, s, "https://admin/health");
  assert.match(mail.subject, /^\[StoneSuite\] 1 critical problem: Website down/);
  assert.ok(!mail.html.includes("<script>"));
  assert.match(mail.text, /Details: https:\/\/admin\/health/);
});

test("dailyReportEmail says all OK when there are no problems", () => {
  const s = snapshot([{ url: "https://x", ok: true, status: 200, ms: 120 }]);
  const mail = dailyReportEmail(s, "");
  assert.equal(mail.subject, "[StoneSuite] Daily health report: all OK");
  assert.match(mail.text, /dev-stonesuite-api \| stopped \| 1.2 h \| 3 \| 0/);
  assert.match(mail.text, /dev-db \| ok \| 10 \/ 100 \(10%\) \| 0.1 \/ 0.5 \(20%\) \| -/);
  assert.match(mail.html, /<h3>Websites<\/h3>/);
});
