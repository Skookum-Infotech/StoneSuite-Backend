//go:build dbtest

package automation

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// enableExpiryRule turns the seeded rule for spec on with the given enabled_at.
// Cadence is the seeded -3 day offset.
func (f *arFixture) enableExpiryRule(spec expirySpec, enabledAt time.Time) ruleRow {
	_, err := f.tx.Exec(f.ctx, `UPDATE automation_rules SET enabled = TRUE WHERE rule_key = $1`, spec.RuleKey)
	require.NoError(f.t, err)
	_, err = f.tx.Exec(f.ctx, `UPDATE automation_state SET enabled_at = $1 WHERE rule_id = (SELECT id FROM automation_rules WHERE rule_key = $2)`, enabledAt, spec.RuleKey)
	require.NoError(f.t, err)
	rule, ok, err := loadRule(f.ctx, f.tx, spec.RuleKey)
	require.NoError(f.t, err)
	require.True(f.t, ok)
	return rule
}

func TestPlanExpiry(t *testing.T) {
	f := newARFixture(t)
	rule := f.enableExpiryRule(quoteSpec, arToday.AddDate(0, 0, -30))
	cust := f.customer(arCust{})
	emp, userID := f.employeeWithUser("Owner-p@x.test", "active")
	mk := func(valid int) string {
		return f.expiryDoc(quoteSpec, expDoc{cust: cust, owner: &emp, status: "SENT", valid: dayOffset(valid)})
	}

	future := mk(6) // stage date is 3 days out: no look-ahead
	_, _, err := planExpiryTx(f.ctx, f.tx, planEnv(), quoteSpec, rule)
	require.NoError(t, err)
	assert.Zero(t, f.count(`rule_id = $1::uuid AND record_id = $2`, rule.ID, future))

	due := mk(3) // stage date is today
	p1, s1, err := planExpiryTx(f.ctx, f.tx, planEnv(), quoteSpec, rule)
	require.NoError(t, err)
	assert.Equal(t, 1, p1)
	assert.Zero(t, s1)

	var email, digest, kind, uid string
	require.NoError(t, f.tx.QueryRow(f.ctx, `SELECT recipient_email, digest_key, recipient_kind, recipient_user_id::text
		FROM automation_deliveries WHERE rule_id = $1::uuid AND record_id = $2 AND status = 'planned'`, rule.ID, due).
		Scan(&email, &digest, &kind, &uid))
	assert.Equal(t, "Owner-p@x.test", email)
	assert.Equal(t, DigestKey(rule.ID, "owner-p@x.test", arToday), digest)
	assert.Equal(t, "internal", kind)
	assert.Equal(t, userID, uid)

	p2, s2, err := planExpiryTx(f.ctx, f.tx, planEnv(), quoteSpec, rule)
	require.NoError(t, err)
	assert.Zero(t, p2)
	assert.Zero(t, s2)

	var lp *time.Time
	require.NoError(t, f.tx.QueryRow(f.ctx, `SELECT last_planned_at FROM automation_state WHERE rule_id = $1::uuid`, rule.ID).Scan(&lp))
	require.NotNil(t, lp)
}

func TestPlanExpiryAnchorMove(t *testing.T) {
	f := newARFixture(t)
	rule := f.enableExpiryRule(quoteSpec, arToday.AddDate(0, 0, -30))
	cust := f.customer(arCust{})
	emp, _ := f.employeeWithUser("owner-m@x.test", "active")
	id := f.expiryDoc(quoteSpec, expDoc{cust: cust, owner: &emp, status: "SENT", valid: dayOffset(3)})
	_, _, err := planExpiryTx(f.ctx, f.tx, planEnv(), quoteSpec, rule)
	require.NoError(t, err)
	require.Equal(t, 1, f.count(`rule_id = $1::uuid AND status = 'planned'`, rule.ID))

	// valid_until moves a day earlier: the old row is superseded and the new anchor plans its own stage.
	_, err = f.tx.Exec(f.ctx, `UPDATE quote SET quote_valid_until = $2 WHERE quote_uuid = $1::uuid`, id, dayOffset(2))
	require.NoError(t, err)
	p, _, err := planExpiryTx(f.ctx, f.tx, planEnv(), quoteSpec, rule)
	require.NoError(t, err)
	assert.Equal(t, 1, p)
	assert.Equal(t, 1, f.count(`rule_id = $1::uuid AND status = 'skipped' AND reason = $2`, rule.ID, reasonAnchorChanged))
	assert.Equal(t, 1, f.count(`rule_id = $1::uuid AND status = 'planned' AND anchor_date = $2`, rule.ID, dayOffset(2)))
}

func TestPlanExpiryPreEnableAndNoEmail(t *testing.T) {
	f := newARFixture(t)
	rule := f.enableExpiryRule(estimateSpec, arToday) // enabled today: today's stage is pre_enable
	cust := f.customer(arCust{})
	emp, _ := f.employeeWithUser("owner-n@x.test", "active")
	pre := f.expiryDoc(estimateSpec, expDoc{cust: cust, owner: &emp, status: "SENT", valid: dayOffset(3)})
	_, _, err := planExpiryTx(f.ctx, f.tx, planEnv(), estimateSpec, rule)
	require.NoError(t, err)
	assert.Equal(t, 1, f.count(`rule_id = $1::uuid AND record_id = $2 AND status = 'skipped' AND reason = 'pre_enable'`, rule.ID, pre))

	rule = f.enableExpiryRule(estimateSpec, arToday.AddDate(0, 0, -30))
	orphan := f.employeeNoUser("orphan-n@x.test")
	disabled, _ := f.employeeWithUser("off-n@x.test", "disabled")
	noUser := f.expiryDoc(estimateSpec, expDoc{cust: cust, owner: &orphan, status: "SENT", valid: dayOffset(3)})
	offUser := f.expiryDoc(estimateSpec, expDoc{cust: cust, owner: &disabled, status: "SENT", valid: dayOffset(3)})
	_, _, err = planExpiryTx(f.ctx, f.tx, planEnv(), estimateSpec, rule)
	require.NoError(t, err)
	for _, id := range []string{noUser, offUser} {
		assert.Equal(t, 1, f.count(`rule_id = $1::uuid AND record_id = $2 AND status = 'skipped' AND reason = 'no_email'`, rule.ID, id))
	}
}

func TestPlanExpirySharedDigest(t *testing.T) {
	f := newARFixture(t)
	rule := f.enableExpiryRule(quoteSpec, arToday.AddDate(0, 0, -30))
	cust := f.customer(arCust{})
	emp, _ := f.employeeWithUser("owner-d@x.test", "active")
	for i := 0; i < 6; i++ {
		f.expiryDoc(quoteSpec, expDoc{cust: cust, owner: &emp, status: "SENT", valid: dayOffset(3)})
	}
	p, _, err := planExpiryTx(f.ctx, f.tx, planEnv(), quoteSpec, rule)
	require.NoError(t, err)
	assert.Equal(t, 6, p)
	var n int
	require.NoError(t, f.tx.QueryRow(f.ctx, `SELECT count(DISTINCT digest_key) FROM automation_deliveries WHERE rule_id = $1::uuid AND status = 'planned'`, rule.ID).Scan(&n))
	assert.Equal(t, 1, n)
}
