//go:build dbtest

package fabrication

import (
	"context"
	"crypto/rand"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestActionReplayAndRollback(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	job, err := Create(ctx, pool, CreateJobInput{SalesOrderUUID: seedSalesOrder(t, pool)}, 1)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE fabrication_job SET workflow_version=2,delivery_mode='supply_only' WHERE fabrication_job_uuid=$1`, job.ID)
	require.NoError(t, err)
	actor := ActionActor{IdentityID: testActionUUID(), EmployeeID: 1, AllScope: true}
	meta := CommandMeta{ExpectedVersion: job.Version, RequestID: testActionUUID()}
	command := actionCommand{Code: "test", SubjectID: job.ID, Payload: []byte(`{"value":1}`)}
	calls := 0
	apply := func(ctx context.Context, tx pgx.Tx, state actionState) error { calls++; return nil }
	first, err := executeAction(ctx, pool, job.ID, actor, meta, command, apply)
	require.NoError(t, err)
	second, err := executeAction(ctx, pool, job.ID, actor, meta, command, apply)
	require.NoError(t, err)
	require.Equal(t, first, second)
	require.Equal(t, 1, calls)
	command.Payload = []byte(`{"value":2}`)
	_, err = executeAction(ctx, pool, job.ID, actor, meta, command, apply)
	require.ErrorIs(t, err, ErrActionConflict)
	meta.RequestID = testActionUUID()
	_, err = executeAction(ctx, pool, job.ID, actor, meta, command, apply)
	require.ErrorIs(t, err, ErrActionConflict)
	meta.ExpectedVersion = first.Version
	_, err = executeAction(ctx, pool, job.ID, actor, meta, command, func(ctx context.Context, tx pgx.Tx, state actionState) error { return ErrApprovalRequired })
	require.ErrorIs(t, err, ErrApprovalRequired)
	var version int64
	require.NoError(t, pool.QueryRow(ctx, `SELECT fabrication_job_record_version FROM fabrication_job WHERE fabrication_job_uuid=$1`, job.ID).Scan(&version))
	require.Equal(t, first.Version, version)
}

func testActionUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func TestActionConcurrentVersionAndScope(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	job, err := Create(ctx, pool, CreateJobInput{SalesOrderUUID: seedSalesOrder(t, pool)}, 1)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE fabrication_job SET workflow_version=2,delivery_mode='installed' WHERE fabrication_job_uuid=$1`, job.ID)
	require.NoError(t, err)
	actor := ActionActor{IdentityID: testActionUUID(), EmployeeID: 1, AllScope: true}
	command := actionCommand{Code: "test", SubjectID: job.ID, Payload: []byte(`{}`)}
	apply := func(context.Context, pgx.Tx, actionState) error { return nil }
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			_, err := executeAction(ctx, pool, job.ID, actor, CommandMeta{ExpectedVersion: job.Version, RequestID: testActionUUID()}, command, apply)
			results <- err
		}()
	}
	successes := 0
	for i := 0; i < 2; i++ {
		err := <-results
		if err == nil {
			successes++
		} else {
			require.ErrorIs(t, err, ErrActionConflict)
		}
	}
	require.Equal(t, 1, successes)
	actor.AllScope = false
	_, err = executeAction(ctx, pool, job.ID, actor, CommandMeta{ExpectedVersion: job.Version + 1, RequestID: testActionUUID()}, command, apply)
	require.ErrorIs(t, err, ErrNotFound)
}
