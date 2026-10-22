package automation

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRenderExpiryDigest(t *testing.T) {
	today := time.Date(2026, 10, 16, 0, 0, 0, 0, time.FixedZone("x", -5*3600))
	exp := func(n int) time.Time { return time.Date(2026, 10, 16+n, 0, 0, 0, 0, time.UTC) }
	tests := []struct {
		name       string
		items      []ExpiryDoc
		wantLabels []string
		wantValue  []string
		wantLinks  int
	}{
		{"upcoming", []ExpiryDoc{{Number: "Q-1", CustomerName: "Acme", ValidUntil: exp(3), Link: "https://x/1"}},
			[]string{"Quote Q-1"}, []string{"expires in 3 days"}, 1},
		{"expired", []ExpiryDoc{{Number: "Q-2", CustomerName: "Acme", ValidUntil: exp(-2), Link: "https://x/2"}},
			[]string{"Quote Q-2"}, []string{"expired 2 days ago"}, 1},
		{"empty link omitted", []ExpiryDoc{{Number: "Q-3", CustomerName: "Acme", ValidUntil: exp(0)}},
			[]string{"Quote Q-3"}, []string{"expires today"}, 0},
		{"six ordered by expiry then number", []ExpiryDoc{
			{Number: "F", ValidUntil: exp(2), Link: "l"}, {Number: "C", ValidUntil: exp(-1), Link: "l"},
			{Number: "E", ValidUntil: exp(0), Link: "l"}, {Number: "A", ValidUntil: exp(-2), Link: "l"},
			{Number: "D", ValidUntil: exp(-1), Link: "l"}, {Number: "B", ValidUntil: exp(-2), Link: "l"}},
			[]string{"Quote A", "Quote B", "Quote C", "Quote D", "Quote E", "Quote F"}, nil, 6},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			in := append([]ExpiryDoc(nil), tc.items...)
			e := renderExpiryDigest("Acme Stone", "quote", in, today)
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
			assert.True(t, e.Greet)
		})
	}
}

func TestRenderExpiryDigestWording(t *testing.T) {
	d := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	one := renderExpiryDigest("", "estimate", []ExpiryDoc{{Number: "E-1", ValidUntil: d}}, d)
	assert.Equal(t, "Estimate expiry reminder", one.Heading)
	many := renderExpiryDigest("", "quote", []ExpiryDoc{{Number: "1", ValidUntil: d}, {Number: "2", ValidUntil: d}}, d)
	var all strings.Builder
	for _, p := range many.Paragraphs {
		for _, s := range p {
			all.WriteString(s.Text)
		}
	}
	require.Contains(t, all.String(), "These quotes are about to expire")
	assert.NotContains(t, strings.ToLower(all.String()), "offer")
}

func TestDefaultJobsIncludesExpiryReminders(t *testing.T) {
	names := []string{}
	for _, j := range DefaultJobs(Deps{}) {
		names = append(names, j.Name())
	}
	assert.Contains(t, names, "expiry_reminders")
}

func TestRenderExpiryDigestEmptyNoun(t *testing.T) {
	d := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC)
	assert.NotPanics(t, func() { renderExpiryDigest("", "", []ExpiryDoc{{Number: "1", ValidUntil: d}}, d) })
}

func TestExpiryBody(t *testing.T) {
	tests := []struct {
		noun string
		n    int
		want string
	}{
		{"quote", 1, "1 quote is about to expire."},
		{"quote", 3, "3 quotes are about to expire."},
		{"estimate", 2, "2 estimates are about to expire."},
	}
	for _, tc := range tests {
		assert.Equal(t, tc.want, expiryBody(tc.noun, tc.n))
	}
}
