// Builds the alert and daily-report emails from a snapshot. Pure functions,
// unit-tested in report.test.js.

const HTML_ESCAPES = { "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" };

export function escapeHtml(value) {
  return String(value ?? "").replace(/[&<>"']/g, (c) => HTML_ESCAPES[c]);
}

const SEVERITY_LABEL = { critical: "CRITICAL", warning: "WARNING" };

function dash(v, suffix = "") {
  return v === null || v === undefined ? "-" : `${v}${suffix}`;
}

function problemLinesText(problems) {
  return problems.map((p) => `- [${SEVERITY_LABEL[p.severity] ?? p.severity}] ${p.title}\n  ${p.detail}`).join("\n");
}

function problemListHtml(problems) {
  if (problems.length === 0) return "<p>No problems found.</p>";
  const items = problems
    .map((p) => `<li><strong>[${escapeHtml(SEVERITY_LABEL[p.severity] ?? p.severity)}] ${escapeHtml(p.title)}</strong><br>${escapeHtml(p.detail)}</li>`)
    .join("");
  return `<ul>${items}</ul>`;
}

function table(headers, rows) {
  const head = headers.map((h) => `<th align="left" style="padding:4px 8px;border-bottom:1px solid #ccc">${escapeHtml(h)}</th>`).join("");
  const body = rows
    .map((r) => `<tr>${r.map((c) => `<td style="padding:4px 8px;border-bottom:1px solid #eee">${escapeHtml(c)}</td>`).join("")}</tr>`)
    .join("");
  return `<table cellspacing="0" style="border-collapse:collapse;font-size:13px">${head ? `<tr>${head}</tr>` : ""}${body}</table>`;
}

function appRows(snapshot) {
  return snapshot.apps.map((a) => [
    a.app,
    a.crashing ? "CRASHING" : [...new Set(a.machines.map((m) => m.state))].join(", ") || "no machines",
    dash(a.awakeHours24h, " h"),
    dash(a.wakes24h),
    dash(a.errors5xx24h),
  ]);
}

function databaseRows(snapshot) {
  const pct = (part) => (part.percent === null ? "-" : `${part.used} / ${part.limit} (${part.percent}%)`);
  return snapshot.databases.map((d) => [d.name, d.level, pct(d.compute), pct(d.storage), pct(d.transfer)]);
}

function siteRows(snapshot) {
  return snapshot.sites.map((s) => [s.url, s.ok ? "up" : "DOWN", s.ok ? `${s.status} in ${s.ms} ms` : s.error ?? `HTTP ${s.status}`]);
}

function footerHtml(dashboardUrl) {
  return dashboardUrl
    ? `<p style="color:#666;font-size:12px">Full details: <a href="${escapeHtml(dashboardUrl)}">Platform Admin &gt; System Health</a>. Checked hourly by the stonesuite-watchdog Cloudflare Worker, which reads Neon's and Fly's APIs and never wakes the backend or the databases.</p>`
    : "";
}

// alertEmail is sent when new problems appear (each at most once a day).
export function alertEmail(problems, snapshot, dashboardUrl) {
  const critical = problems.filter((p) => p.severity === "critical").length;
  const subject = critical > 0
    ? `[StoneSuite] ${critical} critical problem${critical === 1 ? "" : "s"}: ${problems[0].title}`
    : `[StoneSuite] Warning: ${problems[0].title}`;
  const text = `New problems found at ${snapshot.checkedAt}:\n\n${problemLinesText(problems)}\n` +
    (dashboardUrl ? `\nDetails: ${dashboardUrl}\n` : "");
  const html = `<p>New problems found at ${escapeHtml(snapshot.checkedAt)}:</p>${problemListHtml(problems)}${footerHtml(dashboardUrl)}`;
  return { subject, text, html };
}

// dailyReportEmail is the once-a-day summary, sent even when all is well.
export function dailyReportEmail(snapshot, dashboardUrl) {
  const n = snapshot.problems.length;
  const subject = n === 0 ? "[StoneSuite] Daily health report: all OK" : `[StoneSuite] Daily health report: ${n} problem${n === 1 ? "" : "s"}`;
  const appHeaders = ["App", "State", "Awake (24 h)", "Wakes (24 h)", "5xx (24 h)"];
  const dbHeaders = ["Neon project", "Level", "Compute (CU-h)", "Storage (GB)", "Transfer (GB)"];
  const siteHeaders = ["Website", "Status", "Detail"];
  const text = [
    `Health report for ${snapshot.checkedAt}`,
    "",
    n === 0 ? "No problems found." : problemLinesText(snapshot.problems),
    "",
    "Backends (last 24 h):",
    ...appRows(snapshot).map((r) => `- ${r.join(" | ")}`),
    "",
    "Databases (this billing period):",
    ...databaseRows(snapshot).map((r) => `- ${r.join(" | ")}`),
    "",
    "Websites:",
    ...siteRows(snapshot).map((r) => `- ${r.join(" | ")}`),
    dashboardUrl ? `\nDetails: ${dashboardUrl}` : "",
  ].join("\n");
  const html = [
    `<p>Health report for ${escapeHtml(snapshot.checkedAt)}</p>`,
    "<h3>Problems</h3>", problemListHtml(snapshot.problems),
    "<h3>Backends (last 24 h)</h3>",
    "<p style=\"color:#666;font-size:12px\">Every backend scales to zero, so awake time with nobody using the app is cost. Many wakes at a steady interval point at a scheduler or monitor.</p>",
    table(appHeaders, appRows(snapshot)),
    "<h3>Databases (this billing period)</h3>", table(dbHeaders, databaseRows(snapshot)),
    "<h3>Websites</h3>", table(siteHeaders, siteRows(snapshot)),
    footerHtml(dashboardUrl),
  ].join("");
  return { subject, text, html };
}
