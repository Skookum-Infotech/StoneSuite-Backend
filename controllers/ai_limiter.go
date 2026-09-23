package controllers

import (
	"context"
	"net/http"
	"strconv"
	"sync"
	"time"

	"stonesuite-backend/models"
)

// Model-slot limits. The Ollama box behind every generation is 1 CPU: beyond
// a couple of concurrent generations, adding more only slows all of them down
// together. Per-tenant keeps one busy workspace from holding every slot.
const (
	aiGlobalSlots    = 2
	aiPerTenantSlots = 1
	// aiSlotWait is how long a request queues for a slot before giving up
	// with a busy 429 — long enough to absorb a short burst, short enough
	// that a user isn't left staring at nothing.
	aiSlotWait = 10 * time.Second
	// aiBusyRetryAfter is the Retry-After (seconds) sent with a busy 429.
	aiBusyRetryAfter = 5
)

// codeAssistantBusy marks the 429 sent when no model slot freed up in time —
// distinct from models.CodeRateLimited, since the right client reaction
// differs: retry shortly vs. slow down.
const codeAssistantBusy = "assistant_busy"

// aiLimiter bounds concurrent model generations across BOTH ask endpoints,
// globally and per tenant, with a short context-aware wait for a slot.
//
// It replaced a stream-only semaphore: the non-streaming endpoint had no cap,
// and the frontend fell back to it on every stream 429 — so under exactly the
// load the cap existed for, it was bypassed.
type aiLimiter struct {
	mu        sync.Mutex
	global    int
	perTenant int
	inUse     int
	byTenant  map[string]int
	// changed is closed (and replaced) on every release, waking every waiter
	// to re-check — a broadcast that, unlike sync.Cond, composes with ctx.
	changed chan struct{}
}

func newAILimiter(global, perTenant int) *aiLimiter {
	return &aiLimiter{global: global, perTenant: perTenant, byTenant: map[string]int{}, changed: make(chan struct{})}
}

// acquire claims a slot for tenantID, waiting until one frees up or ctx ends.
// On success the returned release must be called exactly once.
func (l *aiLimiter) acquire(ctx context.Context, tenantID string) (release func(), ok bool) {
	for {
		l.mu.Lock()
		if l.inUse < l.global && l.byTenant[tenantID] < l.perTenant {
			l.inUse++
			l.byTenant[tenantID]++
			l.mu.Unlock()
			var once sync.Once
			return func() { once.Do(func() { l.release(tenantID) }) }, true
		}
		wake := l.changed
		l.mu.Unlock()
		select {
		case <-wake:
		case <-ctx.Done():
			return nil, false
		}
	}
}

func (l *aiLimiter) release(tenantID string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.inUse--
	if l.byTenant[tenantID]--; l.byTenant[tenantID] <= 0 {
		delete(l.byTenant, tenantID)
	}
	close(l.changed)
	l.changed = make(chan struct{})
}

// writeAssistantBusy sends the busy 429. Only valid before any SSE header.
func writeAssistantBusy(w http.ResponseWriter) {
	w.Header().Set("Retry-After", strconv.Itoa(aiBusyRetryAfter))
	writeJSON(w, http.StatusTooManyRequests, models.APIResponse{
		Success: false,
		Code:    codeAssistantBusy,
		Message: "The assistant is busy with other questions right now — please try again in a few seconds.",
	})
}
