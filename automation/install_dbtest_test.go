//go:build dbtest

package automation

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/config"
	"stonesuite-backend/tenancy"
)

// instJob inserts a fabrication job (with its sales order) for customer cust in the given job status.
func (f *arFixture) instJob(cust int, jobStatus string, deleted bool) int {
	var soID, jobID int
	require.NoError(f.t, f.tx.QueryRow(f.ctx, `
		INSERT INTO sales_order (record_type, sales_order_status, sales_order_customer_id, sales_order_number)
		VALUES ($1, $2, $3, 'SORD-' || substr(gen_random_uuid()::text, 1, 8)) RETURNING sales_order_id`,
		f.typeID("SORD"), f.statusID("SORD", "OPEN"), cust).Scan(&soID))
	require.NoError(f.t, f.tx.QueryRow(f.ctx, `
		INSERT INTO fabrication_job (fabrication_job_number, record_type, fabrication_job_status, sales_order_id, fabrication_job_customer_id,
			job_site_addr_line1, job_site_addr_city, job_site_addr_zip)
		VALUES ('FJOB-T' || ($4::int)::text, $1, $2, $3, $5, '12 Quarry Rd', 'Austin', '78701') RETURNING fabrication_job_id`,
		f.typeID("FJOB"), f.statusID("FJOB", jobStatus), soID, soID, cust).Scan(&jobID))
	if deleted {
		_, err := f.tx.Exec(f.ctx, `UPDATE fabrication_job SET fabrication_job_deleted_at = now(), fabrication_job_deleted_by = 1 WHERE fabrication_job_id = $1`, jobID)
		require.NoError(f.t, err)
	}
	return jobID
}

type instAppt struct {
	job       int
	visitType string
	status    string // scheduled | cancelled | completed
	day       *time.Time
}

// instAppointment inserts an appointment starting 14:00 UTC on day (America/Chicago) and returns its uuid.
func (f *arFixture) instAppointment(a instAppt) string {
	if a.visitType == "" {
		a.visitType = "install"
	}
	if a.status == "" {
		a.status = "scheduled"
	}
	start := time.Date(a.day.Year(), a.day.Month(), a.day.Day(), 14, 0, 0, 0, time.UTC)
	var uuid string
	require.NoError(f.t, f.tx.QueryRow(f.ctx, `
		INSERT INTO install_appointment (fabrication_job_id, visit_type, appointment_status, starts_at, ends_at,
			local_start_date, local_end_date, timezone, cancelled_at, completed_at, created_by, updated_by)
		VALUES ($1, $2, $3::text, $4, $5, $6::date, $6::date, 'America/Chicago',
			CASE WHEN $3::text = 'cancelled' THEN now() END, CASE WHEN $3::text = 'completed' THEN now() END, 1, 1)
		RETURNING install_appointment_uuid::text`,
		a.job, a.visitType, a.status, start, start.Add(2*time.Hour), *a.day).Scan(&uuid))
	return uuid
}

// customerContact sets how the customer can be reached for an installation notice.
func (f *arFixture) customerContact(cust int, email string, dnc bool) {
	_, err := f.tx.Exec(f.ctx, `UPDATE customer SET customer_contact_email = $2, customer_do_not_contact = $3 WHERE customer_id = $1`, cust, email, dnc)
	require.NoError(f.t, err)
}

func TestInstallListCandidates(t *testing.T) {
	f := newARFixture(t)
	cust := f.customer(arCust{})
	f.customerContact(cust, "Customer@Acme.COM", false)
	jobOK := f.instJob(cust, "INST", false)

	tomorrow := f.instAppointment(instAppt{job: jobOK, day: dayOffset(1)})
	today := f.instAppointment(instAppt{job: jobOK, day: dayOffset(0)})
	f.instAppointment(instAppt{job: jobOK, visitType: "template", day: dayOffset(1)})
	f.instAppointment(instAppt{job: jobOK, visitType: "service", day: dayOffset(1)})
	f.instAppointment(instAppt{job: jobOK, status: "cancelled", day: dayOffset(1)})
	f.instAppointment(instAppt{job: jobOK, status: "completed", day: dayOffset(1)})
	f.instAppointment(instAppt{job: jobOK, day: dayOffset(5)})
	f.instAppointment(instAppt{job: jobOK, day: dayOffset(-1)})
	f.instAppointment(instAppt{job: f.instJob(cust, "CANC", false), day: dayOffset(1)})
	f.instAppointment(instAppt{job: f.instJob(cust, "INST", true), day: dayOffset(1)})

	rows, err := listInstallCandidates(f.ctx, f.tx, arToday)
	require.NoError(t, err)
	var got []string
	for _, r := range rows {
		got = append(got, r.UUID)
	}
	assert.Equal(t, []string{today, tomorrow}, got,
		"only scheduled installation visits on live jobs, today through two days ahead, soonest first; no template or service visits")

	r := rows[1]
	assert.Equal(t, "customer@acme.com", r.ContactEmail, "the address is lower-cased")
	assert.Equal(t, "Acme", r.CustomerName)
	assert.Equal(t, "America/Chicago", r.Timezone)
	assert.Equal(t, "12 Quarry Rd, Austin, 78701", r.Address)
	assert.NotEmpty(t, r.SalesOrderUUID)
	assert.NotEmpty(t, r.CustomerUUID)
	assert.True(t, sameDay(r.LocalStart, *dayOffset(1)))
	assert.False(t, r.DoNotContact)
}

func TestInstallLoadAndReasons(t *testing.T) {
	f := newARFixture(t)
	cust := f.customer(arCust{})
	f.customerContact(cust, "c@acme.com", false)
	job := f.instJob(cust, "INST", false)
	planned := *dayOffset(1)
	uuid := f.instAppointment(instAppt{job: job, day: dayOffset(1)})

	row, found, err := loadInstall(f.ctx, f.tx, uuid)
	require.NoError(t, err)
	require.True(t, found)
	assert.Empty(t, installReason(row, planned, "c@acme.com"))

	_, found, err = loadInstall(f.ctx, f.tx, "123e4567-e89b-12d3-a456-426614174999")
	require.NoError(t, err)
	assert.False(t, found)

	check := func(name, sql string, args []any, want string) {
		t.Run(name, func(t *testing.T) {
			_, err := f.tx.Exec(f.ctx, sql, args...)
			require.NoError(t, err)
			row, found, err := loadInstall(f.ctx, f.tx, uuid)
			require.NoError(t, err)
			require.True(t, found)
			assert.Equal(t, want, installReason(row, planned, "c@acme.com"))
		})
	}
	check("do not contact", `UPDATE customer SET customer_do_not_contact = TRUE WHERE customer_id = $1`, []any{cust}, reasonDoNotContact)
	check("contact flag cleared again", `UPDATE customer SET customer_do_not_contact = FALSE WHERE customer_id = $1`, []any{cust}, "")
	check("email changed", `UPDATE customer SET customer_contact_email = 'other@acme.com' WHERE customer_id = $1`, []any{cust}, reasonRecipientChanged)
	check("email removed", `UPDATE customer SET customer_contact_email = '' WHERE customer_id = $1`, []any{cust}, reasonNoEmail)
	check("email restored", `UPDATE customer SET customer_contact_email = 'c@acme.com' WHERE customer_id = $1`, []any{cust}, "")
	check("rescheduled", `UPDATE install_appointment SET local_start_date = local_start_date + 3, local_end_date = local_end_date + 3 WHERE install_appointment_uuid = $1::uuid`, []any{uuid}, reasonAnchorChanged)
	check("moved back", `UPDATE install_appointment SET local_start_date = local_start_date - 3, local_end_date = local_end_date - 3 WHERE install_appointment_uuid = $1::uuid`, []any{uuid}, "")
	check("job cancelled", `UPDATE fabrication_job SET fabrication_job_status = (SELECT record_status_id FROM lkp_record_status s JOIN lkp_record_type t ON t.record_type_id = s.record_status_record_type WHERE t.record_type_code = 'FJOB' AND s.record_status_code = 'CANC') WHERE fabrication_job_id = $1`, []any{job}, reasonCancelled)
	check("job restored", `UPDATE fabrication_job SET fabrication_job_status = (SELECT record_status_id FROM lkp_record_status s JOIN lkp_record_type t ON t.record_type_id = s.record_status_record_type WHERE t.record_type_code = 'FJOB' AND s.record_status_code = 'INST') WHERE fabrication_job_id = $1`, []any{job}, "")
	check("visit cancelled", `UPDATE install_appointment SET appointment_status = 'cancelled', cancelled_at = now() WHERE install_appointment_uuid = $1::uuid`, []any{uuid}, reasonCancelled)
}

// -- end to end: plan, claim, render ---------------------------------------------------------

func instE2EEnv(t *testing.T) JobEnv {
	t.Helper()
	chicago, err := time.LoadLocation("America/Chicago")
	require.NoError(t, err)
	now := time.Date(arToday.Year(), arToday.Month(), arToday.Day(), 10, 0, 0, 0, chicago)
	return JobEnv{Tenant: &tenancy.Tenant{ID: "tenant-inst"}, Now: now, Today: arToday, Loc: chicago}
}

func (f *arFixture) instSeededRule() ruleRow {
	_, err := f.tx.Exec(f.ctx, `UPDATE automation_state SET enabled_at = $2 WHERE rule_id = (SELECT id FROM automation_rules WHERE rule_key = $1)`, RuleKeyInstallTomorrow, arToday.AddDate(0, 0, -7))
	require.NoError(f.t, err)
	rule, enabled, err := loadRule(f.ctx, f.tx, RuleKeyInstallTomorrow)
	require.NoError(f.t, err)
	require.True(f.t, enabled, "the install_tomorrow rule must be seeded enabled")
	return rule
}

func (f *arFixture) instClaim(ruleID string, env JobEnv) []Group {
	ds, err := claimDue(f.ctx, f.tx, ruleID, env.Now)
	require.NoError(f.t, err)
	return groupDeliveries(ds)
}

func setInstFrontend(t *testing.T) {
	t.Helper()
	old := config.AppConfig.FrontendURL
	config.AppConfig.FrontendURL = "https://app.example.test"
	t.Cleanup(func() { config.AppConfig.FrontendURL = old })
}

func portalFound(id string) PortalLookup {
	return func(context.Context, string, string) (PortalIdentity, bool, error) {
		return PortalIdentity{IdentityID: id, Email: "portal-login@acme.com", Name: "Pat Portal"}, true, nil
	}
}

func TestInstallReminderEndToEnd(t *testing.T) {
	setInstFrontend(t)
	f := newARFixture(t)
	env := instE2EEnv(t)
	cust := f.customer(arCust{})
	f.customerContact(cust, "Customer@Acme.com", false)
	job := f.instJob(cust, "INST", false)
	rule := f.instSeededRule()
	assert.Equal(t, []ARCadenceStage{{OffsetDays: -1}}, rule.Config.Cadence, "the seeded cadence is the day before")

	appt := f.instAppointment(instAppt{job: job, day: dayOffset(1)})

	planned, skipped, err := planInstallTx(f.ctx, f.tx, env, rule)
	require.NoError(t, err)
	assert.Equal(t, 1, planned)
	assert.Zero(t, skipped)
	again, _, err := planInstallTx(f.ctx, f.tx, env, rule)
	require.NoError(t, err)
	assert.Zero(t, again, "re-planning inserts nothing, so a customer is never reminded twice")

	var kind, email string
	require.NoError(t, f.tx.QueryRow(f.ctx, `SELECT recipient_kind, recipient_email FROM automation_deliveries WHERE rule_id = $1::uuid AND status = 'planned'`, rule.ID).Scan(&kind, &email))
	assert.Equal(t, "external", kind, "a customer-facing delivery goes through the external guards")
	assert.Equal(t, "customer@acme.com", email)

	groups := f.instClaim(rule.ID, env)
	require.Len(t, groups, 1)
	assert.Equal(t, "customer@acme.com", groups[0].RecipientEmail)
	assert.Equal(t, appt, groups[0].Items[0].RecordID)

	t.Run("a customer with a portal login gets the email and an in-app notification", func(t *testing.T) {
		r := installRenderer{pool: f.tx, deps: Deps{Portal: portalFound("identity-1")}}
		req, kept, dropped, err := r.Render(f.ctx, env, groups[0])
		require.NoError(t, err)
		assert.Empty(t, dropped)
		require.Len(t, kept, 1)
		assert.Equal(t, "Your installation is tomorrow", req.Title)
		assert.Contains(t, req.Body, "Installation scheduled for")
		assert.Equal(t, []string{arChannelMail, expiryChannelApp}, req.Channels)
		require.Len(t, req.Recipients, 1)
		assert.Equal(t, "identity-1", req.Recipients[0].UserID, "the in-app copy needs the portal user's id")
		assert.Equal(t, "customer@acme.com", req.Recipients[0].Email, "the email goes to the address the guards checked")
		assert.Equal(t, "sales_order", req.Resource)
		assert.NotEmpty(t, req.ResourceID)
		assert.Equal(t, "/sales/sales_order/"+req.ResourceID, req.Link, "it opens the customer's order in the portal")
		require.NotNil(t, req.Email)
		assert.Equal(t, "https://app.example.test/sales/sales_order/"+req.ResourceID, req.Email.ActionURL)
		assert.Equal(t, "tenant-inst", req.TenantID)
	})

	t.Run("a customer with no portal login gets the email only", func(t *testing.T) {
		for name, deps := range map[string]Deps{
			"not found": {Portal: func(context.Context, string, string) (PortalIdentity, bool, error) {
				return PortalIdentity{}, false, nil
			}},
			"lookup fails": {Portal: func(context.Context, string, string) (PortalIdentity, bool, error) {
				return PortalIdentity{}, false, errors.New("control plane down")
			}},
			"not configured": {},
		} {
			req, kept, _, err := installRenderer{pool: f.tx, deps: deps}.Render(f.ctx, env, groups[0])
			require.NoError(t, err, name)
			require.Len(t, kept, 1, name)
			assert.Equal(t, []string{arChannelMail}, req.Channels, name)
			assert.Empty(t, req.Recipients[0].UserID, name)
			assert.Empty(t, req.Link, name)
			assert.Empty(t, req.Email.ActionURL, "%s: no button without a portal page to open", name)
			assert.Equal(t, "Your installation is tomorrow", req.Title, name)
		}
	})

	r := installRenderer{pool: f.tx}
	drop := func(sql string, args []any, want string) {
		t.Helper()
		_, err := f.tx.Exec(f.ctx, sql, args...)
		require.NoError(t, err)
		_, kept, dropped, err := r.Render(f.ctx, env, groups[0])
		require.NoError(t, err)
		assert.Empty(t, kept)
		assert.Equal(t, want, dropped[groups[0].Items[0].OccurrenceKey])
	}
	restore := func(sql string, args ...any) {
		t.Helper()
		_, err := f.tx.Exec(f.ctx, sql, args...)
		require.NoError(t, err)
	}
	t.Run("stops when the installation is cancelled", func(t *testing.T) {
		drop(`UPDATE install_appointment SET appointment_status = 'cancelled', cancelled_at = now() WHERE install_appointment_uuid = $1::uuid`, []any{appt}, reasonCancelled)
		restore(`UPDATE install_appointment SET appointment_status = 'scheduled', cancelled_at = NULL WHERE install_appointment_uuid = $1::uuid`, appt)
	})
	t.Run("stops when it is rescheduled", func(t *testing.T) {
		drop(`UPDATE install_appointment SET local_start_date = local_start_date + 4, local_end_date = local_end_date + 4 WHERE install_appointment_uuid = $1::uuid`, []any{appt}, reasonAnchorChanged)
		restore(`UPDATE install_appointment SET local_start_date = local_start_date - 4, local_end_date = local_end_date - 4 WHERE install_appointment_uuid = $1::uuid`, appt)
	})
	t.Run("stops when the customer asks not to be contacted", func(t *testing.T) {
		drop(`UPDATE customer SET customer_do_not_contact = TRUE WHERE customer_id = $1`, []any{cust}, reasonDoNotContact)
		restore(`UPDATE customer SET customer_do_not_contact = FALSE WHERE customer_id = $1`, cust)
	})
	t.Run("does not send to an address the customer changed since planning", func(t *testing.T) {
		drop(`UPDATE customer SET customer_contact_email = 'new@acme.com' WHERE customer_id = $1`, []any{cust}, reasonRecipientChanged)
		restore(`UPDATE customer SET customer_contact_email = 'customer@acme.com' WHERE customer_id = $1`, cust)
	})
	t.Run("stops when the customer is deleted", func(t *testing.T) {
		drop(`UPDATE customer SET customer_deleted_at = now(), customer_deleted_by = 1 WHERE customer_id = $1`, []any{cust}, reasonCustomerGone)
		restore(`UPDATE customer SET customer_deleted_at = NULL, customer_deleted_by = NULL WHERE customer_id = $1`, cust)
	})
	_, kept, _, err := r.Render(f.ctx, env, groups[0])
	require.NoError(t, err)
	assert.Len(t, kept, 1, "once everything is restored the reminder is sendable again")
}

func TestInstallReminderOneEmailForTwoVisitsSameDay(t *testing.T) {
	f := newARFixture(t)
	env := instE2EEnv(t)
	cust := f.customer(arCust{})
	f.customerContact(cust, "c@acme.com", false)
	rule := f.instSeededRule()
	f.instAppointment(instAppt{job: f.instJob(cust, "INST", false), day: dayOffset(1)})
	f.instAppointment(instAppt{job: f.instJob(cust, "INST", false), day: dayOffset(1)})

	planned, _, err := planInstallTx(f.ctx, f.tx, env, rule)
	require.NoError(t, err)
	assert.Equal(t, 2, planned)
	groups := f.instClaim(rule.ID, env)
	require.Len(t, groups, 1, "one customer, one day, one email")
	req, kept, _, err := installRenderer{pool: f.tx}.Render(f.ctx, env, groups[0])
	require.NoError(t, err)
	assert.Len(t, kept, 2)
	assert.Equal(t, "Your installations are coming up", req.Title)
	assert.Equal(t, "2 installations are coming up.", req.Body)
}

func TestInstallReminderSkipsCustomersItCannotReach(t *testing.T) {
	f := newARFixture(t)
	env := instE2EEnv(t)
	rule := f.instSeededRule()
	noEmail := f.customer(arCust{})
	dnc := f.customer(arCust{})
	f.customerContact(dnc, "dnc@acme.com", true)
	f.instAppointment(instAppt{job: f.instJob(noEmail, "INST", false), day: dayOffset(1)})
	f.instAppointment(instAppt{job: f.instJob(dnc, "INST", false), day: dayOffset(1)})

	planned, skipped, err := planInstallTx(f.ctx, f.tx, env, rule)
	require.NoError(t, err)
	assert.Zero(t, planned, "nothing is sent to a customer with no email or who asked not to be contacted")
	assert.Equal(t, 2, skipped)
	rows, err := f.tx.Query(f.ctx, `SELECT reason FROM automation_deliveries WHERE rule_id = $1::uuid ORDER BY reason`, rule.ID)
	require.NoError(t, err)
	defer rows.Close()
	var reasons []string
	for rows.Next() {
		var s string
		require.NoError(t, rows.Scan(&s))
		reasons = append(reasons, s)
	}
	assert.Equal(t, []string{reasonDoNotContact, reasonNoEmail}, reasons, "each skip says why, in the delivery log")
	assert.Empty(t, f.instClaim(rule.ID, env))
}

func TestInstallReminderUsesTheExternalGuards(t *testing.T) {
	f := newARFixture(t)
	settings := automationSettings{WeeklyCap: 3, DailyCap: 200}
	reason, err := guardExternal(f.ctx, f.tx, settings, "c@acme.com", arToday)
	require.NoError(t, err)
	assert.Empty(t, reason)

	reason, err = guardExternal(f.ctx, f.tx, automationSettings{KillSwitch: true, WeeklyCap: 3, DailyCap: 200}, "c@acme.com", arToday)
	require.NoError(t, err)
	assert.Equal(t, reasonKillSwitch, reason, "the workspace pause switch stops customer reminders")

	_, err = f.tx.Exec(f.ctx, `INSERT INTO automation_suppressions (email, reason, source) VALUES ('c@acme.com', 'blocked', 'manual')`)
	require.NoError(t, err)
	reason, err = guardExternal(f.ctx, f.tx, settings, "c@acme.com", arToday)
	require.NoError(t, err)
	assert.Equal(t, reasonSuppressed, reason, "an address on the suppression list is skipped")
}

func TestInstallReminderIsNotRetroactiveWhenEnabled(t *testing.T) {
	f := newARFixture(t)
	env := instE2EEnv(t)
	cust := f.customer(arCust{})
	f.customerContact(cust, "c@acme.com", false)
	f.instAppointment(instAppt{job: f.instJob(cust, "INST", false), day: dayOffset(1)})
	f.instSeededRule()
	_, err := f.tx.Exec(f.ctx, `UPDATE automation_state SET enabled_at = $2 WHERE rule_id = (SELECT id FROM automation_rules WHERE rule_key = $1)`, RuleKeyInstallTomorrow, env.Now)
	require.NoError(t, err)
	rule, _, err := loadRule(f.ctx, f.tx, RuleKeyInstallTomorrow)
	require.NoError(t, err)

	planned, skipped, err := planInstallTx(f.ctx, f.tx, env, rule)
	require.NoError(t, err)
	assert.Zero(t, planned, "an installation already inside its window when the rule is switched on is not reminded about")
	assert.Equal(t, 1, skipped)
	assert.Empty(t, f.instClaim(rule.ID, env))
}
