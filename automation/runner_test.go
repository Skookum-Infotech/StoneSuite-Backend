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
			r := NewRunner(tc.lister, pools, time.UTC, DefaultJobs(Deps{})...)
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

func TestNewJobEnv(t *testing.T) {
	chicago, err := time.LoadLocation("America/Chicago")
	require.NoError(t, err)
	tn := servable("acme")
	now := time.Date(2026, 10, 16, 3, 0, 0, 0, time.UTC) // still Oct 15 in Chicago

	tests := []struct {
		name     string
		tz       string
		fallback *time.Location
		wantLoc  string
		wantDay  int
	}{
		{name: "profile timezone wins", tz: "America/Chicago", fallback: time.UTC, wantLoc: chicago.String(), wantDay: 15},
		{name: "empty uses fallback", tz: "", fallback: chicago, wantLoc: chicago.String(), wantDay: 15},
		{name: "invalid uses fallback", tz: "Nope/Zone", fallback: time.UTC, wantLoc: "UTC", wantDay: 16},
		{name: "nil fallback is UTC", tz: "", fallback: nil, wantLoc: "UTC", wantDay: 16},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			env := newJobEnv(&tn, now, tc.tz, tc.fallback)
			assert.Equal(t, "acme", env.Tenant.Slug)
			assert.Equal(t, now, env.Now)
			assert.Equal(t, tc.wantLoc, env.Loc.String())
			assert.Equal(t, TenantToday(now, tc.tz, tc.fallback), env.Today)
			assert.Equal(t, tc.wantDay, env.Today.Day())
		})
	}
}

func TestSummaryAdd(t *testing.T) {
	var s Summary
	s.add(Result{Transitioned: 1, Skipped: 2, Sent: 3, Failed: 4})
	s.add(Result{Sent: 2, Failed: 1})
	assert.Equal(t, Summary{Transitioned: 1, Skipped: 2, Sent: 5, FailedSends: 5}, s)
}
