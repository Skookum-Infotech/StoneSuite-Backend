// automation-cron — Cloudflare Worker whose only job is to be the clock for
// stonesuite-backend. The backend scales to zero on Fly, so it cannot keep its
// own ticker; every 15 minutes this Worker sends a signed
// POST /api/internal/automation/tick, which wakes the machine and runs
// date-triggered work (e.g. dispatching scheduled vendor payments).
//
// Signature: hex(HMAC-SHA256(AUTOMATION_TICK_SECRET,
// `${method} ${path}\n${ts}\n${body}`)), sent as X-Automation-Timestamp /
// X-Automation-Signature. Must match middleware/internal_sig.go (SignInternal)
// in the backend. Each tick gets a fresh timestamp, hence a fresh signature —
// the backend rejects a signature it has already accepted.

const encoder = new TextEncoder();

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

async function tick(env) {
  if (!env.AUTOMATION_TICK_SECRET || !env.TICK_URL) {
    throw new Error("AUTOMATION_TICK_SECRET and TICK_URL must be configured");
  }
  const body = "{}";
  const ts = Math.floor(Date.now() / 1000).toString();
  const method = "POST";
  const signature = await sign(env.AUTOMATION_TICK_SECRET, method, new URL(env.TICK_URL).pathname, ts, body);
  const res = await fetch(env.TICK_URL, {
    method,
    headers: {
      "Content-Type": "application/json",
      "X-Automation-Timestamp": ts,
      "X-Automation-Signature": signature,
    },
    body,
  });
  const text = await res.text();
  console.log(`automation tick ${res.status}: ${text}`);
  if (!res.ok) {
    // Throwing marks the cron invocation failed in the Cloudflare dashboard.
    throw new Error(`automation tick failed with HTTP ${res.status}`);
  }
}

export default {
  async scheduled(_controller, env, ctx) {
    ctx.waitUntil(tick(env));
  },
};
