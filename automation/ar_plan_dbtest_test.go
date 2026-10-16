//go:build dbtest

package automation

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// planEnv is 10:00 UTC on arToday so the send window is open.
func planEnv() JobEnv {
	return JobEnv{Now: arToday.Add(10 * time.Hour), Today: arToday, Loc: time.UTC}
}

// enableRule turns the seeded ar_dunning rule on with the given enabled_at.
func (f *arFixture) enableRule(enabledAt time.Time) ruleRow {
	_, err := f.tx.Exec(f.ctx, `UPDATE automation_rules SET enabled = TRUE WHERE rule_key = $1`, RuleKeyARDunning)
	require.NoError(f.t, err)
	_, err = f.tx.Exec(f.ctx, `UPDATE automation_state SET enabled_at = $1 WHERE rule_id = (SELECT id FROM automation_rules WHERE rule_key = $2)`, enabledAt, RuleKeyARDunning)
	require.NoError(f.t, err)
	rule, ok, err := loadRule(f.ctx, f.tx, RuleKeyARDunning)
	require.NoError(f.t, err)
	require.True(f.t, ok)
	return rule
}

func (f *arFixture) count(where string, args ...any) int {
	var n int
	require.NoError(f.t, f.tx.QueryRow(f.ctx, `SELECT count(*) FROM automation_deliveries WHERE `+where, args...).Scan(&n))
	return n
}

func TestARPlanIdempotent(t *testing.T) {
	f := newARFixture(t)
	rule := f.enableRule(arToday.AddDate(0, 0, -30))
	cust := f.customer(arCust{accountsEmail: "Pay@Acme.com"})
	f.invoice(arInv{cust: cust, status: "SENT", due: dayOffset(0), balance: "10", sent: true})
	j := &ARDunning{}

	p1, s1, err := j.planTx(f.ctx, f.tx, planEnv(), rule)
	require.NoError(t, err)
	// stages -3 and 0 are due today: -3 superseded, 0 planned.
	assert.Equal(t, 1, p1)
	assert.Equal(t, 1, s1)
	before := f.count(`rule_id = $1::uuid`, rule.ID)

	p2, s2, err := j.planTx(f.ctx, f.tx, planEnv(), rule)
	require.NoError(t, err)
	assert.Zero(t, p2)
	assert.Zero(t, s2)
	assert.Equal(t, before, f.count(`rule_id = $1::uuid`, rule.ID))

	var email, digest, kind, status string
	require.NoError(t, f.tx.QueryRow(f.ctx, `SELECT recipient_email, digest_key, recipient_kind, status FROM automation_deliveries
		WHERE rule_id = $1::uuid AND status = 'planned'`, rule.ID).Scan(&email, &digest, &kind, &status))
	assert.Equal(t, "pay@acme.com", email)
	assert.Equal(t, DigestKey(rule.ID, "pay@acme.com", arToday), digest)
	assert.Equal(t, "external", kind)
}

func TestARPlanDueDateMove(t *testing.T) {
	f := newARFixture(t)
	rule := f.enableRule(arToday.AddDate(0, 0, -30))
	cust := f.customer(arCust{accountsEmail: "a@x.com"})
	inv := f.invoice(arInv{cust: cust, status: "SENT", due: dayOffset(0), balance: "10", sent: true})
	j := &ARDunning{}
	_, _, err := j.planTx(f.ctx, f.tx, planEnv(), rule)
	require.NoError(t, err)
	require.Equal(t, 1, f.count(`rule_id = $1::uuid AND status = 'planned'`, rule.ID))

	// Due date moves to 3 days ago (stage 0 is then 3 days old -> stale; stage 7d not yet due).
	_, err = f.tx.Exec(f.ctx, `UPDATE invoice SET invoice_due_date = $2 WHERE invoice_uuid = $1::uuid`, inv, arToday.AddDate(0, 0, -1))
	require.NoError(t, err)
	p, _, err := j.planTx(f.ctx, f.tx, planEnv(), rule)
	require.NoError(t, err)
	assert.Equal(t, 1, p, "new anchor plans its own stage")
	assert.Equal(t, 1, f.count(`rule_id = $1::uuid AND reason = 'anchor_changed' AND status = 'skipped'`, rule.ID))
	assert.Equal(t, 1, f.count(`rule_id = $1::uuid AND status = 'planned' AND anchor_date = $2`, rule.ID, arToday.AddDate(0, 0, -1)))
}

func TestARPlanDisabledRule(t *testing.T) {
	pool := newTenantTestPool(t)
	ctx := t.Context()
	rule, enabled, err := loadRule(ctx, pool, RuleKeyARDunning)
	require.NoError(t, err)
	assert.False(t, enabled)
	assert.Empty(t, rule.ID)
	_, enabled, err = loadRule(ctx, pool, "no.such.rule")
	require.NoError(t, err)
	assert.False(t, enabled)
}

func TestARPlanBacklogPreEnable(t *testing.T) {
	f := newARFixture(t)
	rule := f.enableRule(arToday.Add(9 * time.Hour)) // enabled today
	for i := 0; i < 200; i++ {
		cust := f.customer(arCust{accountsEmail: fmt.Sprintf("c%d@x.com", i)})
		f.invoice(arInv{cust: cust, status: "ODUE", due: dayOffset(-15 - i%40), balance: "10", sent: true})
	}
	p, s, err := (&ARDunning{}).planTx(f.ctx, f.tx, planEnv(), rule)
	require.NoError(t, err)
	assert.Zero(t, p)
	assert.Positive(t, s)
	assert.Zero(t, f.count(`rule_id = $1::uuid AND status = 'planned'`, rule.ID))
	assert.Equal(t, s, f.count(`rule_id = $1::uuid AND status = 'skipped' AND reason = 'pre_enable'`, rule.ID))
}

func TestARPlanSharedDigestAndNoEmail(t *testing.T) {
	f := newARFixture(t)
	rule := f.enableRule(arToday.AddDate(0, 0, -30))
	cust := f.customer(arCust{accountsEmail: "ap@acme.com"})
	for i := 0; i < 5; i++ {
		f.invoice(arInv{cust: cust, status: "SENT", due: dayOffset(0), balance: "10", sent: true})
	}
	noMail := f.customer(arCust{})
	f.invoice(arInv{cust: noMail, status: "SENT", due: dayOffset(0), balance: "10", sent: true})

	p, _, err := (&ARDunning{}).planTx(f.ctx, f.tx, planEnv(), rule)
	require.NoError(t, err)
	assert.Equal(t, 5, p)
	var keys int
	require.NoError(t, f.tx.QueryRow(f.ctx, `SELECT count(DISTINCT digest_key) FROM automation_deliveries
		WHERE rule_id = $1::uuid AND status = 'planned'`, rule.ID).Scan(&keys))
	assert.Equal(t, 1, keys)
	assert.Equal(t, 1, f.count(`rule_id = $1::uuid AND reason = 'no_email' AND recipient_email = '' AND digest_key IS NULL`, rule.ID))

	var lp *time.Time
	require.NoError(t, f.tx.QueryRow(f.ctx, `SELECT last_planned_at FROM automation_state WHERE rule_id = $1::uuid`, rule.ID).Scan(&lp))
	require.NotNil(t, lp)
}
