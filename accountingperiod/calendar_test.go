package accountingperiod

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func day(y int, m time.Month, d int) time.Time {
	return time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
}

func TestFirstOfMonth(t *testing.T) {
	tests := []struct {
		name string
		in   time.Time
		want time.Time
	}{
		{"mid month", day(2026, 7, 15), day(2026, 7, 1)},
		{"already first", day(2026, 7, 1), day(2026, 7, 1)},
		{"last day", day(2026, 7, 31), day(2026, 7, 1)},
		{"strips time of day", time.Date(2026, 7, 15, 23, 59, 59, 0, time.UTC), day(2026, 7, 1)},
		{"normalizes to UTC-midnight from another zone",
			time.Date(2026, 7, 15, 12, 0, 0, 0, time.FixedZone("x", 3600)), day(2026, 7, 1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.True(t, FirstOfMonth(tt.in).Equal(tt.want),
				"FirstOfMonth(%v) = %v, want %v", tt.in, FirstOfMonth(tt.in), tt.want)
		})
	}
}

func TestLastOfMonth(t *testing.T) {
	tests := []struct {
		name string
		in   time.Time
		want time.Time
	}{
		{"31-day month", day(2026, 1, 5), day(2026, 1, 31)},
		{"30-day month", day(2026, 4, 5), day(2026, 4, 30)},
		{"february, non-leap", day(2026, 2, 5), day(2026, 2, 28)},
		{"february, leap year", day(2028, 2, 5), day(2028, 2, 29)},
		{"december", day(2026, 12, 1), day(2026, 12, 31)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.True(t, LastOfMonth(tt.in).Equal(tt.want),
				"LastOfMonth(%v) = %v, want %v", tt.in, LastOfMonth(tt.in), tt.want)
		})
	}
}

func TestValidateStartMonth(t *testing.T) {
	tests := []struct {
		name    string
		month   int
		wantErr bool
	}{
		{"january", 1, false},
		{"july", 7, false},
		{"december", 12, false},
		{"zero", 0, true},
		{"negative", -1, true},
		{"thirteen", 13, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateStartMonth(tt.month)
			if tt.wantErr {
				require.Error(t, err)
				assert.True(t, IsClientError(err), "want a ClientError, got %T", err)
				return
			}
			assert.NoError(t, err)
		})
	}
}

func TestFiscalYearStart(t *testing.T) {
	tests := []struct {
		name       string
		date       time.Time
		startMonth int
		want       time.Time
	}{
		{"calendar year, mid year", day(2026, 7, 15), 1, day(2026, 1, 1)},
		{"calendar year, january", day(2026, 1, 1), 1, day(2026, 1, 1)},
		{"calendar year, december", day(2026, 12, 31), 1, day(2026, 1, 1)},
		{"july start, after the boundary", day(2026, 8, 3), 7, day(2026, 7, 1)},
		{"july start, on the boundary", day(2026, 7, 1), 7, day(2026, 7, 1)},
		{"july start, before the boundary rolls back a year", day(2026, 6, 30), 7, day(2025, 7, 1)},
		{"april start, march belongs to the previous year", day(2026, 3, 31), 4, day(2025, 4, 1)},
		{"october start, january belongs to the previous year", day(2026, 1, 2), 10, day(2025, 10, 1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FiscalYearStart(tt.date, tt.startMonth)
			assert.True(t, got.Equal(tt.want), "got %v, want %v", got, tt.want)
		})
	}
}

func TestFiscalYearEndMonth(t *testing.T) {
	tests := []struct {
		name       string
		startMonth int
		want       int
	}{
		{"january start ends in december", 1, 12},
		{"july start ends in june", 7, 6},
		{"december start ends in november", 12, 11},
		{"february start ends in january", 2, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, FiscalYearEndMonth(tt.startMonth))
		})
	}
}

func TestFiscalYearLabel(t *testing.T) {
	tests := []struct {
		name  string
		start time.Time
		want  string
	}{
		{"calendar year is labelled by its own year", day(2026, 1, 1), "FY2026"},
		{"july start is labelled by the year it ends in", day(2026, 7, 1), "FY2027"},
		{"april start is labelled by the year it ends in", day(2026, 4, 1), "FY2027"},
		{"december start", day(2026, 12, 1), "FY2027"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, FiscalYearLabel(tt.start))
		})
	}
}

func TestMonthsFor(t *testing.T) {
	t.Run("calendar year", func(t *testing.T) {
		spans := MonthsFor(day(2026, 1, 1))
		require.Len(t, spans, PeriodsPerYear)

		assert.Equal(t, 1, spans[0].Number)
		assert.Equal(t, "Jan 2026", spans[0].Name)
		assert.True(t, spans[0].Start.Equal(day(2026, 1, 1)))
		assert.True(t, spans[0].End.Equal(day(2026, 1, 31)))

		assert.Equal(t, 12, spans[11].Number)
		assert.Equal(t, "Dec 2026", spans[11].Name)
		assert.True(t, spans[11].End.Equal(day(2026, 12, 31)))
	})

	t.Run("july start straddles the calendar year", func(t *testing.T) {
		spans := MonthsFor(day(2026, 7, 1))
		require.Len(t, spans, PeriodsPerYear)
		assert.Equal(t, "Jul 2026", spans[0].Name)
		assert.Equal(t, "Jun 2027", spans[11].Name)
		assert.True(t, spans[11].End.Equal(day(2027, 6, 30)))
	})

	t.Run("spans are contiguous and non-overlapping", func(t *testing.T) {
		spans := MonthsFor(day(2027, 11, 1)) // crosses a year end and a February
		for i := 1; i < len(spans); i++ {
			assert.True(t, spans[i].Start.Equal(spans[i-1].End.AddDate(0, 0, 1)),
				"gap or overlap between %s and %s", spans[i-1].Name, spans[i].Name)
		}
		// Feb 2028 is a leap February; it must have 29 days.
		assert.True(t, spans[3].End.Equal(day(2028, 2, 29)), "got %v", spans[3].End)
	})

	t.Run("a mid-month start is normalized", func(t *testing.T) {
		spans := MonthsFor(day(2026, 3, 17))
		assert.True(t, spans[0].Start.Equal(day(2026, 3, 1)))
	})
}

func TestQuartersFor(t *testing.T) {
	t.Run("calendar year", func(t *testing.T) {
		months := MonthsFor(day(2026, 1, 1))
		spans := QuartersFor(months, "FY2026")
		require.Len(t, spans, 4)

		assert.Equal(t, 1, spans[0].Number)
		assert.Equal(t, "Q1 FY2026", spans[0].Name)
		assert.True(t, spans[0].Start.Equal(day(2026, 1, 1)))
		assert.True(t, spans[0].End.Equal(day(2026, 3, 31)))

		assert.Equal(t, 4, spans[3].Number)
		assert.Equal(t, "Q4 FY2026", spans[3].Name)
		assert.True(t, spans[3].Start.Equal(day(2026, 10, 1)))
		assert.True(t, spans[3].End.Equal(day(2026, 12, 31)))
	})

	t.Run("july start straddles the calendar year", func(t *testing.T) {
		months := MonthsFor(day(2026, 7, 1))
		spans := QuartersFor(months, "FY2027")
		require.Len(t, spans, 4)

		assert.Equal(t, "Q1 FY2027", spans[0].Name)
		assert.True(t, spans[0].Start.Equal(day(2026, 7, 1)))
		assert.True(t, spans[0].End.Equal(day(2026, 9, 30)))

		assert.Equal(t, "Q4 FY2027", spans[3].Name)
		assert.True(t, spans[3].Start.Equal(day(2027, 4, 1)))
		assert.True(t, spans[3].End.Equal(day(2027, 6, 30)))
	})

	t.Run("spans are contiguous and non-overlapping", func(t *testing.T) {
		months := MonthsFor(day(2026, 1, 1))
		spans := QuartersFor(months, "FY2026")
		for i := 1; i < len(spans); i++ {
			assert.True(t, spans[i].Start.Equal(spans[i-1].End.AddDate(0, 0, 1)),
				"gap or overlap between %s and %s", spans[i-1].Name, spans[i].Name)
		}
	})
}

func TestFiscalYearEnd(t *testing.T) {
	tests := []struct {
		name  string
		start time.Time
		want  time.Time
	}{
		{"calendar year", day(2026, 1, 1), day(2026, 12, 31)},
		{"july start", day(2026, 7, 1), day(2027, 6, 30)},
		{"march start ending in a leap february", day(2027, 3, 1), day(2028, 2, 29)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FiscalYearEnd(tt.start)
			assert.True(t, got.Equal(tt.want), "got %v, want %v", got, tt.want)
		})
	}
}
