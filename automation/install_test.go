package automation

import (
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

func installRow(uuid, userID, email string, day int) InstallRow {
	return InstallRow{UUID: uuid, UserID: userID, Email: email, EmployeeID: 7, VisitType: "install",
		CustomerName: "Acme", JobNumber: "FJOB-000001", Timezone: "America/Chicago",
		StartsAt:   time.Date(2026, 11, day, 14, 0, 0, 0, time.UTC),
		LocalStart: time.Date(2026, 11, day, 0, 0, 0, 0, time.UTC), Status: installStatusScheduled}
}

func TestInstallInsertsFor(t *testing.T) {
	tests := []struct {
		name       string
		row        InstallRow
		env        JobEnv
		wantStatus string
		wantReason string
		wantN      int
	}{
		{"tomorrow is planned", installRow("a", "u1", "c@x.io", 24), installEnv(9), statusPlanned, "", 1},
		{"day after tomorrow not due yet", installRow("a", "u1", "c@x.io", 25), installEnv(9), "", "", 0},
		{"visit today gets a catch-up reminder", installRow("a", "u1", "c@x.io", 23), installEnv(9), statusPlanned, "", 1},
		{"no email is skipped", installRow("a", "", "", 24), installEnv(9), statusSkipped, reasonNoEmail, 1},
		{"planned after hours waits for the window", installRow("a", "u1", "c@x.io", 24), installEnv(22), statusPlanned, "", 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := installInsertsFor([]InstallRow{tc.row}, tc.env, installRule())
			require.Len(t, got, tc.wantN)
			if tc.wantN == 0 {
				return
			}
			assert.Equal(t, tc.wantStatus, got[0].Status)
			if tc.wantReason != "" {
				require.NotNil(t, got[0].Reason)
				assert.Equal(t, tc.wantReason, *got[0].Reason)
			}
			if tc.wantStatus == statusPlanned {
				require.NotNil(t, got[0].Digest)
				assert.True(t, InSendWindow(got[0].RunAt, installLoc), "run_at must be inside the send window")
			}
		})
	}
}

func TestInstallInsertsForKeysPerRecipient(t *testing.T) {
	a := installRow("appt", "u1", "a@x.io", 24)
	b := installRow("appt", "u2", "b@x.io", 24)
	got := installInsertsFor([]InstallRow{a, b, a}, installEnv(9), installRule())
	require.Len(t, got, 3)
	assert.NotEqual(t, got[0].Key, got[1].Key, "each crew member needs their own occurrence key")
	assert.Equal(t, got[0].Key, got[2].Key, "same recipient and visit must collide so the insert is idempotent")
	assert.NotEqual(t, *got[0].Digest, *got[1].Digest)
}

func TestInstallInsertsForNotRetroactive(t *testing.T) {
	rule := installRule()
	rule.EnabledOn = time.Date(2026, 11, 23, 8, 0, 0, 0, installLoc) // enabled today
	got := installInsertsFor([]InstallRow{installRow("a", "u1", "c@x.io", 24)}, installEnv(9), rule)
	require.Len(t, got, 1)
	assert.Equal(t, statusSkipped, got[0].Status)
	require.NotNil(t, got[0].Reason)
	assert.Equal(t, string(StageActionPreEnable), *got[0].Reason)
}

func TestInstallReason(t *testing.T) {
	planned := time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC)
	base := installRow("a", "u1", "c@x.io", 24)
	mut := func(f func(*InstallRow)) InstallRow { r := base; f(&r); return r }
	tests := []struct {
		name string
		row  InstallRow
		want string
	}{
		{"eligible", base, ""},
		{"job deleted", mut(func(r *InstallRow) { r.JobDeleted = true }), reasonVoided},
		{"appointment cancelled", mut(func(r *InstallRow) { r.Status = installStatusCancelled }), reasonCancelled},
		{"job cancelled", mut(func(r *InstallRow) { r.JobCancelled = true }), reasonCancelled},
		{"completed", mut(func(r *InstallRow) { r.Status = installStatusCompleted }), reasonCompleted},
		{"rescheduled", mut(func(r *InstallRow) { r.LocalStart = r.LocalStart.AddDate(0, 0, 3) }), reasonAnchorChanged},
		{"recipient lost email", mut(func(r *InstallRow) { r.Email = "" }), reasonNoEmail},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) { assert.Equal(t, tc.want, installReason(tc.row, planned)) })
	}
}

func TestRenderInstallDigest(t *testing.T) {
	today := time.Date(2026, 11, 23, 0, 0, 0, 0, installLoc)
	late := installRow("b", "u1", "c@x.io", 24)
	late.StartsAt = late.StartsAt.Add(3 * time.Hour)
	early := installRow("a", "u1", "c@x.io", 24)
	early.VisitType = "service"
	in := []InstallRow{late, early}
	e := renderInstallDigest("Acme Stone", in, today, installLoc)
	assert.Equal(t, []InstallRow{late, early}, in, "input must not be reordered")
	require.Len(t, e.Details, 2)
	assert.Equal(t, "Service visit FJOB-000001", e.Details[0].Label, "soonest visit first")
	assert.Contains(t, e.Details[0].Value, "tomorrow")
	assert.Contains(t, e.Details[0].Value, "Acme")
	assert.Equal(t, installTitle, e.Heading)
}

func TestInstallWhen(t *testing.T) {
	r := installRow("a", "u1", "c@x.io", 24)
	assert.Equal(t, "Tue, Nov 24, 8:00 AM", installWhen(r, installLoc))
	r.AllDay = true
	assert.Equal(t, "Tue, Nov 24, all day", installWhen(r, installLoc))
	r.AllDay, r.Timezone = false, "Not/AZone"
	assert.Equal(t, "Tue, Nov 24, 9:00 AM", installWhen(r, time.FixedZone("f", -5*3600)), "unknown zone falls back")
}

func TestInstallBodyAndTiming(t *testing.T) {
	assert.Equal(t, "1 site visit is coming up.", installBody(1))
	assert.Equal(t, "3 site visits are coming up.", installBody(3))
	assert.Equal(t, "tomorrow", installTiming(1))
	assert.Equal(t, "today", installTiming(0))
	assert.Equal(t, "in 2 days", installTiming(2))
}

func TestDefaultJobsIncludesInstallReminders(t *testing.T) {
	var names []string
	for _, j := range DefaultJobs(Deps{}) {
		names = append(names, j.Name())
	}
	assert.Contains(t, names, installJobName)
}
