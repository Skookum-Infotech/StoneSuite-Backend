package automation

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderARDigest(t *testing.T) {
	today := time.Date(2026, 10, 16, 0, 0, 0, 0, time.FixedZone("x", -5*3600))
	due := func(n int) time.Time { return time.Date(2026, 10, 16+n, 0, 0, 0, 0, time.UTC) }
	tests := []struct {
		name       string
		items      []ARInvoice
		wantLabels []string
		wantValue  []string
		wantLinks  int
	}{
		{"upcoming", []ARInvoice{{Number: "INV-1", Anchor: due(3), Balance: "100.00", Link: "https://x/1"}},
			[]string{"Invoice INV-1"}, []string{"due in 3 days"}, 1},
		{"overdue", []ARInvoice{{Number: "INV-2", Anchor: due(-7), Balance: "40.50", Link: "https://x/2"}},
			[]string{"Invoice INV-2"}, []string{"7 days overdue"}, 1},
		{"due today", []ARInvoice{{Number: "INV-3", Anchor: due(0), Balance: "1.00"}},
			[]string{"Invoice INV-3"}, []string{"due today"}, 0},
		{"five ordered by due date", []ARInvoice{
			{Number: "E", Anchor: due(2), Balance: "5", Link: "l"}, {Number: "B", Anchor: due(-10), Balance: "2", Link: "l"},
			{Number: "D", Anchor: due(0), Balance: "4", Link: "l"}, {Number: "A", Anchor: due(-20), Balance: "1", Link: "l"},
			{Number: "C", Anchor: due(-10), Balance: "3", Link: "l"}},
			[]string{"Invoice A", "Invoice B", "Invoice C", "Invoice D", "Invoice E"}, nil, 5},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := append([]ARInvoice(nil), tc.items...)
			e := renderARDigest("Acme Stone", in, today)
			assert.Equal(t, tc.items, in, "input must not be reordered")
			var labels []string
			for _, d := range e.Details {
				labels = append(labels, d.Label)
			}
			assert.Equal(t, tc.wantLabels, labels)
			for i, v := range tc.wantValue {
				assert.Contains(t, e.Details[i].Value, v)
			}
			links := 0
			for _, p := range e.Paragraphs {
				for _, s := range p {
					if s.Href != "" {
						links++
					}
				}
			}
			assert.Equal(t, tc.wantLinks, links)
			assert.Empty(t, e.ActionURL)
			require.NotEmpty(t, e.Paragraphs)
			assert.Contains(t, e.Paragraphs[0][1].Text, "Acme Stone")
			assert.True(t, e.Greet)
		})
	}
}

func TestRenderARDigestWording(t *testing.T) {
	e := renderARDigest("", []ARInvoice{{Number: "X", Anchor: time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC), Balance: "9"}}, time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC))
	assert.Equal(t, "Payment reminder", e.Heading)
	var all strings.Builder
	for _, p := range e.Paragraphs {
		for _, s := range p {
			all.WriteString(s.Text)
		}
	}
	assert.NotContains(t, strings.ToLower(all.String()), "offer")
	assert.Contains(t, all.String(), "from us")
}

func TestDefaultJobsIncludesARDunning(t *testing.T) {
	names := []string{}
	for _, j := range DefaultJobs(Deps{}) {
		names = append(names, j.Name())
	}
	assert.Contains(t, names, RuleKeyARDunning)
}
