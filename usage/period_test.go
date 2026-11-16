package usage

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func day(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

func TestCurrentPeriod(t *testing.T) {
	tests := []struct {
		name   string
		anchor time.Time
		now    time.Time
		want   Period
	}{
		{"mid cycle", day(2026, 1, 15), time.Date(2026, 11, 20, 9, 0, 0, 0, time.UTC), Period{day(2026, 11, 15), day(2026, 12, 15)}},
		{"before this month's anchor day", day(2026, 1, 15), day(2026, 11, 3), Period{day(2026, 10, 15), day(2026, 11, 15)}},
		{"exactly on anchor day", day(2026, 1, 15), day(2026, 11, 15), Period{day(2026, 11, 15), day(2026, 12, 15)}},
		{"first of month anchor", day(2025, 6, 1), day(2026, 11, 16), Period{day(2026, 11, 1), day(2026, 12, 1)}},
		{"31st clamps in February", day(2026, 1, 31), day(2026, 2, 28), Period{day(2026, 2, 28), day(2026, 3, 31)}},
		{"31st before clamped Feb start", day(2026, 1, 31), day(2026, 2, 27), Period{day(2026, 1, 31), day(2026, 2, 28)}},
		{"leap year February", day(2027, 1, 30), day(2028, 2, 29), Period{day(2028, 2, 29), day(2028, 3, 30)}},
		{"year rollover", day(2026, 3, 20), day(2027, 1, 5), Period{day(2026, 12, 20), day(2027, 1, 20)}},
		{"now before anchor", day(2026, 11, 20), day(2026, 11, 1), Period{day(2026, 11, 20), day(2026, 12, 20)}},
		{"anchor time of day ignored", time.Date(2026, 1, 15, 23, 59, 0, 0, time.UTC), day(2026, 11, 15), Period{day(2026, 11, 15), day(2026, 12, 15)}},
		{"non-UTC now normalized", day(2026, 1, 1), time.Date(2026, 12, 1, 1, 0, 0, 0, time.FixedZone("x", 5*3600)), Period{day(2026, 11, 1), day(2026, 12, 1)}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, CurrentPeriod(tc.anchor, tc.now))
		})
	}
}
