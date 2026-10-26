package cache

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTTLCache_Expiry(t *testing.T) {
	c := New[string, int](20 * time.Millisecond)
	c.Set("a", 1)
	v, ok := c.Get("a")
	assert.True(t, ok)
	assert.Equal(t, 1, v)
	time.Sleep(40 * time.Millisecond)
	_, ok = c.Get("a")
	assert.False(t, ok)
}

func TestTTLCache_MaxEvictsExpiredThenNearestExpiry(t *testing.T) {
	c := NewWithMax[string, int](time.Minute, 2)
	c.SetWithTTL("short", 1, time.Second)
	c.Set("long", 2)
	c.Set("new", 3) // full, nothing expired → evict nearest expiry ("short")
	assert.Equal(t, 2, c.Len())
	_, ok := c.Get("short")
	assert.False(t, ok)
	_, ok = c.Get("long")
	assert.True(t, ok)

	c2 := NewWithMax[string, int](time.Minute, 2)
	c2.SetWithTTL("dead", 1, time.Nanosecond)
	c2.Set("live", 2)
	time.Sleep(time.Millisecond)
	c2.Set("new", 3) // expired entry is freed, live one survives
	_, ok = c2.Get("live")
	assert.True(t, ok)
	assert.Equal(t, 2, c2.Len())
}

func TestTTLCache_GetOrLoadSingleflight(t *testing.T) {
	c := New[string, int](time.Minute)
	var loads atomic.Int32
	release := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			v, err := c.GetOrLoad("k", func() (int, error) {
				loads.Add(1)
				<-release
				return 42, nil
			})
			assert.NoError(t, err)
			assert.Equal(t, 42, v)
		}()
	}
	time.Sleep(50 * time.Millisecond)
	close(release)
	wg.Wait()
	assert.Equal(t, int32(1), loads.Load())

	v, err := c.GetOrLoad("k", func() (int, error) { return 0, errors.New("must not run") })
	require.NoError(t, err)
	assert.Equal(t, 42, v)
}

func TestTTLCache_GetOrLoadDoesNotCacheErrors(t *testing.T) {
	c := New[string, int](time.Minute)
	boom := errors.New("boom")
	_, err := c.GetOrLoad("k", func() (int, error) { return 0, boom })
	assert.ErrorIs(t, err, boom)
	v, err := c.GetOrLoad("k", func() (int, error) { return 7, nil })
	require.NoError(t, err)
	assert.Equal(t, 7, v)
}

func TestMemoryStore(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()

	_, ok := s.Get(ctx, "k")
	assert.False(t, ok)

	in := []byte("hello")
	s.Set(ctx, "k", in, time.Minute)
	in[0] = 'X' // stored value must be a copy
	got, ok := s.Get(ctx, "k")
	assert.True(t, ok)
	assert.Equal(t, "hello", string(got))
	got[0] = 'Y' // returned value must be a copy
	again, _ := s.Get(ctx, "k")
	assert.Equal(t, "hello", string(again))

	s.Del(ctx, "k")
	_, ok = s.Get(ctx, "k")
	assert.False(t, ok)

	s.Set(ctx, "short", []byte("x"), 10*time.Millisecond)
	time.Sleep(25 * time.Millisecond)
	_, ok = s.Get(ctx, "short")
	assert.False(t, ok)
}

func TestMemoryStore_Incr(t *testing.T) {
	ctx := context.Background()
	s := NewMemoryStore()
	for want := int64(1); want <= 3; want++ {
		n, err := s.Incr(ctx, "c", time.Minute)
		require.NoError(t, err)
		assert.Equal(t, want, n)
	}
	s.Del(ctx, "c")
	n, _ := s.Incr(ctx, "c", time.Minute)
	assert.Equal(t, int64(1), n)

	s.Incr(ctx, "w", 10*time.Millisecond) //nolint:errcheck
	time.Sleep(25 * time.Millisecond)
	n, _ = s.Incr(ctx, "w", time.Minute)
	assert.Equal(t, int64(1), n, "expired window restarts")
}

func TestTenantKey(t *testing.T) {
	tests := []struct {
		name     string
		env, tid string
		parts    []string
		want     string
	}{
		{"basic", "prod", "t1", []string{"lookups"}, "ss:prod:t:t1:mod:lookups"},
		{"no parts", "dev", "t1", nil, "ss:dev:t:t1:mod"},
		{"empty tenant is uncacheable", "prod", "", []string{"x"}, ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, TenantKey(tc.env, tc.tid, "mod", tc.parts...))
		})
	}
}
