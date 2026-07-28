package journal

import (
	"testing"
	"time"
)

func date(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

func TestIsClosed(t *testing.T) {
	tests := []struct {
		name          string
		effectiveDate time.Time
		closedThrough time.Time
		want          bool
	}{
		{"before closed date", date(2026, 6, 30), date(2026, 6, 30), true},
		{"on closed date", date(2026, 6, 30), date(2026, 6, 30), true},
		{"day after closed date", date(2026, 7, 1), date(2026, 6, 30), false},
		{"far after closed date", date(2026, 12, 31), date(2026, 6, 30), false},
		{"time-of-day is ignored", time.Date(2026, 6, 30, 23, 59, 0, 0, time.UTC), date(2026, 6, 30), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isClosed(tt.effectiveDate, tt.closedThrough); got != tt.want {
				t.Errorf("isClosed(%v, %v) = %v, want %v", tt.effectiveDate, tt.closedThrough, got, tt.want)
			}
		})
	}
}
