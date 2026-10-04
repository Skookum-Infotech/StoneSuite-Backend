//go:build dbtest

package fabrication

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestRevisionRequiresCustomerAfterInternalApproval(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	job, err := Create(ctx, pool, CreateJobInput{SalesOrderUUID: seedSalesOrder(t, pool)}, 1)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE fabrication_job SET workflow_version=2,delivery_mode='installed' WHERE fabrication_job_uuid=$1`, job.ID)
	require.NoError(t, err)
	var revision string
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO fabrication_template_revision(fabrication_job_id,revision,sales_order_version,baseline,measured_lines,change_summary)
 SELECT fabrication_job_id,1,1,'[]','[]','{"internalRequired":true,"customerRequired":true,"changedLines":[]}' FROM fabrication_job WHERE fabrication_job_uuid=$1 RETURNING template_uuid`, job.ID).Scan(&revision))
	_, err = pool.Exec(ctx, `INSERT INTO fabrication_revision_approval(template_id,steps) SELECT template_id,'[{"employeeId":1,"approved":false}]' FROM fabrication_template_revision WHERE template_uuid=$1`, revision)
	require.NoError(t, err)
	actor := ActionActor{IdentityID: testActionUUID(), EmployeeID: 1, AllScope: true}
	receipt, err := decideRevision(ctx, pool, job.ID, revision, actor, RevisionDecisionInput{CommandMeta: CommandMeta{ExpectedVersion: job.Version, RequestID: testActionUUID()}, Approve: true})
	require.NoError(t, err)
	revisions, err := Templates(ctx, pool, job.ID)
	require.NoError(t, err)
	require.Equal(t, "submitted", revisions[0].State)
	_, err = recordCustomerApproval(ctx, pool, job.ID, revision, actor, CustomerApprovalInput{CommandMeta: CommandMeta{ExpectedVersion: receipt.Version, RequestID: testActionUUID()}, Approver: "Customer", Channel: "email", Evidence: "Email saved in job file", ApprovedAt: "2026-10-01T10:00:00Z"})
	require.NoError(t, err)
	revisions, err = Templates(ctx, pool, job.ID)
	require.NoError(t, err)
	require.Equal(t, "approved", revisions[0].State)
}
