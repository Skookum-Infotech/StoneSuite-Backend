package tenancy

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errCPDown = errors.New("control plane down")

// staleTestCP builds a ControlPlane whose loader returns the supplied results in order.
func staleTestCP(results *[]func() (*Tenant, error)) *ControlPlane {
	return &ControlPlane{
		tenants:   newTenantCache(),
		lastKnown: newLastKnownCache(),
		load: func(ctx context.Context, id string) (*Tenant, error) {
			f := (*results)[0]
			*results = (*results)[1:]
			return f()
		},
	}
}

func okTenant() (*Tenant, error)      { return &Tenant{ID: "t1", Slug: "acme"}, nil }
func downTenant() (*Tenant, error)    { return nil, errCPDown }
func missingTenant() (*Tenant, error) { return nil, ErrTenantNotFound }

func TestTenantByID_StaleFallback(t *testing.T) {
	tests := []struct {
		name    string
		steps   []func() (*Tenant, error)
		prep    func(c *ControlPlane) // run after the first call, before the last
		wantErr error
		wantOK  bool
	}{
		{"success is returned", []func() (*Tenant, error){okTenant}, nil, nil, true},
		{"error with recent lastKnown serves stale", []func() (*Tenant, error){okTenant, downTenant},
			func(c *ControlPlane) { c.tenants.Delete("t1") }, nil, true},
		{"error with no lastKnown errors", []func() (*Tenant, error){downTenant}, nil, errCPDown, false},
		{"invalidation drops lastKnown", []func() (*Tenant, error){okTenant, downTenant},
			func(c *ControlPlane) { c.forgetTenant("t1") }, errCPDown, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			steps := tt.steps
			c := staleTestCP(&steps)
			ctx := context.Background()
			var got *Tenant
			var err error
			for i := range tt.steps {
				if i == len(tt.steps)-1 && tt.prep != nil {
					tt.prep(c)
				}
				got, err = c.TenantByID(ctx, "t1")
			}
			if tt.wantOK {
				require.NoError(t, err)
				require.NotNil(t, got)
				assert.Equal(t, "acme", got.Slug)
				return
			}
			assert.ErrorIs(t, err, tt.wantErr)
		})
	}
}

func TestTenantByID_CachesSuccess(t *testing.T) {
	steps := []func() (*Tenant, error){okTenant}
	c := staleTestCP(&steps)
	for i := 0; i < 3; i++ {
		got, err := c.TenantByID(context.Background(), "t1")
		require.NoError(t, err)
		assert.Equal(t, "acme", got.Slug)
	}
	assert.Empty(t, steps)
}

func TestTenantByID_CanceledNotServedStale(t *testing.T) {
	steps := []func() (*Tenant, error){okTenant, func() (*Tenant, error) { return nil, context.Canceled }}
	c := staleTestCP(&steps)
	_, err := c.TenantByID(context.Background(), "t1")
	require.NoError(t, err)
	c.tenants.Delete("t1")
	_, err = c.TenantByID(context.Background(), "t1")
	assert.ErrorIs(t, err, context.Canceled)

	// Caller's own ctx cancelled while the load fails with another error.
	steps = []func() (*Tenant, error){downTenant}
	c.tenants.Delete("t1")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = c.TenantByID(ctx, "t1")
	assert.ErrorIs(t, err, errCPDown)
}

func TestTenantByID_NotFoundDropsLastKnown(t *testing.T) {
	steps := []func() (*Tenant, error){okTenant, missingTenant, downTenant}
	c := staleTestCP(&steps)
	ctx := context.Background()
	_, err := c.TenantByID(ctx, "t1")
	require.NoError(t, err)

	c.tenants.Delete("t1")
	_, err = c.TenantByID(ctx, "t1")
	assert.ErrorIs(t, err, ErrTenantNotFound)
	_, ok := c.lastKnown.Get("t1")
	assert.False(t, ok)

	_, err = c.TenantByID(ctx, "t1")
	assert.ErrorIs(t, err, errCPDown)
}

// TestTenantByID_InvalidationDuringLoadIsNotResurrected guards against an
// in-flight load re-inserting a pre-invalidation row into the stale fallback.
func TestTenantByID_InvalidationDuringLoadIsNotResurrected(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	calls := 0
	cp := &ControlPlane{
		tenants:   newTenantCache(),
		lastKnown: newLastKnownCache(),
		load: func(ctx context.Context, id string) (*Tenant, error) {
			calls++
			if calls == 1 {
				close(started)
				<-release
				return &Tenant{ID: id}, nil
			}
			return nil, errCPDown
		},
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = cp.TenantByID(context.Background(), "t1")
	}()
	<-started
	cp.forgetTenant("t1")
	close(release)
	<-done
	// The primary TTL cache may hold the in-flight result; only the stale
	// fallback is under test, so force a reload.
	cp.tenants.Delete("t1")

	_, err := cp.TenantByID(context.Background(), "t1")
	require.Error(t, err)
	assert.ErrorIs(t, err, errCPDown)
}
