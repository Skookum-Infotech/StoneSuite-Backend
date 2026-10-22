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

const expiryJobTenantID = "tenant-expiry-job"

// expiryJobFx commits fixture rows (the job runs on the pool, not a tx) and
// removes them on cleanup.
type expiryJobFx struct {
	*arFixture
	pool    *pgxpool.Pool
	send    *fakeSender
	quotes  []string
	ests    []string
	soIDs   []int
	ruleBak map[string]string
	custs   []int
	emps    []int
	users   []string
	env     JobEnv
	capSave *int
}

func newExpiryJobFx(t *testing.T) *expiryJobFx {
	t.Helper()
	pool := newTenantTestPool(t)
	ctx := context.Background()
	tx, err := pool.Begin(ctx)
	require.NoError(t, err)
	chicago, err := time.LoadLocation("America/Chicago")
	require.NoError(t, err)
	now := time.Date(2026, 10, 16, 15, 0, 0, 0, time.UTC)
	f := &expiryJobFx{
		arFixture: &arFixture{t: t, ctx: ctx, tx: tx},
		pool:      pool,
		send:      &fakeSender{},
		env: JobEnv{Tenant: &tenancy.Tenant{ID: expiryJobTenantID}, Now: now,
			Today: TenantToday(now, "America/Chicago", nil), Loc: chicago},
	}
	t.Cleanup(f.cleanup)
	return f
}

func (f *expiryJobFx) owner(email string) int {
	emp, uid := f.employeeWithUser(email, "active")
	f.emps = append(f.emps, emp)
	f.users = append(f.users, uid)
	return emp
}

func (f *expiryJobFx) quote(cust, owner int, valid int) string {
	id := f.expiryDoc(quoteSpec, expDoc{cust: cust, owner: &owner, status: "SENT", valid: dayOffset(valid)})
	f.quotes = append(f.quotes, id)
	return id
}

func (f *expiryJobFx) estimate(cust, owner int, valid int) string {
	id := f.expiryDoc(estimateSpec, expDoc{cust: cust, owner: &owner, status: "SENT", valid: dayOffset(valid)})
	f.ests = append(f.ests, id)
	return id
}

// convertCommitted records a quote -> sales order conversion on the pool (the fixture tx is already committed).
func (f *expiryJobFx) convertCommitted(quoteUUID string) {
	var soID int
	require.NoError(f.t, f.pool.QueryRow(f.ctx, `
		INSERT INTO sales_order (record_type, sales_order_status, sales_order_customer_id)
		SELECT (SELECT record_type_id FROM lkp_record_type WHERE record_type_code = 'SORD'),
		       (SELECT s.record_status_id FROM lkp_record_status s
		        JOIN lkp_record_type t ON t.record_type_id = s.record_status_record_type
		        WHERE t.record_type_code = 'SORD' AND s.record_status_code = 'DRFT'),
		       quote_customer_id FROM quote WHERE quote_uuid = $1::uuid
		RETURNING sales_order_id`, quoteUUID).Scan(&soID))
	f.soIDs = append(f.soIDs, soID)
	_, err := f.pool.Exec(f.ctx, `INSERT INTO quote_conversion (quote_id, sales_order_id)
		SELECT quote_id, $2 FROM quote WHERE quote_uuid = $1::uuid`, quoteUUID, soID)
	require.NoError(f.t, err)
}

func (f *expiryJobFx) commit(enable bool) {
	if enable {
		f.enableExpiryRule(quoteSpec, arToday.AddDate(0, 0, -30))
		f.enableExpiryRule(estimateSpec, arToday.AddDate(0, 0, -30))
	}
	require.NoError(f.t, f.tx.Commit(f.ctx))
}

func (f *expiryJobFx) cleanup() {
	ctx := context.Background()
	_ = f.tx.Rollback(ctx)
	for _, k := range []string{RuleKeyQuoteExpiry, RuleKeyEstimateExpiry} {
		_, _ = f.pool.Exec(ctx, `DELETE FROM automation_deliveries WHERE rule_id = (SELECT id FROM automation_rules WHERE rule_key = $1)`, k)
		_, _ = f.pool.Exec(ctx, `UPDATE automation_rules SET enabled = FALSE WHERE rule_key = $1`, k)
		_, _ = f.pool.Exec(ctx, `UPDATE automation_state SET enabled_at = NULL, last_planned_at = NULL WHERE rule_id = (SELECT id FROM automation_rules WHERE rule_key = $1)`, k)
	}
	_, _ = f.pool.Exec(ctx, `DELETE FROM quote_conversion WHERE quote_id IN (SELECT quote_id FROM quote WHERE quote_uuid = ANY($1::uuid[]))`, f.quotes)
	_, _ = f.pool.Exec(ctx, `DELETE FROM sales_order WHERE sales_order_id = ANY($1::int[])`, f.soIDs)
	_, _ = f.pool.Exec(ctx, `DELETE FROM quote WHERE quote_uuid = ANY($1::uuid[])`, f.quotes)
	_, _ = f.pool.Exec(ctx, `DELETE FROM estimate WHERE estimate_uuid = ANY($1::uuid[])`, f.ests)
	for k, v := range f.ruleBak {
		_, _ = f.pool.Exec(ctx, `UPDATE automation_rules SET rule = $2::jsonb WHERE rule_key = $1`, k, v)
	}
	_, _ = f.pool.Exec(ctx, `DELETE FROM customer WHERE customer_id = ANY($1::int[])`, f.custs)
	_, _ = f.pool.Exec(ctx, `DELETE FROM employee WHERE employee_id = ANY($1::int[])`, f.emps)
	_, _ = f.pool.Exec(ctx, `DELETE FROM users WHERE id = ANY($1::uuid[])`, f.users)
	if f.capSave != nil {
		_, _ = f.pool.Exec(ctx, `UPDATE automation_settings SET daily_external_cap = $1 WHERE id`, *f.capSave)
	}
}

func (f *expiryJobFx) job() *ExpiryReminders {
	return &ExpiryReminders{deps: Deps{Send: f.send, LinkURL: func(tenantID, id string) string { return "https://app.test/" + tenantID + "/" + id }}}
}

func (f *expiryJobFx) run() Result {
	res, err := f.job().Run(f.ctx, f.pool, f.env)
	require.NoError(f.t, err)
	return res
}

func (f *expiryJobFx) cust() int {
	id := f.customer(arCust{})
	f.custs = append(f.custs, id)
	return id
}

func (f *expiryJobFx) deliveries(where string) int {
	var n int
	require.NoError(f.t, f.pool.QueryRow(f.ctx, `SELECT count(*) FROM automation_deliveries d JOIN automation_rules r ON r.id = d.rule_id
		WHERE r.rule_key = ANY($1) AND `+where, []string{RuleKeyQuoteExpiry, RuleKeyEstimateExpiry}).Scan(&n))
	return n
}

func TestExpiryJobSendsOnce(t *testing.T) {
	f := newExpiryJobFx(t)
	owner := f.owner("owner-job1@x.test")
	q := f.quote(f.cust(), owner, 3)
	f.commit(true)
	var ownerIdentity string
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT identity_id::text FROM users WHERE id = ANY($1::uuid[])`, f.users).Scan(&ownerIdentity))

	res := f.run()
	assert.Equal(t, 1, res.Sent)
	require.Len(t, f.send.reqs, 1)
	req := f.send.reqs[0]
	assert.Equal(t, "automation.expiry_reminder", req.EventType)
	assert.Equal(t, "quote", req.Resource)
	assert.Equal(t, q, req.ResourceID)
	assert.Empty(t, req.ActorUserID)
	assert.Equal(t, []string{"email", "in_app"}, req.Channels)
	require.Len(t, req.Recipients, 1)
	assert.Equal(t, "owner-job1@x.test", req.Recipients[0].Email)
	assert.Equal(t, ownerIdentity, req.Recipients[0].UserID)
	require.NotNil(t, req.Email)
	var links []string
	for _, p := range req.Email.Paragraphs {
		for _, sp := range p {
			if sp.Href != "" {
				links = append(links, sp.Href)
			}
		}
	}
	assert.Equal(t, []string{"https://app.test/" + expiryJobTenantID + "/" + q}, links)

	f.run()
	assert.Len(t, f.send.reqs, 1, "second tick must not resend")
}

func TestExpiryJobOwnerDisabledBeforeSend(t *testing.T) {
	f := newExpiryJobFx(t)
	owner := f.owner("owner-job2@x.test")
	f.quote(f.cust(), owner, 3)
	f.commit(true)

	rule, ok, err := loadRule(f.ctx, f.pool, RuleKeyQuoteExpiry)
	require.NoError(t, err)
	require.True(t, ok)
	planned, _, err := planExpiry(f.ctx, f.pool, f.env, quoteSpec, rule)
	require.NoError(t, err)
	require.Equal(t, 1, planned)
	_, err = f.pool.Exec(f.ctx, `UPDATE users SET status = 'disabled' WHERE id = ANY($1::uuid[])`, f.users)
	require.NoError(t, err)

	f.run()
	assert.Empty(t, f.send.reqs)
	assert.Equal(t, 1, f.deliveries(`d.status = 'skipped' AND d.reason = 'no_email'`))
}

func TestExpiryJobDeletedBeforeSend(t *testing.T) {
	f := newExpiryJobFx(t)
	owner := f.owner("owner-job3@x.test")
	q := f.quote(f.cust(), owner, 3)
	f.commit(true)

	rule, _, err := loadRule(f.ctx, f.pool, RuleKeyQuoteExpiry)
	require.NoError(t, err)
	_, _, err = planExpiry(f.ctx, f.pool, f.env, quoteSpec, rule)
	require.NoError(t, err)
	_, err = f.pool.Exec(f.ctx, `UPDATE quote SET quote_deleted_at = now(), quote_deleted_by = 1 WHERE quote_uuid = $1::uuid`, q)
	require.NoError(t, err)

	f.run()
	assert.Empty(t, f.send.reqs)
	assert.Equal(t, 1, f.deliveries(`d.status = 'skipped'`))
}

func TestExpiryJobSixDocsOneOwnerOneSend(t *testing.T) {
	f := newExpiryJobFx(t)
	owner := f.owner("owner-job4@x.test")
	c := f.cust()
	for i := 0; i < 6; i++ {
		f.quote(c, owner, 3)
	}
	f.commit(true)

	res := f.run()
	assert.Equal(t, 1, res.Sent)
	require.Len(t, f.send.reqs, 1)
	assert.Len(t, f.send.reqs[0].Email.Details, 6)
}

func TestExpiryJobIgnoresExternalDailyCap(t *testing.T) {
	f := newExpiryJobFx(t)
	owner := f.owner("owner-job5@x.test")
	f.quote(f.cust(), owner, 3)
	f.commit(true)
	var prev int
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT daily_external_cap FROM automation_settings WHERE id`).Scan(&prev))
	f.capSave = &prev
	_, err := f.pool.Exec(f.ctx, `UPDATE automation_settings SET daily_external_cap = 0 WHERE id`)
	require.NoError(t, err)

	res := f.run()
	assert.Equal(t, 1, res.Sent)
}

func TestExpiryJobDisabledRuleWritesNothing(t *testing.T) {
	f := newExpiryJobFx(t)
	owner := f.owner("owner-job6@x.test")
	f.quote(f.cust(), owner, 3)
	f.commit(false)

	f.run()
	assert.Empty(t, f.send.reqs)
	assert.Zero(t, f.deliveries(`TRUE`))
}

func TestExpiryJobConvertedBeforeSend(t *testing.T) {
	f := newExpiryJobFx(t)
	owner := f.owner("owner-job7@x.test")
	q := f.quote(f.cust(), owner, 3)
	f.commit(true)

	rule, _, err := loadRule(f.ctx, f.pool, RuleKeyQuoteExpiry)
	require.NoError(t, err)
	planned, _, err := planExpiry(f.ctx, f.pool, f.env, quoteSpec, rule)
	require.NoError(t, err)
	require.Equal(t, 1, planned)
	f.convertCommitted(q)

	f.run()
	assert.Empty(t, f.send.reqs)
	assert.Equal(t, 1, f.deliveries(`d.status = 'skipped' AND d.reason = 'converted'`))
}

func TestExpiryJobOwnerChangedBeforeSend(t *testing.T) {
	f := newExpiryJobFx(t)
	ownerA := f.owner("owner-job8a@x.test")
	ownerB := f.owner("owner-job8b@x.test")
	c := f.cust()
	keep := f.quote(c, ownerA, 3)
	moved := f.quote(c, ownerA, 3)
	f.commit(true)

	rule, _, err := loadRule(f.ctx, f.pool, RuleKeyQuoteExpiry)
	require.NoError(t, err)
	planned, _, err := planExpiry(f.ctx, f.pool, f.env, quoteSpec, rule)
	require.NoError(t, err)
	require.Equal(t, 2, planned)
	_, err = f.pool.Exec(f.ctx, `UPDATE quote SET quote_owner_id = $2 WHERE quote_uuid = $1::uuid`, moved, ownerB)
	require.NoError(t, err)

	f.run()
	require.Len(t, f.send.reqs, 1)
	req := f.send.reqs[0]
	assert.Equal(t, "owner-job8a@x.test", req.Recipients[0].Email)
	assert.Equal(t, keep, req.ResourceID)
	require.NotNil(t, req.Email)
	assert.Len(t, req.Email.Details, 1)
	assert.Equal(t, 1, f.deliveries(`d.status = 'skipped' AND d.reason = 'owner_changed'`))
}

func TestExpiryJobFirstSpecFailureDoesNotStopSecond(t *testing.T) {
	f := newExpiryJobFx(t)
	owner := f.owner("owner-job9@x.test")
	c := f.cust()
	f.quote(c, owner, 3)
	est := f.estimate(c, owner, 3)
	f.commit(true)

	var orig string
	require.NoError(t, f.pool.QueryRow(f.ctx, `SELECT rule::text FROM automation_rules WHERE rule_key = $1`, RuleKeyQuoteExpiry).Scan(&orig))
	f.ruleBak = map[string]string{RuleKeyQuoteExpiry: orig}
	_, err := f.pool.Exec(f.ctx, `UPDATE automation_rules SET rule = '"broken"'::jsonb WHERE rule_key = $1`, RuleKeyQuoteExpiry)
	require.NoError(t, err)

	res, err := f.job().Run(f.ctx, f.pool, f.env)
	require.Error(t, err)
	assert.Equal(t, 1, res.Sent)
	require.Len(t, f.send.reqs, 1)
	assert.Equal(t, "estimate", f.send.reqs[0].Resource)
	assert.Equal(t, est, f.send.reqs[0].ResourceID)
}
