package automation

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTenantToday(t *testing.T) {
	chicago, err := time.LoadLocation("America/Chicago")
	require.NoError(t, err)

	tests := []struct {
		name     string
		now      time.Time
		tz       string
		fallback *time.Location
		want     string
	}{
		{
			name: "UTC already tomorrow, Chicago still today",
			now:  time.Date(2026, 10, 15, 2, 0, 0, 0, time.UTC), // 21:00 CDT on the 14th
			tz:   "America/Chicago", fallback: time.UTC, want: "2026-10-14",
		},
		{
			name: "same calendar day in both zones",
			now:  time.Date(2026, 10, 15, 18, 0, 0, 0, time.UTC),
			tz:   "America/New_York", fallback: time.UTC, want: "2026-10-15",
		},
		{
			name: "empty tz uses fallback",
			now:  time.Date(2026, 10, 15, 2, 0, 0, 0, time.UTC),
			tz:   "", fallback: chicago, want: "2026-10-14",
		},
		{
			name: "invalid tz uses fallback",
			now:  time.Date(2026, 10, 15, 2, 0, 0, 0, time.UTC),
			tz:   "Not/AZone", fallback: chicago, want: "2026-10-14",
		},
		{
			name: "nil fallback is UTC",
			now:  time.Date(2026, 10, 15, 2, 0, 0, 0, time.UTC),
			tz:   "", fallback: nil, want: "2026-10-15",
		},
		{
			name: "DST spring-forward night resolves the local date",
			now:  time.Date(2026, 3, 8, 7, 30, 0, 0, time.UTC), // 01:30 CST on Mar 8
			tz:   "America/Chicago", fallback: time.UTC, want: "2026-03-08",
		},
		{
			name: "whitespace around tz is ignored",
			now:  time.Date(2026, 10, 15, 2, 0, 0, 0, time.UTC),
			tz:   "  America/Los_Angeles ", fallback: time.UTC, want: "2026-10-14",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := TenantToday(tc.now, tc.tz, tc.fallback)
			assert.Equal(t, tc.want, got.Format("2006-01-02"))
			assert.Equal(t, 0, got.Hour())
		})
	}
}
