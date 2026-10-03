//go:build dbtest

package fabrication

import (
	"context"
	"github.com/stretchr/testify/require"
	"testing"
)

func TestTemplateSubmissionFreezesOrderAndRejectsStale(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	_, policyErr := pool.Exec(ctx, `INSERT INTO fabrication_approval_policy(subject_kind,employee_ids) VALUES('template',ARRAY[1]) ON CONFLICT(subject_kind) DO UPDATE SET employee_ids=EXCLUDED.employee_ids`)
	require.NoError(t, policyErr)
	job, err := Create(ctx, pool, CreateJobInput{SalesOrderUUID: seedSalesOrder(t, pool)}, 1)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE fabrication_job SET workflow_version=2,delivery_mode='supply_only' WHERE fabrication_job_uuid=$1`, job.ID)
	require.NoError(t, err)
	var material, line string
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO inventory_item(inventory_item_sku,inventory_item_name,inventory_item_unit_id) SELECT $1,'Test stone',unit_id FROM lkp_unit WHERE unit_code='SQFT' RETURNING inventory_item_uuid`, testActionUUID()).Scan(&material))
	require.NoError(t, pool.QueryRow(ctx, `INSERT INTO sales_order_item(sales_order_id,line_number,inventory_item_id,description,quantity,unit_price)
 SELECT so.sales_order_id,1,i.inventory_item_id,'counter',10,25 FROM sales_order so,inventory_item i WHERE so.sales_order_uuid=$1 AND i.inventory_item_uuid=$2 RETURNING sales_order_item_uuid`, job.SalesOrderID, material).Scan(&line))
	in := SubmitTemplateInput{CommandMeta: CommandMeta{ExpectedVersion: job.Version, RequestID: testActionUUID()}, SalesOrderVersion: 1, Lines: []TemplateLine{{SourceLineID: line, MaterialID: material, Quantity: 10, UnitPrice: 25, Scope: "counter", Pieces: []MeasuredPiece{{Name: "Island", LengthMM: 2000, WidthMM: 900, ThicknessMM: 30}}}}}
	actor := ActionActor{IdentityID: testActionUUID(), EmployeeID: 1, AllScope: true}
	result, err := submitTemplate(ctx, pool, job.ID, actor, in)
	require.NoError(t, err)
	revisions, err := Templates(ctx, pool, job.ID)
	require.NoError(t, err)
	require.Len(t, revisions, 1)
	require.False(t, revisions[0].Change.CustomerRequired)
	_, err = pool.Exec(ctx, `UPDATE sales_order SET sales_order_record_version=sales_order_record_version+1 WHERE sales_order_uuid=$1`, job.SalesOrderID)
	require.NoError(t, err)
	in.ExpectedVersion = result.Version
	in.RequestID = testActionUUID()
	_, err = submitTemplate(ctx, pool, job.ID, actor, in)
	require.ErrorIs(t, err, ErrActionConflict)
	revisions, err = Templates(ctx, pool, job.ID)
	require.NoError(t, err)
	require.Len(t, revisions, 1)
	require.Equal(t, int64(1), revisions[0].SalesOrderVersion)
}

func TestTemplateSubmitRejectsMissingPermission(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	job, err := Create(ctx, pool, CreateJobInput{SalesOrderUUID: seedSalesOrder(t, pool)}, 1)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, `UPDATE fabrication_job SET workflow_version=2,delivery_mode='supply_only' WHERE fabrication_job_uuid=$1`, job.ID)
	require.NoError(t, err)
	_, err = SubmitTemplate(ctx, pool, job.ID, ActionActor{IdentityID: testActionUUID(), EmployeeID: 1, AllScope: true}, SubmitTemplateInput{CommandMeta: CommandMeta{ExpectedVersion: job.Version, RequestID: testActionUUID()}})
	require.ErrorIs(t, err, ErrActionPermission, "caller-provided AllScope cannot bypass actual grants")
}
