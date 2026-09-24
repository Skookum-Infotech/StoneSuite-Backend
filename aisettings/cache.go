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
}

// NewCache builds a Cache with the standard 30s TTL. now is the clock to use;
// pass nil for time.Now in production, or a fake clock in tests.
func NewCache(now clock) *Cache {
	if now == nil {
		now = time.Now
	}
	return &Cache{ttl: cacheTTL, now: now, items: make(map[string]cacheEntry)}
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

// InvalidatePlatform drops the cached platform switch, forcing the next read
// back to the database. Called after a successful platform-settings write.
func (c *Cache) InvalidatePlatform() {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.items, PlatformKey)
}

// InvalidateTenant drops the cached switch for one tenant, forcing the next
// read back to the database. Called after a successful tenant-settings write.
func (c *Cache) InvalidateTenant(tenantKey string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.items, tenantKey)
}

// platformEnabledCached returns the platform switch, reading through the
// cache and falling back to the database on a miss.
func (c *Cache) platformEnabledCached(ctx context.Context, cpPool *pgxpool.Pool) (bool, error) {
	if v, ok := c.get(PlatformKey); ok {
		return v, nil
	}
	v, err := PlatformEnabled(ctx, cpPool)
	if err != nil {
		return false, err
	}
	c.set(PlatformKey, v)
	return v, nil
}

// tenantEnabledCached returns tenantKey's own switch, reading through the
// cache and falling back to the database on a miss.
func (c *Cache) tenantEnabledCached(ctx context.Context, tenantPool *pgxpool.Pool, tenantKey string) (bool, error) {
	if v, ok := c.get(tenantKey); ok {
		return v, nil
	}
	v, err := TenantEnabled(ctx, tenantPool)
	if err != nil {
		return false, err
	}
	c.set(tenantKey, v)
	return v, nil
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
