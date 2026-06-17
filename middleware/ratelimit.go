package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"stonesuite-backend/models"
)

// tenantBucket is a token bucket for one tenant: refills at RateLimiter.rate
// tokens/sec, up to RateLimiter.burst tokens.
type tenantBucket struct {
	mu       sync.Mutex
	tokens   float64
	lastFill time.Time
}

// RateLimiter enforces a per-tenant request rate using a token bucket per
// tenant ID. It protects the shared Neon compute from a single tenant's
// burst starving the other tenants on the same compute (ADR-3).
type RateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*tenantBucket
	rate    float64 // tokens added per second
	burst   float64 // max tokens a tenant can accumulate (burst capacity)
}

// cleanupInterval is how often the idle-bucket eviction goroutine runs.
const cleanupInterval = 5 * time.Minute

// NewRateLimiter builds a RateLimiter allowing each tenant up to `rate`
// requests/sec sustained, with bursts up to `burst` requests. The ctx
// controls the lifetime of the background eviction goroutine — pass a context
// derived from the server's shutdown context so the goroutine exits cleanly.
func NewRateLimiter(ctx context.Context, rate, burst float64) *RateLimiter {
	rl := &RateLimiter{
		buckets: make(map[string]*tenantBucket),
		rate:    rate,
		burst:   burst,
	}
	go rl.evictIdle(ctx)
	return rl
}

// evictIdle runs on a ticker and removes buckets that have been idle long
// enough to be fully refilled — i.e. buckets that consume no memory value.
// A bucket is considered idle when its lastFill is older than burst/rate
// seconds (the time it takes to refill from zero to burst capacity).
func (rl *RateLimiter) evictIdle(ctx context.Context) {
	ticker := time.NewTicker(cleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			rl.mu.Lock()
			idleThreshold := time.Duration(rl.burst/rl.rate) * time.Second
			cutoff := time.Now().Add(-idleThreshold)
			for id, b := range rl.buckets {
				b.mu.Lock()
				if b.lastFill.Before(cutoff) {
					delete(rl.buckets, id)
				}
				b.mu.Unlock()
			}
			rl.mu.Unlock()
		}
	}
}

// allow reports whether tenantID may make a request now, consuming one token
// from its bucket if so.
func (rl *RateLimiter) allow(tenantID string) bool {
	rl.mu.Lock()
	b, ok := rl.buckets[tenantID]
	if !ok {
		b = &tenantBucket{tokens: rl.burst, lastFill: time.Now()}
		rl.buckets[tenantID] = b
	}
	rl.mu.Unlock()

	b.mu.Lock()
	defer b.mu.Unlock()

	now := time.Now()
	b.tokens += now.Sub(b.lastFill).Seconds() * rl.rate
	if b.tokens > rl.burst {
		b.tokens = rl.burst
	}
	b.lastFill = now

	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// PerTenant returns middleware that rate-limits requests by the authenticated
// request's tenant_id. It MUST run after RequireAuth (which populates the
// tenant id from the JWT). Requests without a tenant_id (platform-admin or
// legacy tokens) are not rate-limited here.
func (rl *RateLimiter) PerTenant(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		payload, err := GetUserFromContext(r.Context())
		if err != nil || payload.TenantID == "" {
			next.ServeHTTP(w, r)
			return
		}

		if !rl.allow(payload.TenantID) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_ = json.NewEncoder(w).Encode(models.APIResponse{
				Success: false,
				Message: "Too many requests for this workspace. Please slow down and try again shortly.",
			})
			return
		}

		next.ServeHTTP(w, r)
	})
}
