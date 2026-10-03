//go:build dbtest

package fabrication

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestCustomerApprovalRejectsOtherCustomer(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	job, err := Create(ctx, pool, CreateJobInput{SalesOrderUUID: seedSalesOrder(t, pool)}, 1)
	require.NoError(t, err)
	identity := testActionUUID()
	otherOrder := seedSalesOrder(t, pool)
	_, err = pool.Exec(ctx, `INSERT INTO customer_portal_user(identity_id,customer_id,email,full_name,status)
 SELECT $1,sales_order_customer_id,$2,'Customer','active' FROM sales_order WHERE sales_order_uuid=$3`, identity, identity+"@example.test", otherOrder)
	require.NoError(t, err)
	_, err = ApproveTemplateAsCustomer(ctx, pool, job.ID, testActionUUID(), identity, CommandMeta{ExpectedVersion: job.Version, RequestID: testActionUUID()})
	require.ErrorIs(t, err, ErrNotFound)
}

func TestCustomerApprovalRequiresInternalAndRechecksAccess(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	job, err := Create(ctx, pool, CreateJobInput{SalesOrderUUID: seedSalesOrder(t, pool)}, 1)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE fabrication_job SET workflow_version=2,delivery_mode='installed' WHERE fabrication_job_uuid=$1`, job.ID)
	require.NoError(t, err)
	identity := testActionUUID()
	_, err = pool.Exec(ctx, `INSERT INTO customer_portal_user(identity_id,customer_id,email,full_name,status)
 SELECT $1,sales_order_customer_id,$2,'Customer','active' FROM sales_order WHERE sales_order_uuid=$3`, identity, identity+"@example.test", job.SalesOrderID)
	require.NoError(t, err)
	var revision string
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO fabrication_template_revision(fabrication_job_id,revision,sales_order_version,baseline,measured_lines,change_summary)
 SELECT fabrication_job_id,1,1,'[]','[]','{"internalRequired":true,"customerRequired":true,"changedLines":[]}' FROM fabrication_job WHERE fabrication_job_uuid=$1 RETURNING template_uuid`, job.ID).Scan(&revision))
	_, err = pool.Exec(ctx, `INSERT INTO fabrication_revision_approval(template_id,steps) SELECT template_id,'[{"employeeId":1,"approved":false}]' FROM fabrication_template_revision WHERE template_uuid=$1`, revision)
	require.NoError(t, err)
	meta := CommandMeta{ExpectedVersion: job.Version, RequestID: testActionUUID()}
	_, err = ApproveTemplateAsCustomer(ctx, pool, job.ID, revision, identity, meta)
	require.ErrorIs(t, err, ErrActionConflict)
	_, err = pool.Exec(ctx, `UPDATE fabrication_revision_approval SET internal_approved_at=NOW() WHERE template_id=(SELECT template_id FROM fabrication_template_revision WHERE template_uuid=$1)`, revision)
	require.NoError(t, err)
	receipt, err := ApproveTemplateAsCustomer(ctx, pool, job.ID, revision, identity, meta)
	require.NoError(t, err)
	replay, err := ApproveTemplateAsCustomer(ctx, pool, job.ID, revision, identity, meta)
	require.NoError(t, err)
	require.Equal(t, receipt, replay)
	_, err = pool.Exec(ctx, `UPDATE customer_portal_user SET status='revoked',revoked_at=NOW() WHERE identity_id=$1`, identity)
	require.NoError(t, err)
	_, err = ApproveTemplateAsCustomer(ctx, pool, job.ID, revision, identity, meta)
	require.ErrorIs(t, err, ErrNotFound)
}
