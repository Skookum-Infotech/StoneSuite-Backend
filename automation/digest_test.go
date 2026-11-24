package automation

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var digestLoc = time.FixedZone("tenant", -5*3600)

func digestEnv(hour int) JobEnv {
	return JobEnv{Now: time.Date(2026, 11, 23, hour, 30, 0, 0, digestLoc),
		Today: time.Date(2026, 11, 23, 0, 0, 0, 0, digestLoc), Loc: digestLoc}
}

func digestRecipient(email string, hour int) DigestRecipient {
	p := DefaultDigestPrefs()
	p.Enabled, p.SendHour = true, hour
	return DigestRecipient{UserID: "u-" + email, IdentityID: "i-" + email, Email: email, EmployeeID: 7, Prefs: p}
}

func TestDigestPrefsValidate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*DigestPrefs)
		ok     bool
	}{
		{"defaults", func(*DigestPrefs) {}, true},
		{"first hour", func(p *DigestPrefs) { p.SendHour = 8 }, true},
		{"last hour in window", func(p *DigestPrefs) { p.SendHour = 17 }, true},
		{"before window", func(p *DigestPrefs) { p.SendHour = 7 }, false},
		{"window end is closed", func(p *DigestPrefs) { p.SendHour = 18 }, false},
		{"negative hour", func(p *DigestPrefs) { p.SendHour = -1 }, false},
		{"on with no sections", func(p *DigestPrefs) {
			p.Enabled, p.IncludeApprovals, p.IncludeInvoices, p.IncludeExpiring = true, false, false, false
		}, false},
		{"off with no sections is allowed", func(p *DigestPrefs) {
			p.Enabled, p.IncludeApprovals, p.IncludeInvoices, p.IncludeExpiring = false, false, false, false
		}, true},
		{"one section is enough", func(p *DigestPrefs) {
			p.Enabled, p.IncludeApprovals, p.IncludeInvoices, p.IncludeExpiring = true, false, true, false
		}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := DefaultDigestPrefs()
			tc.mutate(&p)
			err := p.Validate()
			if tc.ok {
				require.NoError(t, err)
				return
			}
			assert.True(t, errors.Is(err, ErrInvalidDigestPrefs), "got %v", err)
		})
	}
}

func TestDefaultDigestPrefsAreOffAndValid(t *testing.T) {
	d := DefaultDigestPrefs()
	assert.False(t, d.Enabled, "a user who never opted in must not get a digest")
	assert.NoError(t, d.Validate())
}

func TestDigestInsertsFor(t *testing.T) {
	rule := ruleRow{ID: "rule-d"}
	tests := []struct {
		name  string
		hour  int
		rcpts []DigestRecipient
		want  int
	}{
		{"send hour reached", 9, []DigestRecipient{digestRecipient("a@x.io", 9)}, 1},
		{"before the chosen hour", 9, []DigestRecipient{digestRecipient("a@x.io", 11)}, 0},
		{"after the chosen hour still plans today", 15, []DigestRecipient{digestRecipient("a@x.io", 9)}, 1},
		{"before the window opens", 6, []DigestRecipient{digestRecipient("a@x.io", 8)}, 0},
		{"after the window closes", 19, []DigestRecipient{digestRecipient("a@x.io", 8)}, 0},
		{"opted out", 9, []DigestRecipient{func() DigestRecipient { r := digestRecipient("a@x.io", 9); r.Prefs.Enabled = false; return r }()}, 0},
		{"no email", 9, []DigestRecipient{digestRecipient("", 9)}, 0},
		{"two users, different hours", 10, []DigestRecipient{digestRecipient("a@x.io", 9), digestRecipient("b@x.io", 14)}, 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := digestInsertsFor(tc.rcpts, digestEnv(tc.hour), rule)
			require.Len(t, got, tc.want)
			for _, g := range got {
				assert.Equal(t, statusPlanned, g.Status)
				require.NotNil(t, g.Digest)
				require.NotNil(t, g.UserID)
			}
		})
	}
}

func TestDigestInsertsForIsOncePerUserPerDay(t *testing.T) {
	r := digestRecipient("a@x.io", 9)
	morning := digestInsertsFor([]DigestRecipient{r}, digestEnv(9), ruleRow{ID: "rule-d"})
	afternoon := digestInsertsFor([]DigestRecipient{r}, digestEnv(15), ruleRow{ID: "rule-d"})
	require.Len(t, morning, 1)
	require.Len(t, afternoon, 1)
	assert.Equal(t, morning[0].Key, afternoon[0].Key, "later ticks the same day must collide so only one digest is planned")

	tomorrow := digestEnv(9)
	tomorrow.Today = tomorrow.Today.AddDate(0, 0, 1)
	next := digestInsertsFor([]DigestRecipient{r}, tomorrow, ruleRow{ID: "rule-d"})
	assert.NotEqual(t, morning[0].Key, next[0].Key, "the next day is a new occurrence")
}

func TestDigestContentEmpty(t *testing.T) {
	assert.True(t, digestContent{}.empty())
	assert.False(t, digestContent{Approvals: 1}.empty())
	assert.False(t, digestContent{OverdueInvoices: 1}.empty())
	assert.False(t, digestContent{ExpiringQuotes: 1}.empty())
	assert.False(t, digestContent{ExpiringEstimates: 1}.empty())
}

func TestDigestDetails(t *testing.T) {
	today := time.Date(2026, 11, 23, 0, 0, 0, 0, digestLoc)
	oldest := time.Date(2026, 11, 20, 0, 0, 0, 0, time.UTC)
	rows := digestDetails(digestContent{Approvals: 3, OldestApproval: &oldest, OverdueInvoices: 1, OverdueTotal: 1250.5,
		ExpiringQuotes: 2, ExpiringEstimates: 1}, today)
	require.Len(t, rows, 4)
	assert.Equal(t, "Waiting for your approval", rows[0].Label)
	assert.Equal(t, "3 records, oldest waiting 3 days", rows[0].Value)
	assert.Equal(t, "1 invoice, $1,250.50 outstanding", rows[1].Value)
	assert.Equal(t, "2 quotes", rows[2].Value)
	assert.Equal(t, "1 estimate", rows[3].Value)

	assert.Empty(t, digestDetails(digestContent{}, today), "sections with nothing to report are left out")
	one := digestDetails(digestContent{Approvals: 1}, today)
	require.Len(t, one, 1)
	assert.Equal(t, "1 record", one[0].Value, "no age is shown without an oldest date")
}

func TestDigestBodyAndEmail(t *testing.T) {
	today := time.Date(2026, 11, 23, 0, 0, 0, 0, digestLoc)
	assert.Equal(t, digestNothing, digestBody(digestContent{}))
	assert.Equal(t, "1 item needs your attention.", digestBody(digestContent{Approvals: 5}))
	assert.Equal(t, "2 items need your attention.", digestBody(digestContent{Approvals: 5, OverdueInvoices: 1}))

	e := renderDigestEmail("Acme Stone", digestContent{OverdueInvoices: 2, OverdueTotal: 10}, today)
	assert.Equal(t, digestTitle, e.Heading)
	require.Len(t, e.Details, 1)

	empty := renderDigestEmail("", digestContent{}, today)
	assert.Empty(t, empty.Details)
	require.NotEmpty(t, empty.Paragraphs)
}

func TestPlural(t *testing.T) {
	assert.Equal(t, "1 day", plural(1, "day"))
	assert.Equal(t, "2 days", plural(2, "day"))
	assert.Equal(t, "0 records", plural(0, "record"))
}

func TestDefaultJobsIncludesDailyDigest(t *testing.T) {
	var names []string
	for _, j := range DefaultJobs(Deps{}) {
		names = append(names, j.Name())
	}
	assert.Contains(t, names, digestJobName)
}
