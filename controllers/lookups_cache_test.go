package controllers

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/cache"
)

func TestInventoryLookupOps_CachedReadAndInvalidate(t *testing.T) {
	h := NewInventoryLookupOps()
	ctx := context.Background() // no tenant in ctx → must bypass cache
	var loads atomic.Int32
	load := func() (map[string]any, error) { loads.Add(1); return map[string]any{"n": int(loads.Load())}, nil }

	_, err := h.cachedRead(ctx, []string{"all"}, load)
	require.NoError(t, err)
	_, err = h.cachedRead(ctx, []string{"all"}, load)
	require.NoError(t, err)
	assert.Equal(t, int32(2), loads.Load(), "no tenant in context must never be cached")

	h.invalidate(ctx) // must not panic without a tenant
}

func TestInventoryLookupOps_CacheKeyIsTenantScoped(t *testing.T) {
	a := cache.TenantKey("", "tenant-a", lookupCacheKey, "all")
	b := cache.TenantKey("", "tenant-b", lookupCacheKey, "all")
	assert.NotEqual(t, a, b)
	assert.Empty(t, cache.TenantKey("", "", lookupCacheKey, "all"))
}

func TestCRMLookups_StaticCacheBypassWithoutTenant(t *testing.T) {
	h := NewCRMLookups()
	var loads atomic.Int32
	for i := 0; i < 2; i++ {
		_, err := h.statics.GetOrLoad("t1", func() (*crmStaticLookups, error) {
			loads.Add(1)
			return &crmStaticLookups{}, nil
		})
		require.NoError(t, err)
	}
	assert.Equal(t, int32(1), loads.Load(), "second read is served from cache")

	_, err := h.statics.GetOrLoad("t2", func() (*crmStaticLookups, error) {
		return nil, &lookupLoadError{msg: "Failed to load x.", err: errors.New("db")}
	})
	var le *lookupLoadError
	require.ErrorAs(t, err, &le)
	assert.Equal(t, "Failed to load x.", le.msg)
	_, ok := h.statics.Get("t2")
	assert.False(t, ok, "errors are never cached")
}
