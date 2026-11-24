//go:build dbtest

package automation

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

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
