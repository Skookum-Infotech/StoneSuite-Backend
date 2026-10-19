//go:build dbtest

package automation

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestPlanApprovalsMultiGateSupersede covers the installation workflow's two gates:
// a record leaving TMPL for QCPD supersedes its TMPL rows and plans fresh QCPD rows.
func TestPlanApprovalsMultiGateSupersede(t *testing.T) {
	f := newARFixture(t)
	rule := f.enableApprovalRule(arToday.AddDate(0, 0, -30))
	a, _ := f.employeeWithUser("a@x.test", "active")
	for _, code := range []string{"TMPL", "QCPD"} {
		_, err := f.tx.Exec(f.ctx, `INSERT INTO fabrication_job_approver (record_type_id, record_status_id, approver_employee_id)
			VALUES ($1, $2, $3)`, f.typeID("FJOB"), f.statusID("FJOB", code), a)
		require.NoError(t, err)
	}
	cust := f.customer(arCust{})
	var soID, jobID int
	var uuid string
	require.NoError(t, f.tx.QueryRow(f.ctx, `INSERT INTO sales_order (record_type, sales_order_status, sales_order_customer_id)
		VALUES ($1, (SELECT MIN(record_status_id) FROM lkp_record_status WHERE record_status_record_type = $1), $2)
		RETURNING sales_order_id`, f.typeID("SORD"), cust).Scan(&soID))
	require.NoError(t, f.tx.QueryRow(f.ctx, `INSERT INTO fabrication_job (record_type, fabrication_job_status, sales_order_id,
			fabrication_job_customer_id, job_approval_status, job_owner_id, fabrication_job_number)
		VALUES ($1, $2, $3, $4, 'pending', $5, 'FJOB-T3') RETURNING fabrication_job_uuid::text, fabrication_job_id`,
		f.typeID("FJOB"), f.statusID("FJOB", "TMPL"), soID, cust, a).Scan(&uuid, &jobID))
	enter := func(code string, days int) {
		_, err := f.tx.Exec(f.ctx, `INSERT INTO fabrication_job_history (fabrication_job_id, to_status_id, at) VALUES ($1, $2, $3)`,
			jobID, f.statusID("FJOB", code), atDays(days, 12))
		require.NoError(t, err)
	}
	enter("TMPL", 5)

	_, _, err := planApprovalsTx(f.ctx, f.tx, planEnv(), rule)
	require.NoError(t, err)
	require.Equal(t, 1, f.count(`rule_id = $1::uuid AND record_id = $2 AND status = 'planned'`, rule.ID, uuid))

	_, err = f.tx.Exec(f.ctx, `UPDATE fabrication_job SET fabrication_job_status = $1 WHERE fabrication_job_id = $2`,
		f.statusID("FJOB", "QCPD"), jobID)
	require.NoError(t, err)
	enter("QCPD", 3)

	_, _, err = planApprovalsTx(f.ctx, f.tx, planEnv(), rule)
	require.NoError(t, err)

	assert.Equal(t, 0, f.count(`rule_id = $1::uuid AND record_id = $2 AND occurrence_key LIKE '%~TMPL~%' AND status = 'planned'`, rule.ID, uuid))
	assert.Equal(t, 1, f.count(`rule_id = $1::uuid AND record_id = $2 AND occurrence_key LIKE '%~TMPL~%' AND status = 'skipped' AND reason = 'anchor_changed'`, rule.ID, uuid))
	assert.Equal(t, 1, f.count(`rule_id = $1::uuid AND record_id = $2 AND occurrence_key LIKE '%~QCPD~%' AND status = 'planned'`, rule.ID, uuid))
}
