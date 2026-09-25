package controllers

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	ragcore "github.com/Skookum-Infotech/go-rag/rag"

	"stonesuite-backend/middleware"
	"stonesuite-backend/tenancy"
)

// warmBoundedTimeout bounds the detached goroutine POST /api/tenant/ai/warm
// launches (EnsureRunning + one trivial embed/chat call) — its only exit
// strategy, since the request that triggered it has already returned. Raised
// from 60s to cover a cold Fly Machine start (can itself take up to ~60s)
// PLUS Ollama's own model-load latency on first inference, which the
// original 60s budget did not leave any room for.
const warmBoundedTimeout = 4 * time.Minute

// warmCooldown is how long a *successful* warm-up is trusted before another
// is worth paying for. POST /warm is meant to be hit repeatedly (e.g. on
// every app open) without re-warming the shared Ollama box on each call.
const warmCooldown = 5 * time.Minute

// assistantWarmer single-flights the detached warm-up POST
// /api/tenant/ai/warm triggers: at most one in flight at a time, and none
// within warmCooldown of the last success. now is injected so tests don't
// depend on wall-clock timing.
type assistantWarmer struct {
	mu          sync.Mutex
	inFlight    bool
	lastSuccess time.Time
	now         func() time.Time
}

// newAssistantWarmer builds a warmer using now as its clock; pass nil for
// time.Now in production, or a fake clock in tests.
func newAssistantWarmer(now func() time.Time) *assistantWarmer {
	if now == nil {
		now = time.Now
	}
	return &assistantWarmer{now: now}
}

// start reports whether the caller should launch a new warm-up now: false
// when one is already in flight, or the last one succeeded within
// warmCooldown. A caller that receives true MUST call finish exactly once
// when the warm-up ends.
func (a *assistantWarmer) start() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.inFlight || a.now().Sub(a.lastSuccess) < warmCooldown {
		return false
	}
	a.inFlight = true
	return true
}

// finish records a warm-up's outcome; success extends the cooldown from now.
func (a *assistantWarmer) finish(success bool) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.inFlight = false
	if success {
		a.lastSuccess = a.now()
	}
}

// Warm handles POST /api/tenant/ai/warm (behind aiChain, same as Ask):
// best-effort, fire-and-forget warm-up of the shared Ollama box, so the
// first real ask isn't the one paying model-load latency. Always returns 202
// immediately; the warm-up (if any) runs detached and is single-flighted by
// h.warmer.
func (h *AIOps) Warm(w http.ResponseWriter, r *http.Request) {
	payload, err := middleware.GetUserFromContext(r.Context())
	if err != nil || payload.ID == "" {
		fail(w, http.StatusUnauthorized, "Authentication required.")
		return
	}
	tenant, err := tenancy.TenantFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant not resolved.")
		return
	}
	pool, err := tenancy.PoolFromContext(r.Context())
	if err != nil {
		fail(w, http.StatusInternalServerError, "Tenant database not resolved.")
		return
	}

	status, err := h.aiSettings.Status(r.Context(), h.cpPool, pool, tenant.ID)
	if err != nil {
		slog.Error("ai settings status check failed", "request_id", middleware.RequestIDFromContext(r.Context()), "tenant_id", tenant.ID, "err", err)
		fail(w, http.StatusInternalServerError, "Permission check failed.")
		return
	}
	if !status.Available {
		writeAssistantDisabled(w)
		return
	}

	if h.waker != nil && h.warmer.start() {
		// Deliberately NOT r.Context(): this outlives the request, which has
		// already returned 202 by the time it finishes. WithoutCancel keeps
		// the trace/log values and drops only the cancellation; the bounded
		// timeout is this goroutine's actual exit strategy.
		ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), warmBoundedTimeout)
		go func() {
			defer cancel()
			success := h.runWarmUp(ctx, tenant.ID)
			h.warmer.finish(success)
		}()
	}

	writeJSON(w, http.StatusAccepted, map[string]any{"success": true})
}

// runWarmUp starts the Ollama box (if it isn't already up) and fires one
// trivial embed+chat call through it, logging (never failing the caller,
// since this always runs detached from any request) and reporting success.
func (h *AIOps) runWarmUp(ctx context.Context, tenantID string) bool {
	if err := h.waker.EnsureRunning(ctx); err != nil {
		slog.Warn("ai warm-up: ollama did not come up", "tenant_id", tenantID, "err", err)
		return false
	}
	if err := ragcore.WarmUp(ctx, h.queryEmbed, h.llm); err != nil {
		slog.Warn("ai warm-up failed", "tenant_id", tenantID, "err", err)
		return false
	}
	return true
}
