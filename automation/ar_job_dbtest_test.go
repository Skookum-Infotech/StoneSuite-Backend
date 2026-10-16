//go:build dbtest

package automation

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/tenancy"
)

const arJobTenantID = "tenant-ar-job"

// arJobFx commits fixture rows (the job runs on the pool, not a tx) and removes
// them, plus every delivery of the rule, on cleanup.
type arJobFx struct {
	*arFixture
	pool  *pgxpool.Pool
	send  *fakeSender
	uuids []string
	custs []int
	env   JobEnv
}

func newARJobFx(t *testing.T) *arJobFx {
	t.Helper()
	pool := newTenantTestPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	chicago, err := time.LoadLocation("America/Chicago")
	require.NoError(t, err)
	// 10:00 CDT on Oct 16; arToday (UTC midnight) is the stored DATE reference.
	now := time.Date(2026, 10, 16, 15, 0, 0, 0, time.UTC)
	fx := &arJobFx{
		arFixture: &arFixture{t: t, ctx: ctx, tx: tx},
		pool:      pool,
		send:      &fakeSender{},
		env: JobEnv{
			Tenant: &tenancy.Tenant{ID: arJobTenantID},
			Now:    now,
			Today:  TenantToday(now, "America/Chicago", nil),
			Loc:    chicago,
		},
	}
	t.Cleanup(fx.cleanup)
	return fx
}

func (f *arJobFx) cust(c arCust) int {
	id := f.customer(c)
	f.custs = append(f.custs, id)
	return id
}

func (f *arJobFx) inv(i arInv) string {
	id := f.invoice(i)
	f.uuids = append(f.uuids, id)
	return id
}

// commit publishes the fixture rows and enables the rule (enabled_at 30 days back).
func (f *arJobFx) commit(enable bool) {
	if enable {
		f.enableRule(arToday.AddDate(0, 0, -30))
	}
	require.NoError(f.t, f.tx.Commit(f.ctx))
}

func (f *arJobFx) cleanup() {
	ctx := context.Background()
	_ = f.tx.Rollback(ctx)
	_, _ = f.pool.Exec(ctx, `DELETE FROM automation_deliveries WHERE rule_id = (SELECT id FROM automation_rules WHERE rule_key = $1)`, RuleKeyARDunning)
	_, _ = f.pool.Exec(ctx, `UPDATE automation_rules SET enabled = FALSE WHERE rule_key = $1`, RuleKeyARDunning)
	_, _ = f.pool.Exec(ctx, `UPDATE automation_state SET enabled_at = NULL, last_planned_at = NULL WHERE rule_id = (SELECT id FROM automation_rules WHERE rule_key = $1)`, RuleKeyARDunning)
	_, _ = f.pool.Exec(ctx, `DELETE FROM document_sends WHERE record_id = ANY($1::uuid[])`, f.uuids)
	_, _ = f.pool.Exec(ctx, `DELETE FROM invoice WHERE invoice_uuid = ANY($1::uuid[])`, f.uuids)
	_, _ = f.pool.Exec(ctx, `DELETE FROM customer WHERE customer_id = ANY($1::int[])`, f.custs)
}

func (f *arJobFx) job() *ARDunning {
	return &ARDunning{deps: Deps{Send: f.send, LinkURL: func(tenantID, id string) string { return "https://app.test/" + tenantID + "/" + id }}}
}

func (f *arJobFx) run() Result {
	res, err := f.job().Run(f.ctx, f.pool, f.env)
	require.NoError(f.t, err)
	return res
}

func (f *arJobFx) deliveries(where string) int {
	var n int
	require.NoError(f.t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM automation_deliveries d JOIN automation_rules r ON r.id = d.rule_id
		WHERE r.rule_key = $1 AND `+where, RuleKeyARDunning).Scan(&n))
	return n
}

func TestARJobSendsOncePerDigest(t *testing.T) {
	f := newARJobFx(t)
	c := f.cust(arCust{accountsEmail: "Pay@Acme.com"})
	inv := f.inv(arInv{cust: c, status: "SENT", due: dayOffset(3), balance: "125.50", sent: true})
	f.commit(true)

	res := f.run()
	assert.Equal(t, 1, res.Sent)
	require.Len(t, f.send.reqs, 1)
	req := f.send.reqs[0]
	assert.Equal(t, "automation.ar_reminder", req.EventType)
	assert.Equal(t, arJobTenantID, req.TenantID)
	assert.Equal(t, "invoice", req.Resource)
	assert.Equal(t, inv, req.ResourceID)
	assert.Empty(t, req.ActorUserID)
	require.Len(t, req.Recipients, 1)
	assert.Equal(t, "pay@acme.com", req.Recipients[0].Email)
	require.NotNil(t, req.Email)
	require.Len(t, req.Email.Details, 1)
	assert.Contains(t, req.Email.Details[0].Value, "$125.50")
	assert.Contains(t, req.Email.Details[0].Value, "due in 3 days")

	f.run()
	assert.Len(t, f.send.reqs, 1, "second tick must not resend")
	assert.Equal(t, 1, f.deliveries(`d.status = 'sent'`))
}

func TestARJobPaidBeforeSendSkipped(t *testing.T) {
	f := newARJobFx(t)
	c := f.cust(arCust{accountsEmail: "p@acme.com"})
	inv := f.inv(arInv{cust: c, status: "SENT", due: dayOffset(3), balance: "10", sent: true})
	f.commit(true)

	rule, ok, err := loadRule(f.ctx, f.pool, RuleKeyARDunning)
	require.NoError(t, err)
	require.True(t, ok)
	planned, _, err := f.job().plan(f.ctx, f.pool, f.env, rule)
	require.NoError(t, err)
	require.Equal(t, 1, planned)

	var paid int
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT s.record_status_id FROM lkp_record_status s
		JOIN lkp_record_type t ON t.record_type_id = s.record_status_record_type
		WHERE t.record_type_code = 'INVC' AND s.record_status_code = 'PAID'`).Scan(&paid))
	_, err = f.pool.Exec(f.ctx, `UPDATE invoice SET invoice_status = $2, invoice_balance_due = 0 WHERE invoice_uuid = $1::uuid`, inv, paid)
	require.NoError(t, err)

	f.run()
	assert.Empty(t, f.send.reqs)
	assert.Equal(t, 1, f.deliveries(`d.status = 'skipped' AND d.reason = 'paid'`))
}

func TestARJobFiveInvoicesOneEmail(t *testing.T) {
	f := newARJobFx(t)
	c := f.cust(arCust{accountsEmail: "ap@acme.com"})
	for _, off := range []int{3, 2, 1, 0, -1} {
		f.inv(arInv{cust: c, status: "SENT", due: dayOffset(off), balance: "10", sent: true})
	}
	f.commit(true)

	f.run()
	require.Len(t, f.send.reqs, 1)
	assert.Len(t, f.send.reqs[0].Email.Details, 5)
}

func TestARJobDoNotContactNeverSends(t *testing.T) {
	f := newARJobFx(t)
	c := f.cust(arCust{accountsEmail: "dnc@acme.com", dnc: true})
	f.inv(arInv{cust: c, status: "SENT", due: dayOffset(3), balance: "10", sent: true})
	f.commit(true)

	f.run()
	assert.Empty(t, f.send.reqs)
}

func TestARJobDisabledWritesNothing(t *testing.T) {
	f := newARJobFx(t)
	c := f.cust(arCust{accountsEmail: "off@acme.com"})
	f.inv(arInv{cust: c, status: "SENT", due: dayOffset(3), balance: "10", sent: true})
	f.commit(false)

	res := f.run()
	assert.Equal(t, Result{}, res)
	assert.Empty(t, f.send.reqs)
	assert.Zero(t, f.deliveries(`TRUE`))
}

type fixedPools struct{ pool *pgxpool.Pool }

func (p fixedPools) PoolFor(context.Context, *tenancy.Tenant) (*pgxpool.Pool, error) {
	return p.pool, nil
}

// TestARJobRunnerEnvFlow drives the job through Runner.Tick so JobEnv (tenant,
// tenant-local today, location) is built by the runner, not the test.
func TestARJobRunnerEnvFlow(t *testing.T) {
	f := newARJobFx(t)
	c := f.cust(arCust{accountsEmail: "flow@acme.com"})
	f.inv(arInv{cust: c, status: "SENT", due: dayOffset(3), balance: "10", sent: true})
	f.commit(true)

	chicago := f.env.Loc
	tenants := fakeLister{tenants: []tenancy.Tenant{servable(arJobTenantID)}}
	r := NewRunner(tenants, fixedPools{f.pool}, chicago, f.job())
	sum, err := r.Tick(f.ctx, f.env.Now)
	require.NoError(t, err)
	assert.Equal(t, 1, sum.Sent)
	assert.Zero(t, sum.Failed)
	require.Len(t, f.send.reqs, 1)
	assert.Equal(t, arJobTenantID, f.send.reqs[0].TenantID)
}
