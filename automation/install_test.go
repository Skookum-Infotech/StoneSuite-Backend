package automation

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var installLoc = time.FixedZone("tenant", -5*3600)

func installEnv(nowHour int) JobEnv {
	now := time.Date(2026, 11, 23, nowHour, 0, 0, 0, installLoc)
	return JobEnv{Now: now, Today: time.Date(2026, 11, 23, 0, 0, 0, 0, installLoc), Loc: installLoc}
}

func installRule() ruleRow {
	return ruleRow{ID: "rule-1", Config: ARRuleConfig{Cadence: []ARCadenceStage{{OffsetDays: -1}}},
		EnabledOn: time.Date(2026, 11, 1, 0, 0, 0, 0, installLoc)}
}

func installRow(uuid, email string, day int) InstallRow {
	return InstallRow{UUID: uuid, SalesOrderUUID: "so-" + uuid, SalesOrderNo: "SORD-1", CustomerUUID: "cust-" + email,
		CustomerName: "Acme Stone", ContactEmail: email, Timezone: "America/Chicago",
		StartsAt:   time.Date(2026, 11, day, 14, 0, 0, 0, time.UTC),
		LocalStart: time.Date(2026, 11, day, 0, 0, 0, 0, time.UTC), Status: installStatusScheduled,
		Address: "12 Quarry Rd, Austin, TX 78701"}
}

func TestInstallInsertsFor(t *testing.T) {
	tests := []struct {
		name       string
		mutate     func(*InstallRow)
		day        int
		hour       int
		wantStatus string
		wantReason string
		wantN      int
	}{
		{"tomorrow is planned", nil, 24, 9, statusPlanned, "", 1},
		{"day after tomorrow is not due yet", nil, 25, 9, "", "", 0},
		{"a visit today gets a catch-up reminder", nil, 23, 9, statusPlanned, "", 1},
		{"customer with no email is skipped", func(r *InstallRow) { r.ContactEmail = "" }, 24, 9, statusSkipped, reasonNoEmail, 1},
		{"do not contact is skipped", func(r *InstallRow) { r.DoNotContact = true }, 24, 9, statusSkipped, reasonDoNotContact, 1},
		{"deleted customer is skipped", func(r *InstallRow) { r.CustomerDeleted = true }, 24, 9, statusSkipped, reasonCustomerGone, 1},
		{"after hours waits for the window", nil, 24, 22, statusPlanned, "", 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			row := installRow("a", "c@x.io", tc.day)
			if tc.mutate != nil {
				tc.mutate(&row)
			}
			got := installInsertsFor([]InstallRow{row}, installEnv(tc.hour), installRule())
			require.Len(t, got, tc.wantN)
			if tc.wantN == 0 {
				return
			}
			assert.Equal(t, tc.wantStatus, got[0].Status)
			if tc.wantReason != "" {
				require.NotNil(t, got[0].Reason)
				assert.Equal(t, tc.wantReason, *got[0].Reason)
				assert.Nil(t, got[0].Digest, "a skipped row is never sent, so it has no digest")
			}
			if tc.wantStatus == statusPlanned {
				require.NotNil(t, got[0].Digest)
				assert.True(t, InSendWindow(got[0].RunAt, installLoc), "run_at must be inside the send window")
				assert.Equal(t, "c@x.io", got[0].Email)
			}
		})
	}
}

func TestInstallInsertsForOneEmailPerCustomerPerDay(t *testing.T) {
	a := installRow("a", "c@x.io", 24)
	b := installRow("b", "c@x.io", 24)
	other := installRow("c", "other@x.io", 24)
	got := installInsertsFor([]InstallRow{a, b, other}, installEnv(9), installRule())
	require.Len(t, got, 3)
	assert.NotEqual(t, got[0].Key, got[1].Key, "each visit has its own occurrence key")
	assert.Equal(t, *got[0].Digest, *got[1].Digest, "one customer's two visits go in one email")
	assert.NotEqual(t, *got[0].Digest, *got[2].Digest, "another customer gets their own")

	again := installInsertsFor([]InstallRow{a}, installEnv(9), installRule())
	assert.Equal(t, got[0].Key, again[0].Key, "a re-plan must collide so nothing is inserted twice")
}

func TestInstallInsertsForIsNotRetroactive(t *testing.T) {
	rule := installRule()
	rule.EnabledOn = time.Date(2026, 11, 23, 8, 0, 0, 0, installLoc) // enabled today
	got := installInsertsFor([]InstallRow{installRow("a", "c@x.io", 24)}, installEnv(9), rule)
	require.Len(t, got, 1)
	assert.Equal(t, statusSkipped, got[0].Status)
	require.NotNil(t, got[0].Reason)
	assert.Equal(t, string(StageActionPreEnable), *got[0].Reason)
}

func TestInstallReason(t *testing.T) {
	planned := time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC)
	base := installRow("a", "c@x.io", 24)
	mut := func(f func(*InstallRow)) InstallRow { r := base; f(&r); return r }
	tests := []struct {
		name         string
		row          InstallRow
		plannedEmail string
		want         string
	}{
		{"eligible", base, "c@x.io", ""},
		{"eligible when the planned address differs only in case", base, "C@X.IO", ""},
		{"job deleted", mut(func(r *InstallRow) { r.JobDeleted = true }), "c@x.io", reasonVoided},
		{"visit cancelled", mut(func(r *InstallRow) { r.Status = installStatusCancelled }), "c@x.io", reasonCancelled},
		{"job cancelled", mut(func(r *InstallRow) { r.JobCancelled = true }), "c@x.io", reasonCancelled},
		{"visit completed", mut(func(r *InstallRow) { r.Status = installStatusCompleted }), "c@x.io", reasonCompleted},
		{"rescheduled", mut(func(r *InstallRow) { r.LocalStart = r.LocalStart.AddDate(0, 0, 3) }), "c@x.io", reasonAnchorChanged},
		{"customer deleted", mut(func(r *InstallRow) { r.CustomerDeleted = true }), "c@x.io", reasonCustomerGone},
		{"customer asked not to be contacted", mut(func(r *InstallRow) { r.DoNotContact = true }), "c@x.io", reasonDoNotContact},
		{"email removed", mut(func(r *InstallRow) { r.ContactEmail = "" }), "c@x.io", reasonNoEmail},
		{"email changed since planning", mut(func(r *InstallRow) { r.ContactEmail = "new@x.io" }), "c@x.io", reasonRecipientChanged},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { assert.Equal(t, tc.want, installReason(tc.row, planned, tc.plannedEmail)) })
	}
}

func TestFormatInstallAddress(t *testing.T) {
	assert.Equal(t, "12 Quarry Rd, Unit 4, Austin, TX 78701", formatInstallAddress("12 Quarry Rd", "Unit 4", "Austin", "TX", "78701"))
	assert.Equal(t, "12 Quarry Rd, Austin", formatInstallAddress("12 Quarry Rd", "", "Austin", "", ""))
	assert.Equal(t, "Austin, TX", formatInstallAddress("", "", "Austin", "TX", ""))
	assert.Empty(t, formatInstallAddress("", "  ", "", "", ""))
}

func TestInstallWhenAndTiming(t *testing.T) {
	r := installRow("a", "c@x.io", 24)
	assert.Equal(t, "Tue, Nov 24, 8:00 AM", installWhen(r, installLoc), "14:00 UTC is 8:00 AM in the visit's own Chicago zone")
	r.AllDay = true
	assert.Equal(t, "Tue, Nov 24, all day", installWhen(r, installLoc))
	r.AllDay, r.Timezone = false, "Not/AZone"
	assert.Equal(t, "Tue, Nov 24, 9:00 AM", installWhen(r, time.FixedZone("f", -5*3600)), "an unknown zone falls back")

	assert.Equal(t, "today", installTiming(0))
	assert.Equal(t, "tomorrow", installTiming(1))
	assert.Equal(t, "in 2 days", installTiming(2))
	assert.Empty(t, installTiming(-1), "a past visit gets no relative wording")
}

func TestInstallHeadingAndBody(t *testing.T) {
	today := time.Date(2026, 11, 23, 0, 0, 0, 0, installLoc)
	one := []InstallRow{installRow("a", "c@x.io", 24)}
	assert.Equal(t, "Your installation is tomorrow", installHeading(one, today))
	assert.Equal(t, "Your installation is today", installHeading([]InstallRow{installRow("a", "c@x.io", 23)}, today))
	assert.Equal(t, "Your installation is coming up", installHeading([]InstallRow{installRow("a", "c@x.io", 25)}, today))
	two := []InstallRow{installRow("a", "c@x.io", 24), installRow("b", "c@x.io", 24)}
	assert.Equal(t, "Your installations are coming up", installHeading(two, today))

	assert.Equal(t, "Installation scheduled for Tue, Nov 24, 8:00 AM.", installBody(one, installLoc))
	assert.Equal(t, "2 installations are coming up.", installBody(two, installLoc))
}

func TestRenderInstallEmailSingle(t *testing.T) {
	today := time.Date(2026, 11, 23, 0, 0, 0, 0, installLoc)
	e := renderInstallEmail("Acme Stone", []InstallRow{installRow("a", "c@x.io", 24)}, today, installLoc, "")
	assert.Equal(t, "Your installation is tomorrow", e.Heading)
	require.Len(t, e.Details, 2)
	assert.Equal(t, "Installation for order SORD-1", e.Details[0].Label)
	assert.Equal(t, "Tue, Nov 24, 8:00 AM (tomorrow)", e.Details[0].Value)
	assert.Equal(t, "Address", e.Details[1].Label)
	assert.Equal(t, "12 Quarry Rd, Austin, TX 78701", e.Details[1].Value)
	assert.Equal(t, "you have an installation scheduled with Acme Stone.", e.Reason)
	assert.Empty(t, e.ActionURL, "no portal link, no button")
	assert.Empty(t, e.ActionLabel)
}

func TestRenderInstallEmailNeverExposesCrewOrInternalData(t *testing.T) {
	e := renderInstallEmail("Acme Stone", []InstallRow{installRow("a", "c@x.io", 24)}, time.Date(2026, 11, 23, 0, 0, 0, 0, installLoc), installLoc, "")
	var all strings.Builder
	all.WriteString(e.Heading + e.Reason)
	for _, p := range e.Paragraphs {
		for _, s := range p {
			all.WriteString(s.Text)
		}
	}
	for _, d := range e.Details {
		all.WriteString(d.Label + d.Value)
	}
	assert.NotContains(t, all.String(), "so-a", "internal uuids must not appear in the customer email")
	assert.NotContains(t, strings.ToLower(all.String()), "crew")
}

func TestRenderInstallEmailMultipleSortedAndUntouched(t *testing.T) {
	today := time.Date(2026, 11, 23, 0, 0, 0, 0, installLoc)
	late := installRow("b", "c@x.io", 25)
	early := installRow("a", "c@x.io", 24)
	in := []InstallRow{late, early}
	e := renderInstallEmail("", in, today, installLoc, "/sales/sales_order/x")
	assert.Equal(t, []InstallRow{late, early}, in, "the input must not be reordered")
	assert.Equal(t, "Your installations are coming up", e.Heading)
	require.Len(t, e.Details, 4)
	assert.Contains(t, e.Details[0].Value, "Nov 24", "soonest first")
	assert.Contains(t, e.Details[2].Value, "Nov 25")
	assert.NotEmpty(t, e.Paragraphs[0])
}

func TestDefaultJobsIncludesInstallReminders(t *testing.T) {
	var names []string
	for _, j := range DefaultJobs(Deps{}) {
		names = append(names, j.Name())
	}
	assert.Contains(t, names, installJobName)
}
