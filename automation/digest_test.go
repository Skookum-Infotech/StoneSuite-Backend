package automation

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/approvalinbox"
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
			p.Enabled, p.IncludeInstalls, p.IncludeVendorBills, p.IncludeApprovals = true, false, false, false
		}, false},
		{"off with no sections is allowed", func(p *DigestPrefs) {
			p.Enabled, p.IncludeInstalls, p.IncludeVendorBills, p.IncludeApprovals = false, false, false, false
		}, true},
		{"one section is enough", func(p *DigestPrefs) {
			p.Enabled, p.IncludeInstalls, p.IncludeVendorBills, p.IncludeApprovals = true, false, true, false
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
	assert.False(t, digestContent{Installs: []InstallRow{{}}}.empty())
	assert.False(t, digestContent{Bills: []VendorBillDoc{{}}}.empty())
	assert.False(t, digestContent{Approvals: approvalinbox.Counts{Total: 1}}.empty())
}

func digestSample() digestContent {
	inst := installRow("a", "c@x.io", 24)
	bill := VendorBillDoc{Number: "VB-7", VendorName: "Granite Co", Balance: 980, DueDate: time.Date(2026, 11, 24, 0, 0, 0, 0, time.UTC)}
	return digestContent{
		Installs: []InstallRow{inst},
		Bills:    []VendorBillDoc{bill},
		Approvals: approvalinbox.Counts{Total: 3, Modules: []approvalinbox.ModuleCount{
			{WorkflowKey: "quote", Label: "Quotes", Count: 2}, {WorkflowKey: "estimate", Label: "Estimates", Count: 1}, {WorkflowKey: "sales_order", Label: "Sales Orders"}}},
	}
}

func TestDigestDetails(t *testing.T) {
	today := time.Date(2026, 11, 23, 0, 0, 0, 0, digestLoc)
	rows := digestDetails(digestSample(), today, installLoc)
	require.Len(t, rows, 4, "one install, one bill, and a row per module that has something waiting")
	assert.Equal(t, "Installation Tue, Nov 24, 8:00 AM", rows[0].Label)
	assert.Equal(t, "SORD-1 - Acme Stone", rows[0].Value)
	assert.Equal(t, "Vendor bill VB-7 (Granite Co)", rows[1].Label)
	assert.Equal(t, "$980.00, due tomorrow", rows[1].Value)
	assert.Equal(t, "Quotes waiting for your approval", rows[2].Label)
	assert.Equal(t, "2", rows[2].Value)
	assert.Equal(t, "Estimates waiting for your approval", rows[3].Label)

	assert.Empty(t, digestDetails(digestContent{}, today, installLoc), "sections with nothing to report are left out")
}

func TestDigestDetailsCapsLongLists(t *testing.T) {
	today := time.Date(2026, 11, 23, 0, 0, 0, 0, digestLoc)
	c := digestContent{}
	for i := 0; i < 8; i++ {
		c.Installs = append(c.Installs, installRow("a", "c@x.io", 24))
		c.Bills = append(c.Bills, VendorBillDoc{Number: "VB", DueDate: today})
	}
	rows := digestDetails(c, today, installLoc)
	require.Len(t, rows, 2*(digestListMax+1))
	assert.Equal(t, "+3 more", rows[digestListMax].Value)
}

func TestDigestDueWording(t *testing.T) {
	today := time.Date(2026, 11, 23, 0, 0, 0, 0, time.UTC)
	tests := []struct {
		offset int
		want   string
	}{{-2, "overdue by 2 days"}, {-1, "overdue by 1 day"}, {0, "due today"}, {1, "due tomorrow"}, {3, "due in 3 days"}}
	for _, tc := range tests {
		assert.Equal(t, tc.want, digestDueWording(today.AddDate(0, 0, tc.offset), today))
	}
}

func TestDigestBodyAndEmail(t *testing.T) {
	today := time.Date(2026, 11, 23, 0, 0, 0, 0, digestLoc)
	assert.Equal(t, digestNothing, digestBody(digestContent{}))
	assert.Equal(t, "1 installation, 1 vendor bill due, 3 approvals waiting.", digestBody(digestSample()))
	assert.Equal(t, "2 installations.", digestBody(digestContent{Installs: []InstallRow{{}, {}}}))

	e := renderDigestEmail("Acme Stone", digestSample(), today, installLoc)
	assert.Equal(t, digestTitle, e.Heading)
	require.Len(t, e.Details, 4)

	empty := renderDigestEmail("", digestContent{}, today, installLoc)
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
