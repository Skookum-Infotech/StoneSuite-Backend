package anomaly

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"stonesuite-backend/config"
	"stonesuite-backend/services"
)

func TestBuildEmailMatchesTheSharedTemplate(t *testing.T) {
	prev := config.AppConfig.FrontendURL
	config.AppConfig.FrontendURL = "https://app.example.test"
	t.Cleanup(func() { config.AppConfig.FrontendURL = prev })

	a := pendingAlert{
		ruleKey: RuleUnbalancedJournal, severity: string(SeverityCritical),
		title:      "Journal entry JE-000042 does not balance",
		detail:     "Debits total 1250.00 but credits total 1205.00.",
		detectedAt: time.Date(2026, 10, 27, 9, 15, 0, 0, time.UTC),
	}
	e := buildEmail(a)
	assert.Equal(t, "Critical Alert", e.Badge)
	assert.Equal(t, services.IconDocClock, e.Icon)
	assert.Equal(t, a.title, e.Heading)
	assert.True(t, e.Greet, "opens with Hello {name}, like the approval emails")
	assert.Equal(t, "Review anomaly", e.ActionLabel)
	assert.Equal(t, services.AppURL(AnomaliesLink), e.ActionURL)
	assert.Equal(t, []services.Detail{
		{Label: "Anomaly", Value: "Unbalanced journal entry"},
		{Label: "Severity", Value: "Critical"},
		{Label: "Detected On", Value: "Oct 27, 2026"},
	}, e.Details)
	assert.Len(t, e.Paragraphs, 2, "the finding's own detail, then what to do next")

	html, err := services.RenderEmail(*e)
	require.NoError(t, err)
	for _, want := range []string{"JE-000042", "CRITICAL ALERT", "Review anomaly", "https://app.example.test/anomalies", "Unbalanced journal entry", "Oct 27, 2026", "1205.00"} {
		assert.Contains(t, html, want)
	}
}

func TestBuildEmailBadgePerSeverity(t *testing.T) {
	tests := []struct {
		severity string
		want     string
	}{
		{string(SeverityCritical), "Critical Alert"},
		{string(SeverityWarning), "Needs Review"},
		{string(SeverityInfo), "For Your Information"},
		{"unknown", "For Your Information"},
	}
	for _, tt := range tests {
		t.Run(tt.severity, func(t *testing.T) {
			assert.Equal(t, tt.want, buildEmail(pendingAlert{severity: tt.severity, title: "t"}).Badge)
		})
	}
}

func TestBuildEmailOmitsAnEmptyDetailParagraph(t *testing.T) {
	assert.Len(t, buildEmail(pendingAlert{severity: string(SeverityCritical), title: "t"}).Paragraphs, 1)
}

func TestRuleLabel(t *testing.T) {
	assert.Equal(t, "Approved by its own owner", ruleLabel(RuleSelfApproval))
	assert.Equal(t, "Possible duplicate vendor bill", ruleLabel(RuleDuplicateVendorBill))
	assert.Equal(t, "brand new rule", ruleLabel("brand_new_rule"), "an unlisted rule falls back to a readable key")
}

func TestEveryDefaultRuleHasALabel(t *testing.T) {
	for _, r := range DefaultRules() {
		_, ok := ruleLabels[r.Key()]
		assert.True(t, ok, "add a label for rule %s", r.Key())
	}
}
