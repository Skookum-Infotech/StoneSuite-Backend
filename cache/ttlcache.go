// Package cache provides a minimal in-process, short-TTL cache for hot,
// rarely-changing reads (RBAC effective grants, workflow definitions, tenant
// registry rows). It trades a small staleness window for removing a DB
// round-trip from the request path under load (ADR-3).
package cache

import (
	"sync"
	"time"
)

type entry[V any] struct {
	value     V
	expiresAt time.Time
}

// call is one in-flight loader shared by every concurrent GetOrLoad caller of
// the same key.
type call[V any] struct {
	wg  sync.WaitGroup
	val V
	err error
}

// TTLCache is a generic in-memory cache where entries expire lazily on Get.
// Callers that mutate the underlying data must call Delete/DeleteFunc to
// invalidate the relevant entries — the TTL alone is a backstop, not the
// invalidation mechanism. A positive max bounds the entry count.
type TTLCache[K comparable, V any] struct {
	mu       sync.Mutex
	ttl      time.Duration
	max      int // 0 = unbounded
	items    map[K]entry[V]
	inflight map[K]*call[V]
}

// New builds an unbounded TTLCache whose entries are stale after ttl.
func New[K comparable, V any](ttl time.Duration) *TTLCache[K, V] {
	return NewWithMax[K, V](ttl, 0)
}

// NewWithMax builds a TTLCache holding at most max entries (0 = unbounded).
// When full, expired entries are dropped first, then the entry closest to
// expiry is evicted.
func NewWithMax[K comparable, V any](ttl time.Duration, max int) *TTLCache[K, V] {
	return &TTLCache[K, V]{
		ttl:      ttl,
		max:      max,
		items:    make(map[K]entry[V]),
		inflight: make(map[K]*call[V]),
	}
}

// Get returns the cached value for key if present and not yet expired.
func (c *TTLCache[K, V]) Get(key K) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.getLocked(key)
}

func (c *TTLCache[K, V]) getLocked(key K) (V, bool) {
	e, ok := c.items[key]
	if !ok || time.Now().After(e.expiresAt) {
		var zero V
		return zero, false
	}
	return e.value, true
}

// Set stores value for key, resetting its TTL.
func (c *TTLCache[K, V]) Set(key K, value V) {
	c.SetWithTTL(key, value, c.ttl)
}

// SetWithTTL stores value for key with an explicit ttl for this entry.
func (c *TTLCache[K, V]) SetWithTTL(key K, value V, ttl time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.items[key]; !exists && c.max > 0 && len(c.items) >= c.max {
		c.evictLocked()
	}
	c.items[key] = entry[V]{value: value, expiresAt: time.Now().Add(ttl)}
}

// evictLocked frees a slot: expired entries go first; if none were expired,
// the entry nearest to expiry is dropped.
func (c *TTLCache[K, V]) evictLocked() {
	now := time.Now()
	freed := false
	var oldestKey K
	var oldest time.Time
	haveOldest := false
	for k, e := range c.items {
		if now.After(e.expiresAt) {
			delete(c.items, k)
			freed = true
			continue
		}
		if !haveOldest || e.expiresAt.Before(oldest) {
			oldestKey, oldest, haveOldest = k, e.expiresAt, true
		}
	}
	if !freed && haveOldest {
		delete(c.items, oldestKey)
	}
}

// GetOrLoad returns the cached value for key or, on a miss, runs load exactly
// once no matter how many callers miss concurrently (singleflight); the rest
// wait for and share that result. Errors are returned to every waiter and are
// never cached. A Delete issued while a load is running does not stop that
// load from storing its result, so keep loads short.
func (c *TTLCache[K, V]) GetOrLoad(key K, load func() (V, error)) (V, error) {
	c.mu.Lock()
	if v, ok := c.getLocked(key); ok {
		c.mu.Unlock()
		return v, nil
	}
	if cl, ok := c.inflight[key]; ok {
		c.mu.Unlock()
		cl.wg.Wait()
		return cl.val, cl.err
	}
	cl := &call[V]{}
	cl.wg.Add(1)
	c.inflight[key] = cl
	c.mu.Unlock()

	defer func() {
		c.mu.Lock()
		delete(c.inflight, key)
		c.mu.Unlock()
		cl.wg.Done()
	}()
	cl.val, cl.err = load()
	if cl.err == nil {
		c.Set(key, cl.val)
	}
	return cl.val, cl.err
}

// Delete removes key, if present.
func (c *TTLCache[K, V]) Delete(key K) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.items, key)
}

// DeleteFunc removes every entry whose key satisfies match.
func (c *TTLCache[K, V]) DeleteFunc(match func(K) bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.items {
		if match(k) {
			delete(c.items, k)
		}
	}
}

// Len reports the number of stored entries, including not-yet-swept expired ones.
func (c *TTLCache[K, V]) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.items)
}
