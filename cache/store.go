package cache

import (
	"context"
	"sync"
	"time"
)

// DefaultMemoryStoreMax bounds a MemoryStore built with NewMemoryStore.
const DefaultMemoryStoreMax = 10000

// Store is the byte-level cache contract that handlers and middleware depend
// on, so a shared backend (e.g. Redis) can replace the in-process one without
// touching callers. Keys MUST embed the tenant id (see TenantKey) — a key
// without it is a cross-tenant data leak.
type Store interface {
	// Get returns the value for key and whether it was present and unexpired.
	Get(ctx context.Context, key string) ([]byte, bool)
	// Set stores val under key for ttl.
	Set(ctx context.Context, key string, val []byte, ttl time.Duration)
	// Del removes the given keys.
	Del(ctx context.Context, keys ...string)
	// Incr atomically increments the counter at key and returns the new value.
	// The ttl is applied when the counter is created, not on each increment.
	Incr(ctx context.Context, key string, ttl time.Duration) (int64, error)
}

type counter struct {
	n         int64
	expiresAt time.Time
}

// MemoryStore is the in-process Store: values live in a bounded TTLCache and
// counters in a small mutex-guarded map.
type MemoryStore struct {
	values *TTLCache[string, []byte]

	mu       sync.Mutex
	counters map[string]counter
}

// NewMemoryStore builds an in-process Store bounded at DefaultMemoryStoreMax.
func NewMemoryStore() *MemoryStore { return NewMemoryStoreWithMax(DefaultMemoryStoreMax) }

// NewMemoryStoreWithMax builds an in-process Store holding at most max values.
func NewMemoryStoreWithMax(max int) *MemoryStore {
	return &MemoryStore{
		values:   NewWithMax[string, []byte](time.Minute, max),
		counters: make(map[string]counter),
	}
}

// Get implements Store. The returned slice is a copy.
func (m *MemoryStore) Get(_ context.Context, key string) ([]byte, bool) {
	v, ok := m.values.Get(key)
	if !ok {
		return nil, false
	}
	return append([]byte(nil), v...), true
}

// Set implements Store. The value is copied.
func (m *MemoryStore) Set(_ context.Context, key string, val []byte, ttl time.Duration) {
	m.values.SetWithTTL(key, append([]byte(nil), val...), ttl)
}

// Del implements Store.
func (m *MemoryStore) Del(_ context.Context, keys ...string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, k := range keys {
		m.values.Delete(k)
		delete(m.counters, k)
	}
}

// Incr implements Store.
func (m *MemoryStore) Incr(_ context.Context, key string, ttl time.Duration) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	c, ok := m.counters[key]
	if !ok || now.After(c.expiresAt) {
		c = counter{expiresAt: now.Add(ttl)}
		m.sweepLocked(now)
	}
	c.n++
	m.counters[key] = c
	return c.n, nil
}

// sweepLocked drops expired counters so abandoned keys cannot accumulate.
func (m *MemoryStore) sweepLocked(now time.Time) {
	for k, c := range m.counters {
		if now.After(c.expiresAt) {
			delete(m.counters, k)
		}
	}
}

// TenantKey builds a namespaced cache key that always carries the tenant id.
// An empty tenantID yields an empty key, which callers must treat as
// uncacheable.
func TenantKey(env, tenantID, module string, parts ...string) string {
	if tenantID == "" {
		return ""
	}
	k := "ss:" + env + ":t:" + tenantID + ":" + module
	for _, p := range parts {
		k += ":" + p
	}
	return k
}
