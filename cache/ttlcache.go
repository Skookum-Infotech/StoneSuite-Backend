// Package cache provides a minimal in-process, short-TTL cache for hot,
// rarely-changing reads (RBAC effective grants, workflow definitions). It
// trades a small staleness window for removing a DB round-trip from the
// request path under load (ADR-3).
package cache

import (
	"sync"
	"time"
)

type entry[V any] struct {
	value     V
	expiresAt time.Time
}

// TTLCache is a generic in-memory cache where entries expire lazily on Get.
// Callers that mutate the underlying data must call Delete/DeleteFunc to
// invalidate the relevant entries — the TTL alone is a backstop, not the
// invalidation mechanism.
type TTLCache[K comparable, V any] struct {
	mu    sync.Mutex
	ttl   time.Duration
	items map[K]entry[V]
}

// New builds a TTLCache whose entries are considered stale after ttl.
func New[K comparable, V any](ttl time.Duration) *TTLCache[K, V] {
	return &TTLCache[K, V]{ttl: ttl, items: make(map[K]entry[V])}
}

// Get returns the cached value for key if present and not yet expired.
func (c *TTLCache[K, V]) Get(key K) (V, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.items[key]
	if !ok || time.Now().After(e.expiresAt) {
		var zero V
		return zero, false
	}
	return e.value, true
}

// Set stores value for key, resetting its TTL.
func (c *TTLCache[K, V]) Set(key K, value V) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[key] = entry[V]{value: value, expiresAt: time.Now().Add(c.ttl)}
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
