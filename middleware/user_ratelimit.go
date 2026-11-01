package middleware

import (
	"math"
	"sync"
	"time"
)

// UserRateLimiter is a small per-key token bucket for expensive endpoints
// (keyed by identity id). It has no background goroutine: idle full buckets
// are evicted lazily during access, so there is nothing to shut down.
type UserRateLimiter struct {
	mu      sync.Mutex
	buckets map[string]*tenantBucket
	rate    float64 // tokens added per second
	burst   float64 // max tokens a key can accumulate
	now     func() time.Time
	sweepAt time.Time
}

// userLimiterSweepEvery is the minimum gap between lazy idle-bucket sweeps.
const userLimiterSweepEvery = 5 * time.Minute

// NewUserRateLimiter allows each key `rate` requests/sec sustained with bursts
// up to `burst`.
func NewUserRateLimiter(rate, burst float64) *UserRateLimiter {
	return &UserRateLimiter{
		buckets: make(map[string]*tenantBucket),
		rate:    rate,
		burst:   burst,
		now:     time.Now,
	}
}

// Allow reports whether key may proceed now, consuming one token if so.
func (l *UserRateLimiter) Allow(key string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	l.sweep(now)

	b, ok := l.buckets[key]
	if !ok {
		b = &tenantBucket{tokens: l.burst, lastFill: now}
		l.buckets[key] = b
	}
	b.tokens = math.Min(l.burst, b.tokens+now.Sub(b.lastFill).Seconds()*l.rate)
	b.lastFill = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// RetryAfterSeconds is the wait, in whole seconds, for one token to refill.
func (l *UserRateLimiter) RetryAfterSeconds() int {
	if l.rate <= 0 {
		return 1
	}
	return int(math.Ceil(1 / l.rate))
}

// sweep drops buckets idle long enough to be full again; caller holds l.mu.
func (l *UserRateLimiter) sweep(now time.Time) {
	if now.Before(l.sweepAt) {
		return
	}
	l.sweepAt = now.Add(userLimiterSweepEvery)
	if l.rate <= 0 {
		return
	}
	idle := time.Duration(l.burst / l.rate * float64(time.Second))
	for k, b := range l.buckets {
		if now.Sub(b.lastFill) > idle {
			delete(l.buckets, k)
		}
	}
}
