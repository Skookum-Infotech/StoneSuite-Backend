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

func TestIsBefore(t *testing.T) {
	tests := []struct {
		name     string
		d        time.Time
		boundary time.Time
		want     bool
	}{
		{"day before", date(2026, 6, 30), date(2026, 7, 1), true},
		{"same day", date(2026, 7, 1), date(2026, 7, 1), false},
		{"day after", date(2026, 7, 2), date(2026, 7, 1), false},
		{"same day, late", time.Date(2026, 7, 1, 23, 59, 0, 0, time.UTC), date(2026, 7, 1), false},
		{"year before", date(2025, 12, 31), date(2026, 1, 1), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isBefore(tt.d, tt.boundary); got != tt.want {
				t.Errorf("isBefore(%v, %v) = %v, want %v", tt.d, tt.boundary, got, tt.want)
			}
		})
	}
}

// TestVerdictFor covers the resolution table in the module design doc §4,
// including the backward-compatibility row: a tenant with no accounting_period
// rows must fall through to books_closed_through and behave exactly as it did
// before this module existed.
func TestVerdictFor(t *testing.T) {
	ptr := func(t time.Time) *time.Time { return &t }
	effective := date(2026, 7, 15)

	tests := []struct {
		name   string
		lookup periodLookup
		want   periodVerdict
	}{
		{
			name:   "period covers date and is open",
			lookup: periodLookup{Found: true, Status: statusOpen, CalendarExists: true},
			want:   verdictOpen,
		},
		{
			name:   "period covers date and is closed",
			lookup: periodLookup{Found: true, Status: statusClosed, CalendarExists: true},
			want:   verdictClosed,
		},
		{
			name: "closed period wins over an unset legacy column",
			lookup: periodLookup{
				Found: true, Status: statusClosed, CalendarExists: true, ClosedThrough: nil,
			},
			want: verdictClosed,
		},
		{
			name: "open period wins over a legacy column that would close it",
			lookup: periodLookup{
				Found: true, Status: statusOpen, CalendarExists: true,
				ClosedThrough: ptr(date(2026, 12, 31)),
			},
			want: verdictOpen,
		},
		{
			name:   "no calendar, no books_closed_through: nothing is closed",
			lookup: periodLookup{CalendarExists: false, ClosedThrough: nil},
			want:   verdictOpen,
		},
		{
			name: "no calendar, date at or before books_closed_through",
			lookup: periodLookup{
				CalendarExists: false, ClosedThrough: ptr(date(2026, 7, 31)),
			},
			want: verdictClosed,
		},
		{
			name: "no calendar, date after books_closed_through",
			lookup: periodLookup{
				CalendarExists: false, ClosedThrough: ptr(date(2026, 6, 30)),
			},
			want: verdictOpen,
		},
		{
			name: "calendar exists, date before the base period: permanently closed",
			lookup: periodLookup{
				CalendarExists: true, BasePeriodStart: ptr(date(2026, 8, 1)),
			},
			want: verdictClosed,
		},
		{
			name: "calendar exists, date on the base period start is not before it",
			lookup: periodLookup{
				CalendarExists: true, BasePeriodStart: ptr(effective),
			},
			want: verdictNoPeriod,
		},
		{
			name: "calendar exists, date past the last generated period",
			lookup: periodLookup{
				CalendarExists: true, BasePeriodStart: ptr(date(2026, 1, 1)),
			},
			want: verdictNoPeriod,
		},
		{
			name:   "calendar exists but base period start is unset",
			lookup: periodLookup{CalendarExists: true, BasePeriodStart: nil},
			want:   verdictNoPeriod,
		},
		{
			name: "uncovered date ignores books_closed_through once a calendar exists",
			lookup: periodLookup{
				CalendarExists: true, BasePeriodStart: ptr(date(2026, 1, 1)),
				ClosedThrough: ptr(date(2026, 12, 31)),
			},
			want: verdictNoPeriod,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := verdictFor(effective, tt.lookup); got != tt.want {
				t.Errorf("verdictFor(%v, %+v) = %v, want %v", effective, tt.lookup, got, tt.want)
			}
		})
	}
}
