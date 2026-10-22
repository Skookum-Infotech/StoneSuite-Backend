//go:build dbtest

package automation

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// enableApprovalRule turns the seeded approval_stuck rule on with the given enabled_at.
func (f *arFixture) enableApprovalRule(enabledAt time.Time) ruleRow {
	_, err := f.tx.Exec(f.ctx, `UPDATE automation_rules SET enabled = TRUE WHERE rule_key = $1`, RuleKeyApprovalStuck)
	require.NoError(f.t, err)
	_, err = f.tx.Exec(f.ctx, `UPDATE automation_state SET enabled_at = $1 WHERE rule_id = (SELECT id FROM automation_rules WHERE rule_key = $2)`, enabledAt, RuleKeyApprovalStuck)
	require.NoError(f.t, err)
	rule, ok, err := loadRule(f.ctx, f.tx, RuleKeyApprovalStuck)
	require.NoError(f.t, err)
	require.True(f.t, ok)
	return rule
}

func TestPlanApprovalsIdempotent(t *testing.T) {
	f := newARFixture(t)
	rule := f.enableApprovalRule(arToday.AddDate(0, 0, -30))
	a, aUser := f.employeeWithUser("a@x.test", "active")
	f.approver(a)
	uuid, _ := f.po(apprPO{owner: &a, entered: atDays(3, 12)})

	p1, s1, err := planApprovalsTx(f.ctx, f.tx, planEnv(), rule)
	require.NoError(t, err)
	assert.Equal(t, 1, p1)
	assert.Zero(t, s1)

	var email, digest, kind, uid, recordID string
	require.NoError(t, f.tx.QueryRow(f.ctx, `SELECT recipient_email, digest_key, recipient_kind, recipient_user_id::text, record_id
		FROM automation_deliveries WHERE rule_id = $1::uuid AND status = 'planned'`, rule.ID).
		Scan(&email, &digest, &kind, &uid, &recordID))
	assert.Equal(t, "a@x.test", email)
	assert.Equal(t, DigestKey(rule.ID, "a@x.test", arToday), digest)
	assert.Equal(t, "internal", kind)
	assert.Equal(t, aUser, uid)
	assert.Equal(t, uuid, recordID)

	p2, s2, err := planApprovalsTx(f.ctx, f.tx, planEnv(), rule)
	require.NoError(t, err)
	assert.Zero(t, p2)
	assert.Zero(t, s2)
	assert.Equal(t, 1, f.count(`rule_id = $1::uuid`, rule.ID))
}

func TestPlanApprovalsRestartOnResubmit(t *testing.T) {
	f := newARFixture(t)
	rule := f.enableApprovalRule(arToday.AddDate(0, 0, -30))
	a, _ := f.employeeWithUser("a@x.test", "active")
	f.approver(a)
	uuid, id := f.po(apprPO{owner: &a, entered: atDays(3, 12)})

	_, _, err := planApprovalsTx(f.ctx, f.tx, planEnv(), rule)
	require.NoError(t, err)
	require.Equal(t, 1, f.count(`rule_id = $1::uuid AND record_id = $2 AND status = 'planned'`, rule.ID, uuid))

	// Bounced to draft and resubmitted 2 days ago: a later entry row restarts the clock.
	f.enter(id, "PAPV", atDays(2, 12))
	p, _, err := planApprovalsTx(f.ctx, f.tx, planEnv(), rule)
	require.NoError(t, err)
	assert.Equal(t, 1, p)

	assert.Equal(t, 1, f.count(`rule_id = $1::uuid AND record_id = $2 AND status = 'skipped' AND reason = 'anchor_changed'`, rule.ID, uuid))
	assert.Equal(t, 1, f.count(`rule_id = $1::uuid AND record_id = $2 AND status = 'planned' AND anchor_date = $3`, rule.ID, uuid, arToday.AddDate(0, 0, -2)))
}

func TestPlanApprovalsNoRecipientSkipped(t *testing.T) {
	f := newARFixture(t)
	rule := f.enableApprovalRule(arToday.AddDate(0, 0, -30))
	a, _ := f.employeeWithUser("a@x.test", "active")
	f.approver(a)
	// Approver user disabled, owner is the same employee (disabled), no super admin: nobody to remind.
	_, err := f.tx.Exec(f.ctx, `UPDATE users SET status = 'disabled'`)
	require.NoError(t, err)
	uuid, _ := f.po(apprPO{owner: &a, entered: atDays(3, 12)})

	p, s, err := planApprovalsTx(f.ctx, f.tx, planEnv(), rule)
	require.NoError(t, err)
	assert.Zero(t, p)
	assert.Equal(t, 1, s)
	assert.Equal(t, 1, f.count(`rule_id = $1::uuid AND record_id = $2 AND reason = 'no_email'`, rule.ID, uuid))
}
