package middleware

import (
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

// NewRateLimiter builds a RateLimiter allowing each tenant up to `rate`
// requests/sec sustained, with bursts up to `burst` requests.
func NewRateLimiter(rate, burst float64) *RateLimiter {
	return &RateLimiter{
		buckets: make(map[string]*tenantBucket),
		rate:    rate,
		burst:   burst,
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
