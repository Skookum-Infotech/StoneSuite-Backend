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

	vendor := f.vendor()
	bill := f.vbRowInsert(vbRow{vendor: vendor, status: "APPV", due: dayOffset(1), total: "300", paid: "0", credit: "0", owner: &emp})
	f.grantRole(userID, "all", [2]string{"vendor_bill", "read"}, [2]string{"install_appointment", "read"})
	job := f.instJob(cust, "INST", false)
	f.crew(f.instAppointment(instAppt{job: job, day: dayOffset(1)}), emp)

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
	assert.Equal(t, "1 installation, 1 vendor bill due.", req.Body)
	assert.Equal(t, userID, req.ResourceID)
	require.NotNil(t, req.Email)
	assert.Equal(t, []string{arChannelMail, expiryChannelApp}, req.Channels, "email and in-app")
	assert.Equal(t, rcptIdentity(t, f, userID), req.Recipients[0].UserID, "the in-app copy goes to the user's own feed")
	require.Len(t, req.Email.Details, 2)
	assert.Contains(t, req.Email.Details[0].Label, "Installation")
	assert.Equal(t, "$300.00, due tomorrow", req.Email.Details[1].Value)

	// The user turns the digest off before it is sent.
	f.optIn(userID, func(p *DigestPrefs) { p.Enabled = false })
	_, kept, dropped, err = r.Render(f.ctx, env, groups[0])
	require.NoError(t, err)
	assert.Empty(t, kept)
	assert.Equal(t, reasonDigestOff, dropped[groups[0].Items[0].OccurrenceKey])

	// Back on, but the bill is paid and the visit cancelled, and the user skips empty digests.
	f.optIn(userID, func(p *DigestPrefs) { p.SendHour = 9 })
	_, err = f.tx.Exec(f.ctx, `UPDATE vendor_bill SET vendor_bill_amount_paid = vendor_bill_grand_total WHERE vendor_bill_uuid = $1::uuid`, bill)
	require.NoError(t, err)
	_, err = f.tx.Exec(f.ctx, `UPDATE install_appointment SET appointment_status = 'cancelled', cancelled_at = now()`)
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

func rcptIdentity(t *testing.T, f *arFixture, userID string) string {
	t.Helper()
	var id string
	require.NoError(t, f.tx.QueryRow(f.ctx, `SELECT identity_id::text FROM users WHERE id = $1::uuid`, userID).Scan(&id))
	return id
}
