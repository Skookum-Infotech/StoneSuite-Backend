//go:build dbtest

package automation

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/config"
	"stonesuite-backend/tenancy"
)

// approvalJobFx commits fixture rows (the job runs on the pool, not a tx) and
// removes them on cleanup.
type approvalJobFx struct {
	*arFixture
	pool  *pgxpool.Pool
	send  *fakeSender
	env   JobEnv
	emps  []int
	users []string
	pos   []string
	bills []string
	kill  bool
}

func newApprovalJobFx(t *testing.T) *approvalJobFx {
	t.Helper()
	pool := newTenantTestPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	env := planEnv()
	env.Tenant = &tenancy.Tenant{ID: "tenant-approval-job"}
	f := &approvalJobFx{arFixture: &arFixture{t: t, ctx: ctx, tx: tx}, pool: pool, send: &fakeSender{}, env: env}
	old := config.AppConfig.FrontendURL
	config.AppConfig.FrontendURL = renderFrontend
	t.Cleanup(func() { config.AppConfig.FrontendURL = old })
	t.Cleanup(f.cleanup)
	return f
}

func (f *approvalJobFx) approverUser(email string) int {
	emp, uid := f.employeeWithUser(email, "active")
	f.emps = append(f.emps, emp)
	f.users = append(f.users, uid)
	return emp
}

func (f *approvalJobFx) commit(enable bool) {
	if enable {
		f.enableApprovalRule(arToday.AddDate(0, 0, -30))
	}
	require.NoError(f.t, f.tx.Commit(f.ctx))
}

func (f *approvalJobFx) cleanup() {
	ctx := context.Background()
	_ = f.tx.Rollback(ctx)
	_, _ = f.pool.Exec(ctx, `DELETE FROM automation_deliveries WHERE rule_id = (SELECT id FROM automation_rules WHERE rule_key = $1)`, RuleKeyApprovalStuck)
	_, _ = f.pool.Exec(ctx, `UPDATE automation_rules SET enabled = FALSE WHERE rule_key = $1`, RuleKeyApprovalStuck)
	_, _ = f.pool.Exec(ctx, `UPDATE automation_state SET enabled_at = NULL, last_planned_at = NULL WHERE rule_id = (SELECT id FROM automation_rules WHERE rule_key = $1)`, RuleKeyApprovalStuck)
	if f.kill {
		_, _ = f.pool.Exec(ctx, `UPDATE automation_settings SET kill_switch = FALSE`)
	}
	for _, tbl := range []string{"purchase_order_approval", "purchase_order_history"} {
		_, _ = f.pool.Exec(ctx, `DELETE FROM `+tbl+` WHERE purchase_order_id IN (SELECT purchase_order_id FROM purchase_order WHERE purchase_order_uuid = ANY($1::uuid[]))`, f.pos)
	}
	for _, tbl := range []string{"vendor_bill_approval", "vendor_bill_history"} {
		_, _ = f.pool.Exec(ctx, `DELETE FROM `+tbl+` WHERE vendor_bill_id IN (SELECT vendor_bill_id FROM vendor_bill WHERE vendor_bill_uuid = ANY($1::uuid[]))`, f.bills)
	}
	_, _ = f.pool.Exec(ctx, `DELETE FROM purchase_order WHERE purchase_order_uuid = ANY($1::uuid[])`, f.pos)
	_, _ = f.pool.Exec(ctx, `DELETE FROM vendor_bill WHERE vendor_bill_uuid = ANY($1::uuid[])`, f.bills)
	_, _ = f.pool.Exec(ctx, `DELETE FROM purchase_order_approver WHERE approver_employee_id = ANY($1::int[])`, f.emps)
	_, _ = f.pool.Exec(ctx, `DELETE FROM vendor_bill_approver WHERE approver_employee_id = ANY($1::int[])`, f.emps)
	_, _ = f.pool.Exec(ctx, `DELETE FROM vendor WHERE vendor_legal_name = 'Acme Supply'`)
	_, _ = f.pool.Exec(ctx, `DELETE FROM employee WHERE employee_id = ANY($1::int[])`, f.emps)
	_, _ = f.pool.Exec(ctx, `DELETE FROM users WHERE id = ANY($1::uuid[])`, f.users)
}

func (f *approvalJobFx) job() *ApprovalReminders {
	return &ApprovalReminders{deps: Deps{Send: f.send}}
}

func (f *approvalJobFx) run() Result {
	res, err := f.job().Run(f.ctx, f.pool, f.env)
	require.NoError(f.t, err)
	return res
}

func (f *approvalJobFx) deliveries(where string) int {
	var n int
	require.NoError(f.t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM automation_deliveries d JOIN automation_rules r ON r.id = d.rule_id
		WHERE r.rule_key = $1 AND `+where, RuleKeyApprovalStuck).Scan(&n))
	return n
}

func (f *approvalJobFx) signCommitted(poUUID string, emp int) {
	_, err := f.pool.Exec(f.ctx, `INSERT INTO purchase_order_approval (purchase_order_id, record_status_id, approver_employee_id)
		SELECT purchase_order_id, purchase_order_status, $2 FROM purchase_order WHERE purchase_order_uuid = $1::uuid`, poUUID, emp)
	require.NoError(f.t, err)
}

func TestApprovalJobSendsToUnsignedApproverOnce(t *testing.T) {
	f := newApprovalJobFx(t)
	signed := f.approverUser("signed-job1@x.test")
	pending := f.approverUser("pending-job1@x.test")
	f.approver(signed)
	f.approver(pending)
	uuid, id := f.po(apprPO{owner: &signed, entered: atDays(3, 12)})
	f.signOff(id, signed)
	f.pos = append(f.pos, uuid)
	f.commit(true)

	res := f.run()
	assert.Equal(t, 1, res.Sent)
	require.Len(t, f.send.reqs, 1)
	req := f.send.reqs[0]
	assert.Equal(t, approvalEventType, req.EventType)
	assert.Equal(t, uuid, req.ResourceID)
	require.Len(t, req.Recipients, 1)
	assert.Equal(t, "pending-job1@x.test", req.Recipients[0].Email)

	f.run()
	assert.Len(t, f.send.reqs, 1, "second tick must not resend")
}

func TestApprovalJobDisabledRuleWritesNothing(t *testing.T) {
	f := newApprovalJobFx(t)
	a := f.approverUser("a-job2@x.test")
	f.approver(a)
	uuid, _ := f.po(apprPO{owner: &a, entered: atDays(3, 12)})
	f.pos = append(f.pos, uuid)
	f.commit(false)

	res := f.run()
	assert.Equal(t, Result{}, res)
	assert.Empty(t, f.send.reqs)
	assert.Zero(t, f.deliveries(`TRUE`))
}

func TestApprovalJobKillSwitchDefers(t *testing.T) {
	f := newApprovalJobFx(t)
	a := f.approverUser("a-job3@x.test")
	f.approver(a)
	uuid, _ := f.po(apprPO{owner: &a, entered: atDays(3, 12)})
	f.pos = append(f.pos, uuid)
	f.commit(true)
	f.kill = true
	_, err := f.pool.Exec(f.ctx, `UPDATE automation_settings SET kill_switch = TRUE`)
	require.NoError(t, err)

	f.run()
	assert.Empty(t, f.send.reqs)
	var deferred int
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM automation_deliveries d JOIN automation_rules r ON r.id = d.rule_id
		WHERE r.rule_key = $1 AND d.status = 'planned' AND d.run_at > $2`, RuleKeyApprovalStuck, f.env.Now).Scan(&deferred))
	assert.Equal(t, 1, deferred)
}

func TestApprovalJobApproverSignsBetweenPlanAndDrain(t *testing.T) {
	f := newApprovalJobFx(t)
	a := f.approverUser("a-job4@x.test")
	b := f.approverUser("b-job4@x.test")
	f.approver(a)
	f.approver(b)
	uuid, _ := f.po(apprPO{owner: &a, entered: atDays(3, 12)})
	f.pos = append(f.pos, uuid)
	f.commit(true)

	rule, _, err := loadRule(f.ctx, f.pool, RuleKeyApprovalStuck)
	require.NoError(t, err)
	planned, _, err := planApprovals(f.ctx, f.pool, f.env, rule)
	require.NoError(t, err)
	require.Equal(t, 2, planned)
	f.signCommitted(uuid, a)

	f.run()
	require.Len(t, f.send.reqs, 1)
	assert.Equal(t, "b-job4@x.test", f.send.reqs[0].Recipients[0].Email)
	assert.Equal(t, 1, f.deliveries(`d.status = 'skipped' AND d.reason = 'recipient_changed'`))
}

func TestApprovalJobRecordLeftGateBeforeDrain(t *testing.T) {
	f := newApprovalJobFx(t)
	a := f.approverUser("a-job4b@x.test")
	f.approver(a)
	uuid, _ := f.po(apprPO{owner: &a, entered: atDays(3, 12)})
	f.pos = append(f.pos, uuid)
	f.commit(true)

	rule, _, err := loadRule(f.ctx, f.pool, RuleKeyApprovalStuck)
	require.NoError(t, err)
	planned, _, err := planApprovals(f.ctx, f.pool, f.env, rule)
	require.NoError(t, err)
	require.Equal(t, 1, planned)
	f.signCommitted(uuid, a)
	_, err = f.pool.Exec(f.ctx, `UPDATE purchase_order SET purchase_order_status = (
		SELECT s.record_status_id FROM lkp_record_status s JOIN lkp_record_type t ON t.record_type_id = s.record_status_record_type
		WHERE t.record_type_code = 'PORD' AND s.record_status_code = 'DRFT') WHERE purchase_order_uuid = $1::uuid`, uuid)
	require.NoError(t, err)

	f.run()
	assert.Empty(t, f.send.reqs)
	assert.Equal(t, 1, f.deliveries(`d.status = 'skipped' AND d.reason = 'not_pending'`))
}

func TestApprovalJobOneDigestAcrossModules(t *testing.T) {
	f := newApprovalJobFx(t)
	a := f.approverUser("a-job5@x.test")
	f.approver(a)
	poUUID, _ := f.po(apprPO{owner: &a, entered: atDays(3, 12)})
	f.pos = append(f.pos, poUUID)
	f.bills = append(f.bills, f.vendorBill(a, atDays(4, 12)))
	f.commit(true)

	res := f.run()
	assert.Equal(t, 1, res.Sent)
	require.Len(t, f.send.reqs, 1)
	require.NotNil(t, f.send.reqs[0].Email)
	assert.Len(t, f.send.reqs[0].Email.Details, 2)
	assert.Equal(t, "a-job5@x.test", f.send.reqs[0].Recipients[0].Email)
}
