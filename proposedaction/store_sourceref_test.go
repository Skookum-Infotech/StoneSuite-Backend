package proposedaction

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestEscapeLikePrefix(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"plain", "ext:abc", "ext:abc"},
		{"percent", "a%b", `a\%b`},
		{"underscore", "a_b", `a\_b`},
		{"backslash", `a\b`, `a\\b`},
		{"all", `%_\`, `\%\_\\`},
		{"empty", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, escapeLikePrefix(c.in))
		})
	}
}

func TestReportExpired(t *testing.T) {
	now := time.Now()
	cases := []struct {
		name string
		in   Action
		want Status
	}{
		{"pending live", Action{Status: StatusPending, ExpiresAt: now.Add(time.Hour)}, StatusPending},
		{"pending overdue", Action{Status: StatusPending, ExpiresAt: now.Add(-time.Hour)}, StatusExpired},
		{"confirmed overdue", Action{Status: StatusConfirmed, ExpiresAt: now.Add(-time.Hour)}, StatusConfirmed},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, reportExpired(c.in, now).Status)
		})
	}
}
