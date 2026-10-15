package automation

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/tenancy"
)

type fakeLister struct {
	tenants []tenancy.Tenant
	err     error
}

func (f fakeLister) ListTenants(context.Context) ([]tenancy.Tenant, error) { return f.tenants, f.err }

// failingPools is called from the runner's concurrent per-tenant goroutines,
// so its counter must be atomic.
type failingPools struct{ calls atomic.Int32 }

func (f *failingPools) PoolFor(context.Context, *tenancy.Tenant) (*pgxpool.Pool, error) {
	f.calls.Add(1)
	return nil, errors.New("no pool")
}

func servable(slug string) tenancy.Tenant {
	return tenancy.Tenant{ID: slug, Slug: slug, Status: tenancy.StatusActive, MigrationStatus: tenancy.MigrationOK, DBName: "tenant_" + slug}
}

func TestRunnerTick(t *testing.T) {
	unprovisioned := servable("pending")
	unprovisioned.DBName = ""

	tests := []struct {
		name      string
		lister    fakeLister
		wantErr   bool
		wantSum   Summary
		wantPools int
	}{
		{name: "list failure is returned", lister: fakeLister{err: errors.New("cp down")}, wantErr: true},
		{name: "no tenants", lister: fakeLister{}, wantSum: Summary{}},
		{
			name:      "unservable tenants are skipped, pool errors count as failed",
			lister:    fakeLister{tenants: []tenancy.Tenant{servable("a"), unprovisioned, servable("b")}},
			wantSum:   Summary{Tenants: 2, Failed: 2},
			wantPools: 2,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			pools := &failingPools{}
			r := NewRunner(tc.lister, pools, time.UTC, DefaultJobs()...)
			sum, err := r.Tick(context.Background(), time.Now())
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.wantSum, sum)
			assert.Equal(t, int32(tc.wantPools), pools.calls.Load())
		})
	}
}
