package automation

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNextSendWindow(t *testing.T) {
	tests := []struct {
		name, tz, now, want string
	}{
		{"before window on spring-forward day", "America/Chicago", "2026-03-08T07:00:00Z", "2026-03-08T13:00:00Z"},
		{"second 01:30 on fall-back day", "America/Chicago", "2026-11-01T07:30:00Z", "2026-11-01T14:00:00Z"},
		{"inside window returns now", "America/Chicago", "2026-11-02T15:00:00Z", "2026-11-02T15:00:00Z"},
		{"18:00 is outside, next morning", "America/Chicago", "2026-11-02T00:00:00Z", "2026-11-02T14:00:00Z"},
		{"exactly 08:00 is inside", "America/New_York", "2026-07-01T12:00:00Z", "2026-07-01T12:00:00Z"},
		{"phoenix ignores DST", "America/Phoenix", "2026-03-08T13:00:00Z", "2026-03-08T15:00:00Z"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			loc, err := time.LoadLocation(tc.tz)
			require.NoError(t, err)
			now, err := time.Parse(time.RFC3339, tc.now)
			require.NoError(t, err)
			want, err := time.Parse(time.RFC3339, tc.want)
			require.NoError(t, err)
			got := NextSendWindow(now, loc)
			assert.True(t, want.Equal(got), "got %s", got.UTC().Format(time.RFC3339))
			assert.True(t, InSendWindow(got, loc))
		})
	}
}
