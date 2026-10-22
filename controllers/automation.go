package controllers

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"time"

	"stonesuite-backend/automation"
	"stonesuite-backend/middleware"
	"stonesuite-backend/models"
)

// maxTickBodyBytes caps the signed tick body (the Worker sends "{}").
const maxTickBodyBytes = 4 << 10

// Ticker is the automation work one tick triggers (automation.Runner).
type Ticker interface {
	Tick(ctx context.Context, now time.Time) (automation.Summary, error)
}

// PlatformGate reports whether platform-wide automation is switched on
// (automation.PlatformStore). The Worker normally never calls while it is off;
// this is the backstop for a stale KV mirror.
type PlatformGate interface {
	Enabled(ctx context.Context) (bool, error)
}

// AutomationOps serves POST /api/internal/automation/tick — the machine-only
// entry point a Cloudflare Cron Trigger calls every 15 minutes to wake the
// (scale-to-zero) app and run date-triggered work across tenants.
type AutomationOps struct {
	ticker  Ticker
	gate    PlatformGate
	mirror  automationMirror
	secret  string
	replays *middleware.ReplayGuard
	now     func() time.Time
}

// NewAutomationOps builds the handler. An empty secret disables the route. A
// nil gate fails closed (the tick is skipped). mirror may be nil or an
// unconfigured *automation.KVMirror: when the tick finds the platform switched
// off it then re-writes "off" to KV (best effort), so a stale "on" mirror costs
// one wasted wake instead of one every 15 minutes.
func NewAutomationOps(ticker Ticker, gate PlatformGate, mirror automationMirror, secret string) *AutomationOps {
	return &AutomationOps{ticker: ticker, gate: gate, mirror: mirror, secret: secret, replays: middleware.NewReplayGuard(), now: time.Now}
}

// tickSkipPlatformDisabled is the Skipped reason when the platform switch is off.
const tickSkipPlatformDisabled = "platform_disabled"

type tickSkippedResponse struct {
	Success bool   `json:"success"`
	Skipped string `json:"skipped"`
}

type tickResponse struct {
	Success bool `json:"success"`
	automation.Summary
}

// Tick handles POST /api/internal/automation/tick. No JWT and no
// TenantResolver: the caller is not a user and the work spans tenants, so the
// HMAC signature over timestamp+body is the only credential. The work runs
// synchronously — Fly stops an idle machine once no request is in flight, so a
// fire-and-forget goroutine could be killed mid-tick.
func (h *AutomationOps) Tick(w http.ResponseWriter, r *http.Request) {
	if h.secret == "" {
		writeJSON(w, http.StatusNotFound, models.APIResponse{Success: false, Message: "API route not found."})
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxTickBodyBytes))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, models.APIResponse{Success: false, Message: "Invalid request body."})
		return
	}
	sig := r.Header.Get(middleware.InternalSignatureHeader)
	if err := middleware.VerifyInternalSignature(h.secret, r.Method, r.URL.Path,
		r.Header.Get(middleware.InternalTimestampHeader), sig, body, h.now()); err != nil {
		logSecurityEvent(r, "automation_tick_bad_signature", "reason", err.Error())
		writeJSON(w, http.StatusUnauthorized, models.APIResponse{Success: false, Message: "Unauthorized."})
		return
	}
	if !h.replays.FirstUse(sig, h.now()) {
		logSecurityEvent(r, "automation_tick_replay")
		writeJSON(w, http.StatusUnauthorized, models.APIResponse{Success: false, Message: "Unauthorized."})
		return
	}

	on := false
	if h.gate != nil {
		var err error
		if on, err = h.gate.Enabled(r.Context()); err != nil {
			slog.Error("automation platform switch unreadable; skipping tick", "err", err)
			writeJSON(w, http.StatusServiceUnavailable, models.APIResponse{Success: false, Message: "Automation switch unavailable."})
			return
		}
	}
	if !on {
		h.healKVOff(r.Context())
		writeJSON(w, http.StatusOK, tickSkippedResponse{Success: true, Skipped: tickSkipPlatformDisabled})
		return
	}

	started := h.now()
	sum, err := h.ticker.Tick(r.Context(), started)
	if err != nil {
		slog.Error("automation tick failed", "err", err)
		writeJSON(w, http.StatusInternalServerError, models.APIResponse{Success: false, Message: "Automation tick failed."})
		return
	}
	slog.Info("automation tick complete", "tenants", sum.Tenants, "busy", sum.Busy,
		"transitioned", sum.Transitioned, "skipped", sum.Skipped, "failed", sum.Failed, "sent", sum.Sent, "failed_sends", sum.FailedSends,
		"duration_ms", time.Since(started).Milliseconds())
	writeJSON(w, http.StatusOK, tickResponse{Success: true, Summary: sum})
}

// healKVOff re-asserts "off" in the KV mirror after a tick reached a disabled
// platform, which means the Worker's view was stale. Best effort: a failure is
// logged and the next stale tick tries again.
func (h *AutomationOps) healKVOff(ctx context.Context) {
	if h.mirror == nil || !h.mirror.Configured() {
		return
	}
	if err := h.mirror.Set(ctx, false); err != nil {
		slog.Error("automation kv self-heal failed", "err", err)
		return
	}
	slog.Warn("automation tick reached a disabled platform; kv mirror reset to off")
}
