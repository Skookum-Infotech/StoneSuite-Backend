//go:build dbtest

package fabrication

import (
	"context"
	"github.com/stretchr/testify/require"
	"stonesuite-backend/database"
	"testing"
)

func TestWorkflowSchemaPreservesLegacy(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	job, err := Create(ctx, pool, CreateJobInput{SalesOrderUUID: seedSalesOrder(t, pool)}, 1)
	require.NoError(t, err)
	for i := 0; i < 2; i++ {
		_, err = database.ApplyTenantSchema(ctx, pool)
		require.NoError(t, err)
	}
	var version int
	var mode *string
	require.NoError(t, pool.QueryRow(ctx, `SELECT workflow_version,delivery_mode FROM fabrication_job WHERE fabrication_job_uuid=$1`, job.ID).Scan(&version, &mode))
	require.Equal(t, 1, version)
	require.Nil(t, mode, "legacy delivery mode must not be fabricated")
	after, err := Get(ctx, pool, job.ID)
	require.NoError(t, err)
	require.Equal(t, job.StatusCode, after.StatusCode)
}
