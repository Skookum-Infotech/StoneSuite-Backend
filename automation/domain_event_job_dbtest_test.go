//go:build dbtest

package automation

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/domainevent"
	"stonesuite-backend/provenance"
	"stonesuite-backend/tenancy"
)

const (
	domainEventJobResource = "automation_job_test"
	domainEventJobConsumer = "dbtest.automation.drain"
)

func TestDomainEventDrainRunsAsSystemActorAndPrunes(t *testing.T) {
	pool := newTenantTestPool(t)
	ctx := context.Background()
	clean := func() {
		_, _ = pool.Exec(ctx, `DELETE FROM domain_event WHERE resource = $1`, domainEventJobResource)
		_, _ = pool.Exec(ctx, `DELETE FROM domain_event_cursor WHERE consumer = $1`, domainEventJobConsumer)
	}
	clean()
	t.Cleanup(clean)

	var sawSystem, sawVia bool
	reg := domainevent.NewRegistry()
	require.NoError(t, reg.Register(domainEventJobConsumer, func(hctx context.Context, _ pgx.Tx, _ domainevent.Stored) error {
		sawSystem = IsSystemActor(hctx)
		sawVia = provenance.From(hctx) == provenance.Automation
		return nil
	}))

	for i := 0; i < 2; i++ {
		require.NoError(t, domainevent.Emit(ctx, pool, domainevent.Event{Type: "t", Resource: domainEventJobResource, RecordID: "r"}))
	}

	job := &DomainEventDrain{deps: Deps{Events: reg}, retentionDays: 1}
	res, err := job.Run(ctx, pool, JobEnv{Tenant: &tenancy.Tenant{ID: "tenant-de-job"}})
	require.NoError(t, err)
	assert.GreaterOrEqual(t, res.Transitioned, 2)
	assert.True(t, sawSystem, "handler ctx must be a system actor")
	assert.True(t, sawVia, "handler ctx must carry via=automation")

	// Age the events past retention; the cursor has passed them, so they prune.
	_, err = pool.Exec(ctx, `UPDATE domain_event SET created_at = now() - interval '5 days' WHERE resource = $1`, domainEventJobResource)
	require.NoError(t, err)
	_, err = job.Run(ctx, pool, JobEnv{Tenant: &tenancy.Tenant{ID: "tenant-de-job"}})
	require.NoError(t, err)
	var n int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM domain_event WHERE resource = $1`, domainEventJobResource).Scan(&n))
	assert.Zero(t, n)
}
