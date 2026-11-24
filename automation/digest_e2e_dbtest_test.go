//go:build dbtest

package automation

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/tenancy"
)

func dgEnv(t *testing.T, hour int) JobEnv {
	t.Helper()
	chicago, err := time.LoadLocation("America/Chicago")
	require.NoError(t, err)
	now := time.Date(arToday.Year(), arToday.Month(), arToday.Day(), hour, 0, 0, 0, chicago)
	return JobEnv{Tenant: &tenancy.Tenant{ID: "tenant-dg"}, Now: now, Today: arToday, Loc: chicago}
}

func (f *arFixture) dgSeededRule() ruleRow {
	rule, enabled, err := loadRule(f.ctx, f.tx, RuleKeyDailyDigest)
	require.NoError(f.t, err)
	require.True(f.t, enabled, "the daily_digest rule must be seeded enabled")
	return rule
}

func (f *arFixture) dgClaim(ruleID string, env JobEnv) []Group {
	ds, err := claimDue(f.ctx, f.tx, ruleID, env.Now)
	require.NoError(f.t, err)
	return groupDeliveries(ds)
}

func TestDailyDigestEndToEnd(t *testing.T) {
	f := newARFixture(t)
	cust := f.customer(arCust{})
	emp, userID := f.employeeWithUser("dg-e2e@x.io", "active")
	_, otherUser := f.employeeWithUser("dg-skip@x.io", "active")
	rule := f.dgSeededRule()

	uuid := f.invoice(arInv{cust: cust, status: "SENT", due: dayOffset(-4), balance: "300"})
	_, err := f.tx.Exec(f.ctx, `UPDATE invoice SET invoice_owner_id = $2 WHERE invoice_uuid = $1::uuid`, uuid, emp)
	require.NoError(t, err)

	f.optIn(userID, func(p *DigestPrefs) { p.SendHour = 9 })
	// Never opted in: gets nothing.
	require.NoError(t, SaveDigestPrefs(f.ctx, f.tx, otherUser, DefaultDigestPrefs()))

	// Before the chosen hour, nothing is planned. After it (and inside the window), one row is.
	planned, err := planDigestsTx(f.ctx, f.tx, dgEnv(t, 8), rule)
	require.NoError(t, err)
	assert.Zero(t, planned, "08:00 is before this user's 09:00")
	env := dgEnv(t, 10)
	planned, err = planDigestsTx(f.ctx, f.tx, env, rule)
	require.NoError(t, err)
	assert.Equal(t, 1, planned)
	planned, err = planDigestsTx(f.ctx, f.tx, dgEnv(t, 14), rule)
	require.NoError(t, err)
	assert.Zero(t, planned, "a later tick the same day must not plan a second digest")
	planned, err = planDigestsTx(f.ctx, f.tx, dgEnv(t, 19), rule)
	require.NoError(t, err)
	assert.Zero(t, planned, "nothing is planned outside the send window")

	groups := f.dgClaim(rule.ID, env)
	require.Len(t, groups, 1)
	assert.Equal(t, "dg-e2e@x.io", groups[0].RecipientEmail)

	r := digestRenderer{pool: f.tx}
	req, kept, dropped, err := r.Render(f.ctx, env, groups[0])
	require.NoError(t, err)
	assert.Empty(t, dropped)
	require.Len(t, kept, 1)
	assert.Equal(t, digestTitle, req.Title)
	assert.Equal(t, "1 item needs your attention.", req.Body)
	assert.Equal(t, userID, req.ResourceID)
	require.NotNil(t, req.Email)
	require.Len(t, req.Email.Details, 1)
	assert.Equal(t, "Your overdue invoices", req.Email.Details[0].Label)
	assert.Equal(t, "1 invoice, $300.00 outstanding", req.Email.Details[0].Value)

	// The user turns the digest off before it is sent.
	f.optIn(userID, func(p *DigestPrefs) { p.Enabled = false })
	_, kept, dropped, err = r.Render(f.ctx, env, groups[0])
	require.NoError(t, err)
	assert.Empty(t, kept)
	assert.Equal(t, reasonDigestOff, dropped[groups[0].Items[0].OccurrenceKey])

	// Back on, but the invoice is paid and the user skips empty digests.
	f.optIn(userID, func(p *DigestPrefs) { p.SendHour = 9 })
	_, err = f.tx.Exec(f.ctx, `UPDATE invoice SET invoice_status = $2, invoice_balance_due = 0 WHERE invoice_uuid = $1::uuid`, uuid, f.statusID("INVC", "PAID"))
	require.NoError(t, err)
	_, kept, dropped, err = r.Render(f.ctx, env, groups[0])
	require.NoError(t, err)
	assert.Empty(t, kept)
	assert.Equal(t, reasonEmptyDigest, dropped[groups[0].Items[0].OccurrenceKey], "an empty digest is dropped when the user asked to skip them")

	// A user who wants the empty one gets it, saying there is nothing to do.
	f.optIn(userID, func(p *DigestPrefs) { p.SkipEmpty = false })
	req, kept, _, err = r.Render(f.ctx, env, groups[0])
	require.NoError(t, err)
	require.Len(t, kept, 1)
	assert.Equal(t, digestNothing, req.Body)
	assert.Empty(t, req.Email.Details)

	// Deactivated before the send: the recipient is gone.
	_, err = f.tx.Exec(f.ctx, `UPDATE users SET status = 'disabled' WHERE id = $1::uuid`, userID)
	require.NoError(t, err)
	_, _, dropped, err = r.Render(f.ctx, env, groups[0])
	require.NoError(t, err)
	assert.Equal(t, reasonRecipientChanged, dropped[groups[0].Items[0].OccurrenceKey])
}
