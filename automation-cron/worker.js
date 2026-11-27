// automation-cron — Cloudflare Worker whose only job is to be the clock for
// stonesuite-backend. The backend scales to zero on Fly, so it cannot keep its
// own ticker. Two crons (wrangler.toml):
//   - once a day at 14:00 UTC: a signed POST /api/internal/automation/tick,
//     which wakes the machine and runs date-triggered work (e.g. dispatching
//     scheduled vendor payments). Gated by the platform switch (see below).
//   - daily at 02:41 UTC (USAGE_ROLLUP_CRON): a signed
//     POST /api/internal/usage/rollup, which rolls up the previous UTC day's
//     per-tenant usage metrics. NOT gated by the platform switch: metering must
//     run while automation is off, and it wakes the machine once a day.
//
// Signature: hex(HMAC-SHA256(AUTOMATION_TICK_SECRET,
// `${method} ${path}\n${ts}\n${body}`)), sent as X-Automation-Timestamp /
// X-Automation-Signature. Must match middleware/internal_sig.go (SignInternal)
// in the backend. Each tick gets a fresh timestamp, hence a fresh signature —
// the backend rejects a signature it has already accepted.
//
// Platform on/off (tick cron only): before calling, the Worker reads KV key
// AUTOMATION_KV "automation_enabled" ("1" = on). Anything else (off, missing, unreadable)
// means do not call, so a disabled platform never wakes the scale-to-zero
// backend or Neon. The platform admin endpoint PUT /api/platform/automation
// writes that key (backend automation/kv.go); the backend re-checks its own
// database flag on every tick as a backstop.

const encoder = new TextEncoder();

// Must match automation.KVEnabledKey / KVOn in the backend (automation/kv.go).
const KV_ENABLED_KEY = "automation_enabled";
const KV_ON = "1";

// Must match the daily cron in wrangler.toml [triggers].
const USAGE_ROLLUP_CRON = "41 2 * * *";

async function platformEnabled(env) {
  if (!env.AUTOMATION_KV) {
    // Misconfiguration, not "off": fail loudly so the cron shows as failed.
    throw new Error("AUTOMATION_KV binding is not configured");
  }
  return (await env.AUTOMATION_KV.get(KV_ENABLED_KEY)) === KV_ON;
}

async function run(env) {
  if (!(await platformEnabled(env))) {
    console.log("automation disabled at platform level; backend not called");
    return;
  }
  await tick(env);
}

async function sign(secret, method, path, ts, body) {
  const key = await crypto.subtle.importKey(
    "raw",
    encoder.encode(secret),
    { name: "HMAC", hash: "SHA-256" },
    false,
    ["sign"],
  );
  const mac = await crypto.subtle.sign("HMAC", key, encoder.encode(`${method} ${path}\n${ts}\n${body}`));
  return [...new Uint8Array(mac)].map((b) => b.toString(16).padStart(2, "0")).join("");
}

// signedPost sends the signed, empty-bodied POST both crons use. Each call gets
// a fresh timestamp, hence a fresh signature.
async function signedPost(env, url, label) {
  if (!env.AUTOMATION_TICK_SECRET || !url) {
    throw new Error(`AUTOMATION_TICK_SECRET and the ${label} URL must be configured`);
  }
  const body = "{}";
  const ts = Math.floor(Date.now() / 1000).toString();
  const method = "POST";
  const signature = await sign(env.AUTOMATION_TICK_SECRET, method, new URL(url).pathname, ts, body);
  const res = await fetch(url, {
    method,
    headers: {
      "Content-Type": "application/json",
      "X-Automation-Timestamp": ts,
      "X-Automation-Signature": signature,
    },
    body,
  });
  const text = await res.text();
  console.log(`${label} ${res.status}: ${text}`);
  if (!res.ok) {
    // Throwing marks the cron invocation failed in the Cloudflare dashboard.
    throw new Error(`${label} failed with HTTP ${res.status}`);
  }
}

function tick(env) {
  return signedPost(env, env.TICK_URL, "automation tick");
}

// usageRollup runs the daily metering rollup. Deliberately skips platformEnabled.
function usageRollup(env) {
  return signedPost(env, env.USAGE_ROLLUP_URL, "usage rollup");
}

export default {
  async scheduled(controller, env, ctx) {
    ctx.waitUntil(controller.cron === USAGE_ROLLUP_CRON ? usageRollup(env) : run(env));
  },
};
