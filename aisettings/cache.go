package aisettings

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// cacheTTL is how long a cached toggle value is trusted before the next read
// goes back to the database. A write always invalidates its own key
// immediately (see InvalidatePlatform/InvalidateTenant), so this TTL is only
// a backstop against a toggle changed by another process/replica.
const cacheTTL = 30 * time.Second

// PlatformKey is the fixed cache key for the platform-wide switch — there is
// exactly one row, so it needs no tenant qualifier.
const PlatformKey = "platform"

// clock abstracts time.Now so cache-TTL tests are deterministic.
type clock func() time.Time

type cacheEntry struct {
	enabled   bool
	expiresAt time.Time
}

// Cache is a tiny mutex-guarded TTL cache for the platform and per-tenant AI
// toggles, keyed PlatformKey for the platform row and by tenant ID for
// tenant rows. It removes a DB round trip from the hot gating path (every
// ask/warm/reindex request) at the cost of up to cacheTTL of staleness on a
// toggle that changed elsewhere; the write path below invalidates its own
// key immediately to keep that window tight for the common case (an admin
// toggles it and then checks the effect).
type Cache struct {
	mu    sync.Mutex
	ttl   time.Duration
	now   clock
	items map[string]cacheEntry
	// gen counts invalidations per key. A reader captures it before its
	// database read and stores the result only if it is unchanged, so a read
	// that started before a write can't re-cache the pre-write value after the
	// write's Invalidate.
	gen map[string]uint64
}

// NewCache builds a Cache with the standard 30s TTL. now is the clock to use;
// pass nil for time.Now in production, or a fake clock in tests.
func NewCache(now clock) *Cache {
	if now == nil {
		now = time.Now
	}
	return &Cache{ttl: cacheTTL, now: now, items: make(map[string]cacheEntry), gen: make(map[string]uint64)}
}

// get returns the cached value for key, if present and unexpired.
func (c *Cache) get(key string) (bool, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.items[key]
	if !ok || c.now().After(e.expiresAt) {
		return false, false
	}
	return e.enabled, true
}

// set stores value for key, resetting its TTL.
func (c *Cache) set(key string, value bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.items[key] = cacheEntry{enabled: value, expiresAt: c.now().Add(c.ttl)}
}

// generation returns key's current invalidation count.
func (c *Cache) generation(key string) uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.gen[key]
}

// setIfGeneration stores value for key only if no invalidation of key has
// happened since gen was captured; otherwise the value is possibly stale and
// is dropped (the caller still returns it, just doesn't cache it).
func (c *Cache) setIfGeneration(key string, value bool, gen uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.gen[key] != gen {
		return
	}
	c.items[key] = cacheEntry{enabled: value, expiresAt: c.now().Add(c.ttl)}
}

// invalidate drops key and bumps its generation.
func (c *Cache) invalidate(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.items, key)
	c.gen[key]++
}

// cached returns key's value from the cache, or fetches it and stores it
// unless key was invalidated while the fetch was in flight.
func (c *Cache) cached(ctx context.Context, key string, fetch func(context.Context) (bool, error)) (bool, error) {
	if v, ok := c.get(key); ok {
		return v, nil
	}
	gen := c.generation(key)
	v, err := fetch(ctx)
	if err != nil {
		return false, err
	}
	c.setIfGeneration(key, v, gen)
	return v, nil
}

// InvalidatePlatform drops the cached platform switch, forcing the next read
// back to the database. Called after a successful platform-settings write.
func (c *Cache) InvalidatePlatform() {
	c.invalidate(PlatformKey)
}

// InvalidateTenant drops the cached switch for one tenant, forcing the next
// read back to the database. Called after a successful tenant-settings write.
func (c *Cache) InvalidateTenant(tenantKey string) {
	c.invalidate(tenantKey)
}

// platformEnabledCached returns the platform switch, reading through the
// cache and falling back to the database on a miss.
func (c *Cache) platformEnabledCached(ctx context.Context, cpPool *pgxpool.Pool) (bool, error) {
	return c.cached(ctx, PlatformKey, func(ctx context.Context) (bool, error) {
		return PlatformEnabled(ctx, cpPool)
	})
}

// tenantEnabledCached returns tenantKey's own switch, reading through the
// cache and falling back to the database on a miss.
func (c *Cache) tenantEnabledCached(ctx context.Context, tenantPool *pgxpool.Pool, tenantKey string) (bool, error) {
	return c.cached(ctx, tenantKey, func(ctx context.Context) (bool, error) {
		return TenantEnabled(ctx, tenantPool)
	})
}

// Status resolves the combined platform + tenant switch state for tenantKey
// (the tenant's ID), reading each through the cache.
func (c *Cache) Status(ctx context.Context, cpPool, tenantPool *pgxpool.Pool, tenantKey string) (Status, error) {
	platform, err := c.platformEnabledCached(ctx, cpPool)
	if err != nil {
		return Status{}, fmt.Errorf("load platform ai toggle: %w", err)
	}
	tenant, err := c.tenantEnabledCached(ctx, tenantPool, tenantKey)
	if err != nil {
		return Status{}, fmt.Errorf("load tenant ai toggle: %w", err)
	}
	return Status{PlatformEnabled: platform, TenantEnabled: tenant, Available: Available(platform, tenant)}, nil
}
