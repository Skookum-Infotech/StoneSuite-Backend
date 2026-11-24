//go:build dbtest

package automation

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/tenancy"
)

func instE2EEnv(t *testing.T) JobEnv {
	t.Helper()
	chicago, err := time.LoadLocation("America/Chicago")
	require.NoError(t, err)
	now := time.Date(arToday.Year(), arToday.Month(), arToday.Day(), 10, 0, 0, 0, chicago)
	return JobEnv{Tenant: &tenancy.Tenant{ID: "tenant-inst"}, Now: now, Today: arToday, Loc: chicago}
}

func (f *arFixture) instSeededRule(key string) ruleRow {
	_, err := f.tx.Exec(f.ctx, `UPDATE automation_state SET enabled_at = $2 WHERE rule_id = (SELECT id FROM automation_rules WHERE rule_key = $1)`, key, arToday.AddDate(0, 0, -7))
	require.NoError(f.t, err)
	rule, enabled, err := loadRule(f.ctx, f.tx, key)
	require.NoError(f.t, err)
	require.True(f.t, enabled, "the %s rule must be seeded enabled", key)
	return rule
}

func (f *arFixture) instClaim(ruleID string, env JobEnv) []Group {
	ds, err := claimDue(f.ctx, f.tx, ruleID, env.Now)
	require.NoError(f.t, err)
	return groupDeliveries(ds)
}

func TestInstallReminderEndToEnd(t *testing.T) {
	f := newARFixture(t)
	env := instE2EEnv(t)
	cust := f.customer(arCust{})
	job := f.instJob(cust, "INST", false)
	crewA, _ := f.employeeWithUser("inst-a@x.io", "active")
	crewNoUser := f.employeeNoUser("inst-nouser@x.io")
	rule := f.instSeededRule(RuleKeyInstallTomorrow)
	assert.Equal(t, []ARCadenceStage{{OffsetDays: -1}}, rule.Config.Cadence, "the seeded cadence")

	appt := f.instAppointment(instAppt{job: job, status: "scheduled", day: dayOffset(1), crew: []int{crewA, crewNoUser}})

	planned, skipped, err := planInstallTx(f.ctx, f.tx, env, rule)
	require.NoError(t, err)
	assert.Equal(t, 1, planned, "the crew member with an account is reminded")
	assert.Equal(t, 1, skipped, "the crew member with no account is a skipped no_email row")
	again, _, err := planInstallTx(f.ctx, f.tx, env, rule)
	require.NoError(t, err)
	assert.Zero(t, again, "re-planning inserts nothing")

	groups := f.instClaim(rule.ID, env)
	require.Len(t, groups, 1)
	assert.Equal(t, "inst-a@x.io", groups[0].RecipientEmail)
	assert.Equal(t, appt, groups[0].Items[0].RecordID)

	r := installRenderer{pool: f.tx}
	req, kept, dropped, err := r.Render(f.ctx, env, groups[0])
	require.NoError(t, err)
	assert.Empty(t, dropped)
	require.Len(t, kept, 1)
	assert.Equal(t, installTitle, req.Title)
	assert.Equal(t, "1 site visit is coming up.", req.Body)
	assert.Equal(t, appt, req.ResourceID)
	require.NotNil(t, req.Email)
	require.Len(t, req.Email.Details, 1)
	assert.Contains(t, req.Email.Details[0].Label, "Install FJOB-T")
	assert.Contains(t, req.Email.Details[0].Value, "Acme")
	assert.Contains(t, req.Email.Details[0].Value, "tomorrow")
	assert.Contains(t, req.Email.Details[0].Value, "9:00 AM", "14:00 UTC is 9:00 AM in the visit's own America/Chicago zone")
	assert.Contains(t, req.Link, appt)

	// A crew member added afterwards gets their own reminder; the first is not repeated.
	crewB, _ := f.employeeWithUser("inst-b@x.io", "active")
	_, err = f.tx.Exec(f.ctx, `INSERT INTO install_appointment_crew (install_appointment_id, employee_id, crew_role, assigned_by)
		SELECT install_appointment_id, $2, 'member', 1 FROM install_appointment WHERE install_appointment_uuid = $1::uuid`, appt, crewB)
	require.NoError(t, err)
	added, _, err := planInstallTx(f.ctx, f.tx, env, rule)
	require.NoError(t, err)
	assert.Equal(t, 1, added, "only the newcomer is planned")

	// Removed from the crew before the send.
	_, err = f.tx.Exec(f.ctx, `DELETE FROM install_appointment_crew WHERE employee_id = $1`, crewA)
	require.NoError(t, err)
	_, kept, dropped, err = r.Render(f.ctx, env, groups[0])
	require.NoError(t, err)
	assert.Empty(t, kept)
	assert.Equal(t, reasonCrewChanged, dropped[groups[0].Items[0].OccurrenceKey])

	// Cancelled before the send.
	_, err = f.tx.Exec(f.ctx, `INSERT INTO install_appointment_crew (install_appointment_id, employee_id, crew_role, assigned_by)
		SELECT install_appointment_id, $2, 'member', 1 FROM install_appointment WHERE install_appointment_uuid = $1::uuid`, appt, crewA)
	require.NoError(t, err)
	_, err = f.tx.Exec(f.ctx, `UPDATE install_appointment SET appointment_status = 'cancelled', cancelled_at = now() WHERE install_appointment_uuid = $1::uuid`, appt)
	require.NoError(t, err)
	_, _, dropped, err = r.Render(f.ctx, env, groups[0])
	require.NoError(t, err)
	assert.Equal(t, reasonCancelled, dropped[groups[0].Items[0].OccurrenceKey])
}
